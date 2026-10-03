// Buckets is a tiny in-memory pastebin with curl support.
//
// Everything is opinionated and hardcoded: port 8080, 24h TTL, 500 MB max
// body, 500 MB total storage (oldest evicted first), 12 uploads/min, 240
// requests/min for everything else. No auth, no TLS, no disk, no config.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed index.html
var indexHTML []byte

const (
	addr            = ":8080"
	defaultTTL      = 24 * time.Hour
	defaultMaxBody  = 500 * 1000 * 1000 // per upload
	defaultMaxStore = 500 * 1000 * 1000 // across all buckets
	// 15 Crockford base32 chars = 75 bits. The slug alone opens a bucket
	// (GET /<slug> redirects to the full path), so it carries the whole
	// secret: scanning at the rate limit (4 req/s) for a year against a
	// full store of 17,280 buckets hits one with odds of ~1 in 17 billion.
	slugLen           = 15
	uploadsPerMinute  = 12
	requestsPerMinute = 240
)

// file is immutable once stored; replacing creates a new value.
type file struct {
	slug        string
	name        string
	data        []byte
	contentType string
	uploaded    time.Time
	expires     time.Time
}

// store holds all buckets in memory. order is sorted by expiry (earliest
// first), which equals insertion order because the TTL is uniform and a
// replace re-inserts at the back.
type store struct {
	mu       sync.Mutex
	ttl      time.Duration
	maxBytes int
	files    map[string]*file // keyed by slug
	order    []*file
	total    int
}

func newStore(ttl time.Duration, maxBytes int) *store {
	return &store{ttl: ttl, maxBytes: maxBytes, files: make(map[string]*file)}
}

func (s *store) purgeLocked(now time.Time) {
	for len(s.order) > 0 && !s.order[0].expires.After(now) {
		s.removeLocked(s.order[0])
	}
}

func (s *store) removeLocked(f *file) {
	delete(s.files, f.slug)
	s.total -= len(f.data)
	for i, g := range s.order {
		if g == f {
			// slices.Delete zeroes the vacated tail slot; the append idiom
			// would keep the last *file reachable and its data uncollectable.
			s.order = slices.Delete(s.order, i, i+1)
			break
		}
	}
}

// evictLocked drops the oldest buckets until need more bytes fit under the cap.
func (s *store) evictLocked(need int) {
	for len(s.order) > 0 && s.total+need > s.maxBytes {
		s.removeLocked(s.order[0])
	}
}

func (s *store) insertLocked(f *file) {
	s.files[f.slug] = f
	s.order = append(s.order, f)
	s.total += len(f.data)
}

func (s *store) put(name string, data []byte, contentType string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.purgeLocked(now)
	s.evictLocked(len(data))
	var slug string
	for {
		slug = genSlug()
		if _, taken := s.files[slug]; !taken {
			break
		}
	}
	s.insertLocked(&file{slug, name, data, contentType, now, now.Add(s.ttl)})
	return slug
}

// replace overwrites an existing bucket. The slug and filename must both
// match; this is the only way to write to a chosen path, so slugs always
// come from genSlug. Replacing resets the TTL.
func (s *store) replace(slug, name string, data []byte, contentType string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.purgeLocked(now)
	old, ok := s.files[slug]
	if !ok || old.name != name {
		return false
	}
	s.removeLocked(old)
	s.evictLocked(len(data))
	s.insertLocked(&file{slug, name, data, contentType, now, now.Add(s.ttl)})
	return true
}

func (s *store) get(slug string) (*file, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeLocked(time.Now())
	f, ok := s.files[slug]
	return f, ok
}

func (s *store) delete(slug, name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeLocked(time.Now())
	f, ok := s.files[slug]
	if !ok || f.name != name {
		return false
	}
	s.removeLocked(f)
	return true
}

// slugAlphabet is Crockford base32: digits and lowercase letters without
// i, l, o and u, so a slug can be read aloud or retyped on another device
// without lookalike mistakes. Lookups are case-insensitive and map i, l to
// 1 and o to 0 (see normSlug).
const slugAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

func genSlug() string {
	b := make([]byte, slugLen)
	if _, err := rand.Read(b); err != nil {
		log.Panicf("crypto/rand failed: %v", err)
	}
	for i := range b {
		b[i] = slugAlphabet[b[i]&31]
	}
	return string(b)
}

// normSlug maps what a human might have typed back to the canonical slug.
func normSlug(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case 'I', 'i', 'L', 'l':
			return '1'
		case 'O', 'o':
			return '0'
		}
		if 'A' <= r && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s)
}

// limiter is a global token bucket.
type limiter struct {
	mu     sync.Mutex
	tokens float64
	burst  float64
	perSec float64
	last   time.Time
}

func newLimiter(perMinute float64) *limiter {
	return &limiter{tokens: perMinute, burst: perMinute, perSec: perMinute / 60, last: time.Now()}
}

func (l *limiter) allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.tokens = min(l.burst, l.tokens+now.Sub(l.last).Seconds()*l.perSec)
	l.last = now
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}

