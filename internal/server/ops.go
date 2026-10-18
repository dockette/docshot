package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"strings"

	"github.com/dockette/docshot/internal/apierr"
	"github.com/dockette/docshot/internal/metrics"
)

func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	var b strings.Builder
	metrics.Write(&b)
	s.render.WriteGauges(&b)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	metrics.Gauge(&b, "go_goroutines", "Goroutines.", float64(runtime.NumGoroutine()))
	metrics.Gauge(&b, "go_memstats_heap_alloc_bytes", "Go heap in use.", float64(ms.HeapAlloc))
	metrics.Gauge(&b, "go_memstats_sys_bytes", "Memory obtained from the OS by the Go runtime (incl. PDFium WASM).", float64(ms.Sys))
	fmt.Fprintf(&b, "# HELP docshot_build_info Build version.\n# TYPE docshot_build_info gauge\ndocshot_build_info%s 1\n", metrics.Label("version", s.cfg.Version))
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = w.Write([]byte(b.String()))
}

// sign = base64url(HMAC-SHA256(key, path + "?" + sorted query without sign)),
// so strangers cannot make the box render arbitrary files or sizes.

func signature(key []byte, path string, q url.Values) string {
	c := url.Values{}
	for k, v := range q {
		if k != "sign" {
			c[k] = v
		}
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(path + "?" + c.Encode())) // Encode sorts by key: canonical
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (s *Server) checkSignature(r *http.Request) error {
	if len(s.cfg.signKey) == 0 {
		return nil
	}
	got := r.URL.Query().Get("sign")
	want := signature(s.cfg.signKey, r.URL.Path, r.URL.Query())
	if got == "" || !hmac.Equal([]byte(got), []byte(want)) {
		return apierr.New(http.StatusForbidden, "missing or invalid signature")
	}
	return nil
}

// SignURL appends &sign=… to a path+query.
func SignURL(key []byte, raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Del("sign")
	q.Set("sign", signature(key, u.Path, q))
	u.RawQuery = q.Encode()
	return u.String(), nil
}
