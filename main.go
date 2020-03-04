package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	storeGet = iota
	storeDelete
	storeSetWithPath
	storeSet
	storeTooLarge
	storePresent
	storeNotPresent
)

type store struct {
	lookup map[string][]byte
	timeouts []time.Time
	keys []string
	sizes []int
	totalSize int
}

type storeOp struct {
	method int
	body   interface{}
}

type setWithPath struct {
	path string
	body []byte
}

var baseUrl string = "http://localhost:8080"
var charset = "abcdefghijklmnopqrstuvwxyz"
var ttl = 172800 //Time to live in seconds, 0 means unlimited, default: 2 days
var maxBuckets = 1000
var slugSize = 4
var seededRand *rand.Rand = rand.New(rand.NewSource(time.Now().UnixNano()))
var maxStorageSize float64 = 1000 //in MB (i.e. 1000000 bytes)

var storeMutex sync.Mutex

func main() {
	loadEnv()

	if slowIntPow(len(charset), slugSize) < maxBuckets {
		log.Panic("slug size and charset too small for max number of buckets")
	}

	req := make(chan storeOp)
	resp := make(chan storeOp)
	go manageStore(req, resp)

	http.HandleFunc("/", makeHandler(req, resp))
	err := http.ListenAndServe(":8080", nil)
	if err != nil {
		log.Panic(err)
	}
}

func makeHandler(request chan storeOp, response chan storeOp) func(http.ResponseWriter, *http.Request) {
	indexHtml := ""
	{
		file, err := os.Open("index.html")
		if err != nil {
			log.Panic(err)
		}
		indexHtmlFile, err := ioutil.ReadAll(file)
		if err != nil {
			log.Panic(err)
		}
		err = file.Close()
		if err != nil {
			log.Println(err)
		}
		indexHtml = string(indexHtmlFile)
		indexHtml = strings.ReplaceAll(indexHtml, "{{baseurl}}", baseUrl)
	}

	return func(w http.ResponseWriter, r *http.Request) {
		path := ""
		if len(r.URL.Path) > 0 {
			path = r.URL.Path[1:]
		}
		if r.Method == http.MethodGet {
			if path == "" {
				_, err := w.Write([]byte(indexHtml))
				if err != nil {
					log.Println(err)
				}
			} else {
				storeMutex.Lock()
				request <- storeOp{storeGet, path}
				op := <-response
				storeMutex.Unlock()
				if op.method == storePresent {
					w.Header().Set("content-length", strconv.Itoa(len(op.body.([]byte))))
					_, err := w.Write(op.body.([]byte))
					if err != nil {
						log.Println(err)
					}
				} else {
					w.WriteHeader(http.StatusNotFound)
				}
			}
		} else if r.Method == http.MethodPost {
			bytes, err := ioutil.ReadAll(r.Body)
			if err != nil {
				log.Println(err)
				w.WriteHeader(http.StatusInternalServerError)
			} else {
				storeMutex.Lock()
				if path != "" {
					request <- storeOp{storeSetWithPath, setWithPath{path, bytes}}
				} else {
					request <- storeOp{storeSet, bytes}
				}
				op := <-response
				storeMutex.Unlock()
				if op.method == storeTooLarge {
					w.WriteHeader(http.StatusRequestEntityTooLarge)
				} else {
					_, err := w.Write([]byte(baseUrl + "/" + op.body.(string) + "\n"))
					if err != nil {
						log.Println(err)
					}
				}
			}
		} else if r.Method == http.MethodDelete {
			storeMutex.Lock()
			request <- storeOp{storeDelete, path}
			op := <-response
			storeMutex.Unlock()
			if op.method == storeNotPresent {
				w.WriteHeader(http.StatusNotFound)
			}
		} else {
			w.WriteHeader(http.StatusBadRequest)
		}
	}
}

