package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testApp returns an app with limits loose enough to not interfere; tests
// that exercise a specific limit override the relevant field.
func testApp() *app {
	return &app{
		store:    newStore(time.Hour, 1000),
		maxBody:  1000,
		postLim:  newLimiter(100000),
		otherLim: newLimiter(100000),
	}
}

func do(t *testing.T, h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, r)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// uploadPath uploads body under name and returns the resulting /slug/name path.
func uploadPath(t *testing.T, h http.Handler, name, body string) string {
	t.Helper()
	w := do(t, h, "POST", "/"+name, body)
	if w.Code != http.StatusOK {
		t.Fatalf("upload %q: status %d, body %q", name, w.Code, w.Body.String())
	}
	url := strings.TrimSpace(w.Body.String())
	i := strings.Index(url, "://")
	if i == -1 {
		t.Fatalf("unexpected upload response %q", url)
	}
	return url[strings.Index(url[i+3:], "/")+i+3:]
}

func TestUploadGetDelete(t *testing.T) {
	h := testApp().handler()
	p := uploadPath(t, h, "hello.txt", "hi there")

	w := do(t, h, "GET", p, "")
	if w.Code != http.StatusOK || w.Body.String() != "hi there" {
		t.Fatalf("get: status %d, body %q", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content type = %q, want text/plain", ct)
	}
	if cl := w.Header().Get("Content-Length"); cl != "8" {
		t.Errorf("content length = %q, want 8", cl)
	}

	if w := do(t, h, "DELETE", p, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete: status %d", w.Code)
	}
	if w := do(t, h, "GET", p, ""); w.Code != http.StatusNotFound {
		t.Fatalf("get after delete: status %d", w.Code)
	}
}

func TestHead(t *testing.T) {
	h := testApp().handler()
	p := uploadPath(t, h, "a.txt", "12345")
	w := do(t, h, "HEAD", p, "")
	if w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Fatalf("head: status %d, body %q", w.Code, w.Body.String())
	}
	if cl := w.Header().Get("Content-Length"); cl != "5" {
		t.Errorf("content length = %q, want 5", cl)
	}
}

func TestSlugRedirect(t *testing.T) {
	h := testApp().handler()
	p := uploadPath(t, h, "hello.txt", "hi")
	slug := strings.Split(p, "/")[1]
	w := do(t, h, "GET", "/"+slug, "")
	if w.Code != http.StatusFound || w.Header().Get("Location") != p {
		t.Fatalf("redirect: status %d, location %q, want %q", w.Code, w.Header().Get("Location"), p)
	}
}

func TestInvalidNames(t *testing.T) {
	h := testApp().handler()
	for _, name := range []string{"a%20b", "-flag", "a%2Fb", "sp%C3%A4ter.txt"} {
		if w := do(t, h, "POST", "/"+name, "x"); w.Code != http.StatusBadRequest {
			t.Errorf("upload %q: status %d, want 400", name, w.Code)
		}
	}
}

func TestValidName(t *testing.T) {
	valid := []string{"a", "notes.txt", "A-1_b.c", "README", strings.Repeat("x", 255), "..hidden"}
	invalid := []string{"", ".", "..", "-x", "a b", "a/b", "ä.txt", strings.Repeat("x", 256)}
	for _, n := range valid {
		if !validName(n) {
			t.Errorf("validName(%q) = false, want true", n)
		}
	}
	for _, n := range invalid {
		if validName(n) {
			t.Errorf("validName(%q) = true, want false", n)
		}
	}
}

