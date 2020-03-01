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
	"time"
)

const (
	storeGet = iota
	storeDelete
	storeSetWithPath
	storeSet
	storePresent
	storeNotPresent
)

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


	return func (w http.ResponseWriter, r *http.Request) {
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
				request <- storeOp{storeGet, path}
				op := <-response
				if op.method == storePresent {
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
				if path != "" {
					request <- storeOp{storeSetWithPath, setWithPath{path, bytes}}
				} else {
					request <- storeOp{storeSet, bytes}
				}
				op := <- response
				_, err := w.Write([]byte(baseUrl + "/" + op.body.(string) + "\n"))
				if err != nil {
					log.Println(err)
				}
			}
		} else if r.Method == http.MethodDelete {
			request <- storeOp{storeDelete, path}
			op := <- response
			if op.method == storeNotPresent {
				w.WriteHeader(http.StatusNotFound)
			}
		} else {
			w.WriteHeader(http.StatusBadRequest)
		}
	}
}


func manageStore(request chan storeOp, response chan storeOp) {
	store := make(map[string][]byte)
	timeouts := make([]time.Time, 0)
	keys := make([]string, 0)

	for true {
		timeout := time.Duration(1<<63-1) //never timeout
		for len(timeouts) > 0 {
			if timeouts[0].Before(time.Now()){
				t := timeouts[0]
				log.Println("timeout")
				log.Println(t.Unix())
				log.Println(time.Now().Unix())
				delete(store, keys[0])
				deleteKey(&keys, &timeouts, keys[0])
				continue
			}
			break
		}
		if len(timeouts) > 0 {
			timeout = timeouts[0].Sub(time.Now())
		}

		select {
		case op := <-request:
			if op.method == storeGet {
				if val, ok := store[op.body.(string)]; ok {
					response <- storeOp{storePresent, val}
				} else {
					response <- storeOp{storeNotPresent, nil}
				}
			} else if op.method == storeDelete {
				if _, ok := store[op.body.(string)]; ok {
					delete(store, op.body.(string))
					deleteKey(&keys, &timeouts, op.body.(string))
					response <- storeOp{storePresent, nil}
				} else {
					response <- storeOp{storeNotPresent, nil}
				}
			} else if op.method == storeSetWithPath || op.method == storeSet {
				path := ""
				var body []byte = nil
				if len(keys) == maxBuckets {
					delete(store, keys[0])
					deleteKey(&keys, &timeouts, keys[0])
				}
				if op.method == storeSetWithPath {
					path = op.body.(setWithPath).path
					body = op.body.(setWithPath).body
					if _, ok := store[path]; ok {
						delete(store, path)
						deleteKey(&keys, &timeouts, path)
					}
				} else {
					retry := true
					for retry {
						path = genSlug()
						_, retry = store[path]
					}
					body = op.body.([]byte)
				}
				store[path] = body
				keys = append(keys, path)
				if ttl == 0 {
					timeouts = append(timeouts, time.Unix(1<<33 - 1, 0))
				} else {
					timeouts = append(timeouts, time.Now().Add(time.Duration(ttl)*time.Second))
				}
				response <- storeOp{storeSet, path}
			} else {
				log.Panicf("unknown store method: %d", op.method)
			}
		case <- time.After(timeout):
			delete(store, keys[0])
			deleteKey(&keys, &timeouts, keys[0])
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

func deleteKey(keys *[]string, timeouts *[]time.Time, key string) {
	ind := -1
	for i, val := range *keys {
		if val == key {
			ind = i
			break
		}
	}
	if ind == -1 {
		log.Panic("could not delete non existing key")
	}
	if ind == 0 {
		*keys = (*keys)[1:]
		*timeouts = (*timeouts)[1:]
	} else {
		copy((*keys)[ind:], (*keys)[ind+1:])
		*keys = (*keys)[:len(*keys)-1]

		copy((*timeouts)[ind:], (*timeouts)[ind+1:])
		*timeouts = (*timeouts)[:len(*timeouts)-1]
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
