package server

import (
	"bytes"
	"context"
	"image/color"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigStrict(t *testing.T) {
	t.Setenv("DOCSHOT_TIMEOUT_SECONDS", "3m")
	t.Setenv("DOCSHOT_CONCURRENCY", "0")
	t.Setenv("DOCSHOT_CACHE_TAGS", "yes please")
	t.Setenv("DOCSHOT_MAX_BYTES", "lots")
	t.Setenv("DOCSHOT_STORE_STRATEGY", "overwrite")
	t.Setenv("DOCSHOT_FETCH_ALLOW", "https://(a")
	_, err := LoadConfig()
	if err == nil {
		t.Fatal("bad settings accepted")
	}
	for _, key := range []string{"DOCSHOT_TIMEOUT_SECONDS", "DOCSHOT_CONCURRENCY", "DOCSHOT_CACHE_TAGS", "DOCSHOT_MAX_BYTES", "DOCSHOT_STORE_STRATEGY", "DOCSHOT_FETCH_ALLOW"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error does not name %s: %v", key, err)
		}
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	t.Setenv("DOCSHOT_CACHE_TAGS", "1")
	t.Setenv("DOCSHOT_MAX_BYTES", "2MB")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.cacheTags || cfg.maxBytes != 2<<20 || cfg.Timeout != 180*time.Second || !cfg.cacheStale {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestCompileAllowAnchored(t *testing.T) {
	re, err := compileAllow(`https://files\.example\.com/`)
	if err != nil {
		t.Fatal(err)
	}
	for u, want := range map[string]bool{
		"https://files.example.com/a.pdf":                  true,
		"https://evil.test/?u=https://files.example.com/x": false,
		"http://files.example.com/a.pdf":                   false,
	} {
		if re.MatchString(u) != want {
			t.Errorf("%s: match %v, want %v", u, !want, want)
		}
	}
}

func liveServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	cfg.defaultDPI, cfg.maxSide, cfg.cacheMB, cfg.concurrency, cfg.queue = 150, 2000, 16, 2, 4
	if cfg.maxBytes == 0 {
		cfg.maxBytes = 1 << 20
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	s := newServer(cfg, nil, nil)
	t.Cleanup(s.Close)
	return s
}

func serveReq(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// The allow-list matches, but the address is loopback: refused at dial time.
func TestFetchDialGuard(t *testing.T) {
	o := newOrigin(t, color.Black)
	s := liveServer(t, Config{fetchAllow: regexp.MustCompile("^" + regexp.QuoteMeta(o.URL) + "/")})
	w := serveReq(s.Handler(), httptest.NewRequest("GET", "/render?url="+o.src(), nil))
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "loopback") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestUploadSigning(t *testing.T) {
	key := []byte("k")
	s := liveServer(t, Config{signKey: key})
	h := s.Handler()
	img := pngBytes(t, color.Black)
	upload := func(target string) *httptest.ResponseRecorder {
		return serveReq(h, httptest.NewRequest("POST", target, bytes.NewReader(img)))
	}

	if w := upload("/upload?name=a.png"); w.Code != http.StatusForbidden {
		t.Fatalf("unsigned: %d %s", w.Code, w.Body)
	}
	signed, _ := SignURL(key, "/upload?name=a.png&width=20", 0)
	if w := upload(signed); w.Code != http.StatusOK {
		t.Fatalf("signed: %d %s", w.Code, w.Body)
	}
	tampered := strings.Replace(signed, "width=20", "width=30", 1)
	if w := upload(tampered); w.Code != http.StatusForbidden {
		t.Fatalf("tampered: %d %s", w.Code, w.Body)
	}
	expired, _ := SignURL(key, "/upload?name=a.png&exp="+strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10), 0)
	if w := upload(expired); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "expired") {
		t.Fatalf("expired: %d %s", w.Code, w.Body)
	}
	fresh, _ := SignURL(key, "/upload?name=a.png", time.Hour)
	if w := upload(fresh); w.Code != http.StatusOK {
		t.Fatalf("with exp: %d %s", w.Code, w.Body)
	}
	if w := serveReq(h, httptest.NewRequest("POST", "/upload/info?name=a.png", bytes.NewReader(img))); w.Code != http.StatusForbidden {
		t.Fatalf("unsigned info: %d %s", w.Code, w.Body)
	}

	// Form fields would add parameters the signature does not cover.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("width", "999")
	fw, _ := mw.CreateFormFile("file", "a.png")
	_, _ = fw.Write(img)
	_ = mw.Close()
	signedForm, _ := SignURL(key, "/upload", 0)
	r := httptest.NewRequest("POST", signedForm, &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	if w := serveReq(h, r); w.Code != http.StatusBadRequest {
		t.Fatalf("signed form field: %d %s", w.Code, w.Body)
	}
}

// chunked hides the length, as a streaming client would.
type chunked struct{ io.Reader }

func TestUploadTooLarge(t *testing.T) {
	s := liveServer(t, Config{maxBytes: 1 << 10})
	h := s.Handler()
	big := bytes.Repeat([]byte("x"), 4<<20)

	r := httptest.NewRequest("POST", "/upload?name=a.txt", bytes.NewReader(big))
	if w := serveReq(h, r); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("declared length: %d %s", w.Code, w.Body)
	}
	r = httptest.NewRequest("POST", "/upload?name=a.txt", chunked{bytes.NewReader(big)})
	r.ContentLength = -1
	if w := serveReq(h, r); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked: %d %s", w.Code, w.Body)
	}

	// Many small form fields add up too.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for i := 0; body.Len() < 3<<20; i++ {
		_ = mw.WriteField("f"+strconv.Itoa(i), strings.Repeat("v", 4000))
	}
	_ = mw.Close()
	r = httptest.NewRequest("POST", "/upload", chunked{&body})
	r.ContentLength = -1
	r.Header.Set("Content-Type", mw.FormDataContentType())
	if w := serveReq(h, r); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("form fields: %d %s", w.Code, w.Body)
	}
}

func TestUploadMemoryBudget(t *testing.T) {
	s := liveServer(t, Config{Timeout: 200 * time.Millisecond})
	h := s.Handler() // registers metrics: once per server
	img := pngBytes(t, color.Black)
	if err := s.uploads.Acquire(context.Background(), s.cfg.uploadMemory); err != nil {
		t.Fatal(err)
	}
	w := serveReq(h, httptest.NewRequest("POST", "/upload?name=a.png", bytes.NewReader(img)))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("full budget: %d %s", w.Code, w.Body)
	}
	s.uploads.Release(s.cfg.uploadMemory)

	if w := serveReq(h, httptest.NewRequest("POST", "/upload?name=a.png", bytes.NewReader(img))); w.Code != http.StatusOK {
		t.Fatalf("after release: %d %s", w.Code, w.Body)
	}
	// Every request gave its bytes back.
	if !s.uploads.TryAcquire(s.cfg.uploadMemory) {
		t.Fatal("upload budget leaked")
	}
}

func TestSignURLCoversExp(t *testing.T) {
	u, err := SignURL([]byte("k"), "/render?url=https://a/b.pdf", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	q, _ := url.ParseQuery(strings.SplitN(u, "?", 2)[1])
	if q.Get("exp") == "" || q.Get("sign") == "" {
		t.Fatalf("missing exp or sign: %s", u)
	}
}