func TestReplace(t *testing.T) {
	h := testApp().handler()
	p := uploadPath(t, h, "note.txt", "v1")

	if w := do(t, h, "POST", p, "v2"); w.Code != http.StatusOK {
		t.Fatalf("replace: status %d", w.Code)
	}
	if w := do(t, h, "GET", p, ""); w.Body.String() != "v2" {
		t.Fatalf("get after replace: body %q, want v2", w.Body.String())
	}

	slug := strings.Split(p, "/")[1]
	if w := do(t, h, "POST", "/"+slug+"/other.txt", "x"); w.Code != http.StatusNotFound {
		t.Errorf("replace with new name on existing slug: status %d, want 404", w.Code)
	}
	if w := do(t, h, "POST", "/nosuchslug111/note.txt", "x"); w.Code != http.StatusNotFound {
		t.Errorf("replace on unknown slug: status %d, want 404", w.Code)
	}
}

func TestBodyTooLarge(t *testing.T) {
	a := testApp()
	a.maxBody = 10
	h := a.handler()
	if w := do(t, h, "POST", "/big.bin", strings.Repeat("x", 11)); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload: status %d, want 413", w.Code)
	}
	if w := do(t, h, "POST", "/ok.bin", strings.Repeat("x", 10)); w.Code != http.StatusOK {
		t.Fatalf("max-size upload: status %d, want 200", w.Code)
	}
}

// failReader fails the test if anything tries to read from it.
type failReader struct{ t *testing.T }

func (f failReader) Read([]byte) (int, error) {
	f.t.Fatal("body was read")
	return 0, errors.New("unreachable")
}

// rawRequest builds a POST with an explicit Content-Length, bypassing
// httptest.NewRequest's inference from the body.
func rawRequest(target string, body io.Reader, contentLength int64) *http.Request {
	req := httptest.NewRequest("POST", target, nil)
	req.Body = io.NopCloser(body)
	req.ContentLength = contentLength
	return req
}

func TestBodyTooLargeRejectedBeforeRead(t *testing.T) {
	a := testApp()
	a.maxBody = 10
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, rawRequest("/big.bin", failReader{t}, 11))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("declared oversize upload: status %d, want 413", w.Code)
	}
}

func TestBodyTooLargeChunked(t *testing.T) {
	a := testApp()
	a.maxBody = 10
	h := a.handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, rawRequest("/big.bin", strings.NewReader(strings.Repeat("x", 11)), -1))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked oversize upload: status %d, want 413", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, rawRequest("/ok.bin", strings.NewReader("chunked"), -1))
	if w.Code != http.StatusOK {
		t.Fatalf("chunked upload: status %d, want 200", w.Code)
	}
	if got := do(t, h, "GET", uploadPath(t, h, "x.txt", "y"), "").Body.String(); got != "y" {
		t.Fatalf("sanity get: %q", got)
	}
}

func TestBodyShorterThanContentLength(t *testing.T) {
	a := testApp()
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, rawRequest("/short.bin", strings.NewReader("abc"), 5))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("truncated body: status %d, want 400", w.Code)
	}
	if a.store.total != 0 {
		t.Fatalf("truncated upload was stored: total=%d", a.store.total)
	}
}

func TestEviction(t *testing.T) {
	a := testApp()
	a.store = newStore(time.Hour, 1000)
	h := a.handler()
	body := strings.Repeat("x", 400)
	p1 := uploadPath(t, h, "a.txt", body)
	p2 := uploadPath(t, h, "b.txt", body)
	p3 := uploadPath(t, h, "c.txt", body) // 1200 > 1000: evicts oldest

	if w := do(t, h, "GET", p1, ""); w.Code != http.StatusNotFound {
		t.Errorf("oldest bucket should be evicted, got status %d", w.Code)
	}
	for _, p := range []string{p2, p3} {
		if w := do(t, h, "GET", p, ""); w.Code != http.StatusOK {
			t.Errorf("get %s: status %d, want 200", p, w.Code)
		}
	}
}

func TestUploadRateLimit(t *testing.T) {
	a := testApp()
	a.postLim = newLimiter(2)
	h := a.handler()
	uploadPath(t, h, "a.txt", "x")
	uploadPath(t, h, "b.txt", "x")
	if w := do(t, h, "POST", "/c.txt", "x"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("third upload: status %d, want 429", w.Code)
	}
	// GETs use the other bucket and still work.
	if w := do(t, h, "GET", "/", ""); w.Code != http.StatusOK {
		t.Fatalf("get index while post-limited: status %d", w.Code)
	}
}