type app struct {
	store    *store
	maxBody  int64
	postLim  *limiter
	otherLim *limiter
}

func newApp() *app {
	return &app{
		store:    newStore(defaultTTL, defaultMaxStore),
		maxBody:  defaultMaxBody,
		postLim:  newLimiter(uploadsPerMinute),
		otherLim: newLimiter(requestsPerMinute),
	}
}

func (a *app) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", a.index)
	mux.HandleFunc("POST /{name}", a.upload)
	mux.HandleFunc("POST /{slug}/{name}", a.replace)
	mux.HandleFunc("GET /{slug}", a.redirect)
	mux.HandleFunc("GET /{slug}/{name}", a.serve)
	mux.HandleFunc("DELETE /{slug}/{name}", a.delete)
	return a.rateLimit(mux)
}

func (a *app) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lim := a.otherLim
		if r.Method == http.MethodPost {
			lim = a.postLim
		}
		if !lim.allow() {
			w.Header().Set("Retry-After", "10")
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *app) index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

func (a *app) upload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !validName(name) {
		http.Error(w, "invalid filename: 1-255 chars of A-Za-z0-9._- with no leading '-'", http.StatusBadRequest)
		return
	}
	data, ok := a.readBody(w, r)
	if !ok {
		return
	}
	slug := a.store.put(name, data, detectContentType(name, data))
	fmt.Fprintf(w, "%s/%s/%s\n", baseURL(r), slug, name)
}

func (a *app) replace(w http.ResponseWriter, r *http.Request) {
	slug, name := normSlug(r.PathValue("slug")), r.PathValue("name")
	data, ok := a.readBody(w, r)
	if !ok {
		return
	}
	if !a.store.replace(slug, name, data, detectContentType(name, data)) {
		http.NotFound(w, r)
		return
	}
	fmt.Fprintf(w, "%s/%s/%s\n", baseURL(r), slug, name)
}

func (a *app) serve(w http.ResponseWriter, r *http.Request) {
	f, ok := a.store.get(normSlug(r.PathValue("slug")))
	if !ok || f.name != r.PathValue("name") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", f.contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Buckets are mutable (replace) and expire, so force revalidation.
	// The ETag carries nanosecond precision; Last-Modified alone would
	// serve stale 304s for replacements within the same second.
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Etag", fmt.Sprintf(`"%x-%x"`, f.uploaded.UnixNano(), len(f.data)))
	http.ServeContent(w, r, "", f.uploaded, bytes.NewReader(f.data))
}

func (a *app) redirect(w http.ResponseWriter, r *http.Request) {
	f, ok := a.store.get(normSlug(r.PathValue("slug")))
	if !ok {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/"+f.slug+"/"+f.name, http.StatusFound)
}

func (a *app) delete(w http.ResponseWriter, r *http.Request) {
	if !a.store.delete(normSlug(r.PathValue("slug")), r.PathValue("name")) {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *app) readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.ContentLength > a.maxBody {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	if n := r.ContentLength; n > 0 {
		// Read straight into a right-sized buffer. io.ReadAll grows
		// geometrically and peaks at ~2.4x the body for a 500 MB upload.
		data := make([]byte, n)
		if _, err := io.ReadFull(r.Body, data); err != nil {
			http.Error(w, "error reading body", http.StatusBadRequest)
			return nil, false
		}
		return data, true
	}
	// Chunked body (e.g. curl -T -): size unknown, fall back to ReadAll.
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, a.maxBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "error reading body", http.StatusBadRequest)
		}
		return nil, false
	}
	return data, true
}

// validName reports whether name is a POSIX portable filename: 1-255 bytes
// of [A-Za-z0-9._-], not "." or "..", no leading '-'.
func validName(name string) bool {
	if len(name) == 0 || len(name) > 255 || name == "." || name == ".." || name[0] == '-' {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' ||
			c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// detectContentType picks the content type once at upload time: by
// extension first, sniffed from the payload otherwise.
func detectContentType(name string, data []byte) string {
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		return ct
	}
	return http.DetectContentType(data)
}

// baseURL reconstructs the external URL from the request so no base-URL
// configuration is needed.
func baseURL(r *http.Request) string {
	scheme := "http"
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	}
	return scheme + "://" + r.Host
}

// janitor frees expired buckets between requests so a dead 500 MB upload
// doesn't linger until the next hit.
func (a *app) janitor(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.store.mu.Lock()
			a.store.purgeLocked(time.Now())
			a.store.mu.Unlock()
		}
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a := newApp()
	go a.janitor(ctx)

	srv := &http.Server{
		Addr:    addr,
		Handler: a.handler(),
		// Together with the upload rate limit these bound how much memory
		// stalled or malicious connections can hold: slow header attacks
		// die in seconds, and a stalled 500 MB upload dies within
		// ReadTimeout instead of buffering forever. 30 min still allows
		// a max-size upload at ~300 KB/s.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Printf("buckets listening on %s", addr)

	select {
	case err := <-errc:
		log.Fatal(err)
	case <-ctx.Done():
		log.Print("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Print(err)
		}
	}
}
