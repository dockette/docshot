package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"runtime"
	"strings"

	"github.com/dockette/docshot/internal/apierr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// instrument counts and times every HTTP request by route pattern (bounded, unlike raw paths).
func instrument(reg *prometheus.Registry, mux *http.ServeMux) http.Handler {
	inflight := prometheus.NewGauge(prometheus.GaugeOpts{Name: "docshot_http_requests_in_flight", Help: "HTTP requests being served."})
	total := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "docshot_http_requests_total", Help: "HTTP requests by route, method and status."},
		[]string{"handler", "method", "code"})
	duration := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "docshot_http_request_duration_seconds", Help: "HTTP request latency by route.",
		Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 180}}, []string{"handler", "method", "code"})
	size := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "docshot_http_response_size_bytes", Help: "HTTP response body size by route.",
		Buckets: prometheus.ExponentialBuckets(256, 4, 12)}, []string{"handler"})
	reg.MustRegister(inflight, total, duration, size)

	return promhttp.InstrumentHandlerInFlight(inflight, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		if pattern == "" || pattern == "/" {
			pattern = "other"
		}
		l := prometheus.Labels{"handler": strings.TrimPrefix(strings.TrimPrefix(pattern, r.Method+" "), "GET ")}
		h := promhttp.InstrumentHandlerCounter(total.MustCurryWith(l),
			promhttp.InstrumentHandlerDuration(duration.MustCurryWith(l),
				promhttp.InstrumentHandlerResponseSize(size.MustCurryWith(l), mux)))
		h.ServeHTTP(w, r)
	}))
}

func buildInfo(version string) prometheus.Collector {
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: "docshot_build_info", Help: "Build version.",
		ConstLabels: prometheus.Labels{"version": version, "goversion": runtime.Version()}})
	g.Set(1)
	return g
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