func TestGeneralRateLimit(t *testing.T) {
	a := testApp()
	a.otherLim = newLimiter(2)
	h := a.handler()
	do(t, h, "GET", "/", "")
	do(t, h, "GET", "/", "")
	if w := do(t, h, "GET", "/", ""); w.Code != http.StatusTooManyRequests {
		t.Fatalf("third get: status %d, want 429", w.Code)
	}
	// Uploads use their own bucket and still work.
	if w := do(t, h, "POST", "/a.txt", "x"); w.Code != http.StatusOK {
		t.Fatalf("upload while get-limited: status %d", w.Code)
	}
}

func TestExpiry(t *testing.T) {
	a := testApp()
	a.store = newStore(50*time.Millisecond, 1000)
	h := a.handler()
	p := uploadPath(t, h, "a.txt", "x")
	time.Sleep(80 * time.Millisecond)
	if w := do(t, h, "GET", p, ""); w.Code != http.StatusNotFound {
		t.Fatalf("get after expiry: status %d, want 404", w.Code)
	}
}

func TestReplaceResetsTTL(t *testing.T) {
	a := testApp()
	a.store = newStore(300*time.Millisecond, 1000)
	h := a.handler()
	p := uploadPath(t, h, "a.txt", "v1")
	time.Sleep(200 * time.Millisecond)
	if w := do(t, h, "POST", p, "v2"); w.Code != http.StatusOK {
		t.Fatalf("replace: status %d", w.Code)
	}
	time.Sleep(200 * time.Millisecond) // 400ms past first upload, 200ms past replace
	if w := do(t, h, "GET", p, ""); w.Code != http.StatusOK {
		t.Fatalf("get after replace-reset TTL: status %d, want 200", w.Code)
	}
}

func TestContentTypeDetection(t *testing.T) {
	h := testApp().handler()

	p := uploadPath(t, h, "data.json", `{"a":1}`)
	if ct := do(t, h, "GET", p, "").Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("json content type = %q", ct)
	}

	// No usable extension: sniffed from content.
	p = uploadPath(t, h, "page", "<!DOCTYPE html><html><body>hi</body></html>")
	if ct := do(t, h, "GET", p, "").Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("sniffed content type = %q", ct)
	}
}

func TestCaching(t *testing.T) {
	h := testApp().handler()
	p := uploadPath(t, h, "note.txt", "v1")

	w := do(t, h, "GET", p, "")
	if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}
	etag := w.Header().Get("Etag")
	if etag == "" {
		t.Fatal("missing ETag")
	}

	req := httptest.NewRequest("GET", p, nil)
	req.Header.Set("If-None-Match", etag)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotModified {
		t.Fatalf("conditional get: status %d, want 304", w.Code)
	}

	// Replacing must change the ETag so caches revalidate to fresh content.
	if w := do(t, h, "POST", p, "v2"); w.Code != http.StatusOK {
		t.Fatalf("replace: status %d", w.Code)
	}
	w = do(t, h, "GET", p, "")
	if w.Header().Get("Etag") == etag {
		t.Error("ETag unchanged after replace")
	}
}

func TestSlugFormat(t *testing.T) {
	slug := genSlug()
	if len(slug) != 8 {
		t.Fatalf("slug length = %d, want 8", len(slug))
	}
	for _, c := range slug {
		if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_", c) {
			t.Fatalf("slug %q contains non-base64url char %q", slug, c)
		}
	}
}

func TestBaseURLFromRequest(t *testing.T) {
	h := testApp().handler()
	req := httptest.NewRequest("POST", "http://buckets.example.com/a.txt", strings.NewReader("x"))
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if !strings.HasPrefix(w.Body.String(), "https://buckets.example.com/") {
		t.Fatalf("upload response = %q, want https://buckets.example.com/... prefix", w.Body.String())
	}
}
