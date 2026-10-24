package server

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func pngBytes(t *testing.T, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 40, 30))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	img.Set(5, 5, c)
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// origin serves one PNG at /a.png; the test swaps the image or the status.
type origin struct {
	*httptest.Server
	mu     sync.Mutex
	body   []byte
	status int
}

func newOrigin(t *testing.T, c color.Color) *origin {
	t.Helper()
	o := &origin{body: pngBytes(t, c), status: http.StatusOK}
	o.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.mu.Lock()
		defer o.mu.Unlock()
		if o.status != http.StatusOK {
			http.Error(w, "gone", o.status)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(o.body)
	}))
	t.Cleanup(o.Close)
	return o
}

func (o *origin) set(status int, body []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.status = status
	if body != nil {
		o.body = body
	}
}

// src is the ?url= value of the origin's image.
func (o *origin) src() string { return url.QueryEscape(o.URL + "/a.png") }

func cacheServer(t *testing.T, o *origin, cacheDir string, ttl time.Duration, stale bool) http.Handler {
	t.Helper()
	// No PDFium: PNG sources never reach the rasteriser.
	s := newServer(Config{maxBytes: 1 << 20, defaultDPI: 150, maxSide: 2000, Timeout: 30 * time.Second,
		fetchAllow: regexp.MustCompile("^" + regexp.QuoteMeta(o.URL) + "/"), fetchLoopback: true, cacheMB: 16, cacheControl: "public, max-age=60", concurrency: 2, queue: 4,
		cacheDir: cacheDir, cacheSize: 1 << 20, cacheTTL: ttl, cacheStale: stale}, nil, nil)
	t.Cleanup(s.Close)
	if s.cache == nil {
		t.Fatal("disk cache not opened")
	}
	return s.Handler()
}