func manageStore(request chan storeOp, response chan storeOp) {
	s := store{make(map[string][]byte), make([]time.Time, 0), make([]string, 0), make([]int, 0), 0}

	for true {
		timeout := time.Duration(1<<63 - 1) //never timeout
		for len(s.timeouts) > 0 {
			now := time.Now()
			if s.timeouts[0].Before(now) || s.timeouts[0].Equal(now) {
				s.deleteFirstN(1)
				continue
			}
			timeout = s.timeouts[0].Sub(now)
			break
		}

		select {
		case op := <-request:
			if op.method == storeGet {
				if val, ok := s.lookup[op.body.(string)]; ok {
					response <- storeOp{storePresent, val}
				} else {
					response <- storeOp{storeNotPresent, nil}
				}
			} else if op.method == storeDelete {
				if _, ok := s.lookup[op.body.(string)]; ok {
					s.deleteKey(op.body.(string))
					response <- storeOp{storePresent, nil}
				} else {
					response <- storeOp{storeNotPresent, nil}
				}
			} else if op.method == storeSetWithPath || op.method == storeSet {
				path := ""
				var body []byte
				if op.method == storeSetWithPath {
					path = op.body.(setWithPath).path
					body = op.body.(setWithPath).body
					if float64(len(body))/(1000000) > maxStorageSize {
						response <- storeOp{storeTooLarge, nil}
						break
					}
					if _, ok := s.lookup[path]; ok {
						s.deleteKey(path)
					}
				} else {
					retry := true
					for retry {
						path = genSlug()
						_, retry = s.lookup[path]
					}
					body = op.body.([]byte)
					if float64(len(body))/(1000000) > maxStorageSize {
						response <- storeOp{storeTooLarge, nil}
						break
					}
				}
				s.clearSpace(len(body))
				s.lookup[path] = body
				s.keys = append(s.keys, path)
				s.sizes = append(s.sizes, len(body))
				s.totalSize += len(body)
				if ttl == 0 {
					s.timeouts = append(s.timeouts, time.Unix(1<<33-1, 0))
				} else {
					s.timeouts = append(s.timeouts, time.Now().Add(time.Duration(ttl)*time.Second))
				}
				response <- storeOp{storeSet, path}
			} else {
				log.Panicf("unknown store method: %d", op.method)
			}
		case <-time.After(timeout):
			s.deleteFirstN(1)
		}
	}
}

func genSlug() string {
	b := make([]byte, slugSize)
	for i := range b {
		b[i] = charset[seededRand.Intn(len(charset))]
	}
	return string(b)
}

func (s *store) clearSpace(dataSize int)  {
	delCount := 0
	delSize := 0
	for maxStorageSize < float64(s.totalSize - delSize + dataSize) / 1000000 {
		delSize += s.sizes[delCount]
		delCount++
	}
	if len(s.keys) == maxBuckets && delCount == 0 {
		delCount = 1
	}
	s.deleteFirstN(delCount)
}

func (s *store) deleteFirstN(n int) {
	for i, key := range s.keys {
		if i == n {
			break
		}
		s.totalSize -= s.sizes[i]
		delete(s.lookup, key)
	}

	s.keys = s.keys[n:]
	s.timeouts = s.timeouts[n:]
	s.sizes = s.sizes[n:]
}

func (s *store) deleteKey(key string) {
	ind := -1
	for i, val := range s.keys {
		if val == key {
			ind = i
			break
		}
	}
	if ind == -1 {
		log.Panic("could not delete non existing key")
	}

	delete(s.lookup, key)
	s.totalSize -= s.sizes[ind]

	if ind == 0 {
		s.keys = s.keys[1:]
		s.timeouts = s.timeouts[1:]
		s.sizes = s.sizes[1:]
	} else {
		copy(s.keys[ind:], s.keys[ind+1:])
		s.keys = s.keys[:len(s.keys)-1]

		copy(s.timeouts[ind:], s.timeouts[ind+1:])
		s.timeouts = s.timeouts[:len(s.timeouts)-1]

		copy(s.sizes[ind:], s.sizes[ind+1:])
		s.sizes = s.sizes[:len(s.sizes)-1]
	}
}

func slowIntPow(x, y int) int {
	res := 1
	for i := 0; i < y; i++ {
		res *= x
	}
	return res
}

func loadEnv() {
	val := os.Getenv("BUCKETS_BASE_URL")
	if val != "" {
		baseUrl = val
	}
	val = os.Getenv("BUCKETS_CHARSET")
	if val != "" {
		charset = val
	}
	loadIntEnv("BUCKETS_TTL", &ttl)
	loadIntEnv("BUCKETS_MAX_BUCKETS", &maxBuckets)
	loadIntEnv("BUCKETS_SLUG_SIZE", &slugSize)
	seed := 0
	if loadIntEnv("BUCKETS_SEED", &seed) {
		seededRand = rand.New(rand.NewSource(int64(seed)))
	}
	loadFloat64Env("BUCKETS_MAX_STORAGE_SIZE", &maxStorageSize)
}

func loadIntEnv(key string, target *int) bool {
	val := os.Getenv(key)
	if val != "" {
		i, err := strconv.Atoi(val)
		if err != nil {
			log.Println(fmt.Errorf("erroring interpreting %s (%s) as int: %s", key, val, err.Error()))
		} else {
			*target = i
			return true
		}
	}
	return false
}

func loadFloat64Env(key string, target *float64) bool {
	val := os.Getenv(key)
	if val != "" {
		i, err := strconv.ParseFloat(val, 64)
		if err != nil {
			log.Println(fmt.Errorf("erroring interpreting %s (%s) as float64: %s", key, val, err.Error()))
		} else {
			*target = i
			return true
		}
	}
	return false
}