func do(t *testing.T, h http.Handler, method, url string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, url, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestDiskCacheByURL(t *testing.T) {
	o, dir := newOrigin(t, color.Black), t.TempDir()
	h := cacheServer(t, o, dir, 0, true)
	u := "/render?url=" + o.src() + "&width=20"

	first := do(t, h, "GET", u)
	if first.Code != 200 || first.Header().Get("X-Cache") != "miss" || !strings.Contains(first.Header().Get("Cache-Status"), "stored") {
		t.Fatalf("first: %d %v", first.Code, first.Header())
	}
	etag := first.Header().Get("ETag")

	// The source is gone: a hit must not touch it.
	o.set(http.StatusNotFound, nil)
	second := do(t, h, "GET", u)
	if second.Code != 200 || second.Header().Get("X-Cache") != "disk" || !bytes.Equal(second.Body.Bytes(), first.Body.Bytes()) {
		t.Fatalf("second: %d %v", second.Code, second.Header())
	}
	if second.Header().Get("ETag") != etag || second.Header().Get("X-Page-Count") != "1" || second.Header().Get("Cache-Status") != "docshot; hit" {
		t.Fatalf("replayed headers: %v", second.Header())
	}
	if w := do(t, h, "GET", u, "If-None-Match", etag); w.Code != http.StatusNotModified || w.Body.Len() != 0 {
		t.Fatalf("If-None-Match: %d", w.Code)
	}
	if w := do(t, h, "GET", u, "If-Modified-Since", time.Now().Add(time.Minute).UTC().Format(http.TimeFormat)); w.Code != http.StatusNotModified {
		t.Fatalf("If-Modified-Since: %d", w.Code)
	}
	// Parameter order, sign-free canonical form: same entry.
	if w := do(t, h, "GET", "/render?width=20&url="+o.src()); w.Header().Get("X-Cache") != "disk" {
		t.Fatalf("canonical key: %v", w.Header())
	}

	// After a restart, from the same cache directory.
	h2 := cacheServer(t, o, dir, 0, true)
	if w := do(t, h2, "GET", u); w.Code != 200 || w.Header().Get("X-Cache") != "disk" {
		t.Fatalf("after restart: %d %v", w.Code, w.Header())
	}

	// ?v= is a new key with an immutable Cache-Control.
	o.set(http.StatusOK, nil)
	v := do(t, h2, "GET", u+"&v=2")
	if v.Header().Get("X-Cache") == "disk" || !strings.Contains(v.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("?v=: %v", v.Header())
	}

	// Disabling the source disables its hits too.
	off := newServer(Config{maxBytes: 1 << 20, defaultDPI: 150, maxSide: 2000, Timeout: time.Minute, cacheMB: 1,
		concurrency: 1, queue: 1, cacheDir: dir, cacheSize: 1 << 20}, nil, nil)
	defer off.Close()
	if w := do(t, off.Handler(), "GET", u); w.Code != http.StatusForbidden {
		t.Fatalf("hit with DOCSHOT_FETCH_ALLOW unset: %d", w.Code)
	}

	purge := do(t, h2, "DELETE", "/cache?url="+o.src())
	if purge.Code != 200 || !strings.Contains(purge.Body.String(), `"purged":2`) {
		t.Fatalf("purge: %d %s", purge.Code, purge.Body)
	}
	if w := do(t, h2, "GET", u); w.Header().Get("X-Cache") == "disk" {
		t.Fatal("purged entry served")
	}

	m := do(t, h2, "GET", "/metrics").Body.String()
	for _, want := range []string{
		`docshot_cache_requests_total{result="hit"}`,
		`docshot_documents_total{op="page",route="image",type="png"}`,
		`docshot_cache_entries{type="png"} 1`,
		`docshot_http_requests_total{code="200",handler="/render",method="get"}`,
		"go_goroutines", "process_",
	} {
		if !strings.Contains(m, want) {
			t.Errorf("metrics lack %s", want)
		}
	}
}

func TestDiskCacheRevalidation(t *testing.T) {
	o, dir := newOrigin(t, color.Black), t.TempDir()
	h := cacheServer(t, o, dir, time.Nanosecond, false) // always expired, re-checked inline
	u := "/render?url=" + o.src()

	first := do(t, h, "GET", u)
	if first.Header().Get("X-Cache") != "miss" {
		t.Fatalf("first: %v", first.Header())
	}
	if w := do(t, h, "GET", u); w.Header().Get("X-Cache") != "revalidated" || !strings.Contains(w.Header().Get("Cache-Status"), "fwd-status=304") {
		t.Fatalf("unchanged source: %v", w.Header())
	}
	o.set(http.StatusOK, pngBytes(t, color.White))
	w := do(t, h, "GET", u)
	if w.Header().Get("X-Cache") == "revalidated" || w.Header().Get("ETag") == first.Header().Get("ETag") {
		t.Fatalf("changed source: %v", w.Header())
	}

	// Unreachable source: the stale entry is served (stale-if-error).
	o.set(http.StatusInternalServerError, nil)
	if w := do(t, h, "GET", u); w.Code != http.StatusOK || w.Header().Get("X-Cache") != "stale" {
		t.Fatalf("failing source: %d %v", w.Code, w.Header())
	}
}

func TestStaleWhileRevalidate(t *testing.T) {
	o, dir := newOrigin(t, color.Black), t.TempDir()
	h := cacheServer(t, o, dir, time.Nanosecond, true)
	u := "/render?url=" + o.src()
	do(t, h, "GET", u)
	w := do(t, h, "GET", u)
	if w.Header().Get("X-Cache") != "stale" || !strings.Contains(w.Header().Get("Cache-Status"), "ttl=-") {
		t.Fatalf("stale: %v", w.Header())
	}
	io.Copy(io.Discard, w.Body)
}

func TestUploadCachedByContent(t *testing.T) {
	o, dir := newOrigin(t, color.Black), t.TempDir()
	body := pngBytes(t, color.Black)
	h := cacheServer(t, o, dir, 0, true)
	up := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/upload?name=a.png", bytes.NewReader(body))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	up()
	if w := up(); w.Header().Get("X-Cache") != "disk" || w.Header().Get("Cache-Control") != "" {
		t.Fatalf("second upload: %v", w.Header())
	}
}

func TestParseSizeAndTTL(t *testing.T) {
	for in, want := range map[string]int64{"10GB": 10 << 30, "10g": 10 << 30, "1GiB": 1 << 30, "512MB": 512 << 20, "1.5K": 1536, "100": 100, "0": 0} {
		if got, err := parseSize(in); err != nil || got != want {
			t.Errorf("parseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"10XB", "", "GB", "-1G"} {
		if _, err := parseSize(bad); err == nil {
			t.Errorf("parseSize(%q) accepted", bad)
		}
	}
	for in, want := range map[string]time.Duration{"0": 0, "7d": 7 * 24 * time.Hour, "12h": 12 * time.Hour, "90": 90 * time.Second} {
		if got, err := parseTTL(in); err != nil || got != want {
			t.Errorf("parseTTL(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseTTL("soon"); err == nil {
		t.Error("parseTTL accepted junk")
	}
}
