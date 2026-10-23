package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/dockette/docshot/internal/apierr"
	"github.com/dockette/docshot/internal/diskcache"
	"github.com/dockette/docshot/internal/metrics"
	"github.com/dockette/docshot/internal/render"
	"github.com/prometheus/client_golang/prometheus"
)

// The disk cache answers GET requests by their URL (like imgproxy's internal cache):
// the key is the source, the canonical options, ?v= and DOCSHOT_CACHE_BUSTER — not ?sign=,
// which is checked before the lookup. A hit costs no S3 or file access. Past DOCSHOT_CACHE_TTL
// the source revision (S3 ETag, size+mtime, content hash) is re-checked, and only a changed
// source is downloaded and rendered again. Uploads are cached by content hash.

// immutable is the Cache-Control of versioned URLs (?v=): the bytes never change.
const immutable = "public, max-age=31536000, immutable"

// headersToReplay are the render headers a cached entry answers with again.
var headersToReplay = []string{"X-Source-Type", "X-Render-Path", "X-Page", "X-Page-Count", "X-Effective-DPI", "X-Duration"}

// sourceID is the canonical source of a GET request and the purge handle ("" = none given).
func sourceID(q url.Values) string {
	switch {
	case q.Get("path") != "":
		return "path:" + path.Clean("/"+q.Get("path"))
	case q.Get("s3") != "":
		return "s3:" + strings.TrimPrefix(q.Get("s3"), "s3://")
	case q.Get("url") != "":
		return "url:" + q.Get("url")
	}
	return ""
}

// sourceKind is the source as a metric label.
func sourceKind(q url.Values) string {
	for _, k := range []string{"path", "s3", "url"} {
		if q.Get(k) != "" {
			return k
		}
	}
	return "none"
}

func (s *Server) requestKey(q url.Values, o options) (key, source string) {
	source = sourceID(q)
	return render.Hash("get", source, q.Get("name"), o.Key(), q.Get("v"), s.cfg.cacheBuster), source
}

func (s *Server) uploadKey(content, name string, o options) string {
	return render.Hash("upload", content, name, o.Key(), s.cfg.cacheBuster)
}

func (s *Server) etag(parts ...string) string {
	return `"` + render.Hash(append(parts, s.cfg.cacheBuster)...) + `"`
}

func (s *Server) cacheControlFor(q url.Values) string {
	if q.Get("v") != "" {
		return immutable
	}
	return s.cfg.cacheControl
}

func (s *Server) expired(e diskcache.Entry) bool {
	return s.cfg.cacheTTL > 0 && time.Since(e.Validated) > s.cfg.cacheTTL
}

// cacheHeaders sets what every successful answer carries: freshness for browsers and CDNs, Vary, purge tags.
func (s *Server) cacheHeaders(h http.Header, cacheControl, source string, o options) {
	if cacheControl != "" {
		h.Set("Cache-Control", cacheControl)
		if s.cfg.cdnMaxAge > 0 && cacheControl != immutable {
			h.Set("CDN-Cache-Control", "public, max-age="+strconv.Itoa(s.cfg.cdnMaxAge))
		}
	}
	if o.autoFormat {
		h.Set("Vary", "Accept")
	}
	if s.cfg.cacheTags && source != "" {
		tag := sourceTag(source)
		h.Set("Surrogate-Key", "docshot "+tag)
		h.Set("Cache-Tag", "docshot,"+tag)
	}
}

func sourceTag(source string) string { return "docshot-" + render.Hash(source)[:16] }

// cacheStatus formats an RFC 9211 Cache-Status member for this cache.
func cacheStatus(params ...string) string {
	return strings.Join(append([]string{"docshot"}, params...), "; ")
}

func (s *Server) ttlParam(e diskcache.Entry) []string {
	if s.cfg.cacheTTL <= 0 {
		return nil
	}
	return []string{"ttl=" + strconv.Itoa(int(math.Floor(time.Until(e.Validated.Add(s.cfg.cacheTTL)).Seconds())))}
}

// sourceAllowed re-applies the source configuration (no I/O), so a hit cannot outlive
// a disabled source, a bucket taken off DOCSHOT_S3_BUCKETS or a narrowed DOCSHOT_FETCH_ALLOW.
func (s *Server) sourceAllowed(q url.Values) bool {
	switch sourceKind(q) {
	case "path":
		return s.cfg.root != ""
	case "s3":
		bucket, _, _ := strings.Cut(strings.TrimPrefix(q.Get("s3"), "s3://"), "/")
		return s.s3 != nil && s.bucketAllowed(bucket)
	case "url":
		return s.cfg.fetchAllow != nil && s.cfg.fetchAllow.MatchString(q.Get("url"))
	}
	return false
}

// fromCache answers a GET from the disk cache. It returns done=false to go on rendering,
// with the source already resolved when re-checking an expired entry needed it.
func (s *Server) fromCache(w http.ResponseWriter, r *http.Request, key string, o options) (bool, *input) {
	if !s.sourceAllowed(r.URL.Query()) {
		return false, nil // the source lookup reports why
	}
	e, ok := s.cache.Get(key)
	if !ok {
		metrics.CacheRequests.WithLabelValues("miss").Inc()
		return false, nil
	}
	status := append([]string{"hit"}, s.ttlParam(e)...)
	state := "disk"
	if s.expired(e) {
		if !s.cfg.cacheStale {
			// Re-check now: serve() reuses the entry if the revision is unchanged.
			in, err := s.queryInput(r)
			switch {
			case err == nil:
				return false, &in
			case statusOf(err) == http.StatusNotFound:
				s.cache.Delete(key)
				return false, nil
			}
			slog.Warn("cache: source unavailable, serving stale", "source", e.Source, "err", err)
			metrics.CacheRequests.WithLabelValues("revalidate_error").Inc()
			status, state = []string{"fwd=stale", "fwd-status=" + strconv.Itoa(statusOf(err)), "detail=stale-if-error"}, "stale"
		} else {
			state = "stale"
			r2 := r.Clone(context.Background()) // the client's request ends before the re-check does
			s.background.Add(1)
			go func() {
				defer s.background.Done()
				s.revalidate(r2, key, o)
			}()
		}
	}
	result := "hit"
	if state == "stale" {
		result = "stale"
	}
	metrics.CacheRequests.WithLabelValues(result).Inc()
	return s.writeEntry(w, r, key, e, o, sourceKind(r.URL.Query()), state, s.cacheControlFor(r.URL.Query()), cacheStatus(status...), time.Now()), nil
}

// revalidate re-checks a stale entry in the background (stale-while-revalidate); errors keep the stale entry (stale-if-error).
func (s *Server) revalidate(r *http.Request, key string, o options) {
	_, _, _ = s.revalidating.Do(key, func() (any, error) {
		ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Timeout)
		defer cancel()
		r2 := r.WithContext(ctx)
		r2.Header.Del("If-None-Match")
		r2.Header.Del("If-Modified-Since")
		in, err := s.queryInput(r2)
		if err != nil {
			if statusOf(err) == http.StatusNotFound {
				s.cache.Delete(key)
			}
			metrics.CacheRequests.WithLabelValues("revalidate_error").Inc()
			slog.Warn("cache: revalidation failed", "key", key, "err", err)
			return nil, nil
		}
		q := r2.URL.Query()
		in.cacheKey, in.sourceID, in.cacheControl, in.background = key, sourceID(q), s.cacheControlFor(q), true
		s.serve(discard{}, r2, in, o)
		return nil, nil
	})
}

// reuse answers from an entry made from exactly this source revision, restamping it if it had expired.
func (s *Server) reuse(w http.ResponseWriter, r *http.Request, in input, o options, rev string, start time.Time) bool {
	if s.cache == nil || in.cacheKey == "" {
		return false
	}
	e, ok := s.cache.Get(in.cacheKey)
	if !ok || e.Revision != rev {
		return false
	}
	state, status := "disk", append([]string{"hit"}, s.ttlParam(e)...)
	if s.expired(e) {
		now := time.Now()
		if err := s.cache.Revalidated(in.cacheKey, now); err != nil {
			return false
		}
		e.Validated = now
		state, status = "revalidated", []string{"fwd=stale", "fwd-status=304"}
		metrics.CacheRequests.WithLabelValues("revalidated").Inc()
	}
	if in.background {
		return true
	}
	if state == "disk" {
		metrics.CacheRequests.WithLabelValues("hit").Inc()
	}
	return s.writeEntry(w, r, in.cacheKey, e, o, in.source, state, in.cacheControl, cacheStatus(status...), start)
}

// writeEntry answers with a cached entry: 304 if the client has it, else its body. false = the file vanished: render.
func (s *Server) writeEntry(w http.ResponseWriter, r *http.Request, key string, e diskcache.Entry, o options,
	source, state, cacheControl, status string, start time.Time) bool {
	h := w.Header()
	s.cacheHeaders(h, cacheControl, e.Source, o)
	h.Set("ETag", e.ETag)
	h.Set("Last-Modified", e.Created.UTC().Format(http.TimeFormat))
	h.Set("Cache-Status", status)
	if fresh(r, e.ETag, e.Created) {
		metrics.Requests.WithLabelValues(o.Op, source, "not_modified").Inc()
		metrics.CacheRequests.WithLabelValues("not_modified").Inc()
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	_, body, err := s.cache.Open(key)
	if err != nil {
		h.Del("Cache-Status")
		return false
	}
	defer body.Close()
	metrics.Requests.WithLabelValues(o.Op, source, state).Inc()
	if e.Type != "" {
		metrics.Documents.WithLabelValues(e.Type, e.Header["X-Render-Path"], o.Op).Inc()
	}
	metrics.OutputBytes.WithLabelValues(o.Op, outputExt(o.Options)).Observe(float64(e.Size))
	s.attachment(h, e.Name, o)
	for k, v := range e.Header {
		h.Set(k, v)
	}
	h.Set("Content-Type", e.ContentType)
	h.Set("Content-Length", strconv.FormatInt(e.Size, 10))
	h.Set("X-Cache", state)
	h.Set("X-Render-Ms", strconv.FormatInt(time.Since(start).Milliseconds(), 10))
	_, _ = io.Copy(w, body)
	return true
}

// fresh evaluates If-None-Match, else If-Modified-Since (RFC 9110 §13.2.2).
func fresh(r *http.Request, etag string, modified time.Time) bool {
	if r.Header.Get("If-None-Match") != "" {
		return match(r, etag)
	}
	if t, err := http.ParseTime(r.Header.Get("If-Modified-Since")); err == nil {
		return !modified.Truncate(time.Second).After(t)
	}
	return false
}

// putCache stores a fresh result; the outcome lands in Cache-Status.
func (s *Server) putCache(in input, e diskcache.Entry, data []byte) bool {
	if s.cache == nil || in.cacheKey == "" {
		return false
	}
	e.Key, e.Source, e.Name, e.Validated = in.cacheKey, in.sourceID, in.name, e.Created
	switch err := s.cache.Put(e, data); {
	case err == nil:
		metrics.CacheWrites.WithLabelValues("ok").Inc()
		return true
	case errors.Is(err, diskcache.ErrTooLarge):
		metrics.CacheWrites.WithLabelValues("skipped").Inc()
	default:
		metrics.CacheWrites.WithLabelValues("error").Inc()
		slog.Warn("cache: write failed", "key", in.cacheKey, "err", err)
	}
	return false
}

// handlePurge: DELETE /cache?s3=bucket/key (or path=, url=) drops every variant of one source; ?all=true drops everything.
func (s *Server) handlePurge(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for k := range q {
		switch k {
		case "path", "s3", "url", "all", "sign":
		default:
			apierr.Write(w, &apierr.Error{Status: http.StatusBadRequest, Code: "unknown_parameter", Msg: fmt.Sprintf("unknown parameter %q: DELETE /cache takes path=, s3=, url= or all=true", k)})
			return
		}
	}
	if err := s.checkSignature(r); err != nil {
		apierr.Write(w, err)
		return
	}
	if s.cache == nil {
		apierr.Write(w, apierr.New(http.StatusNotFound, "disk cache disabled: set DOCSHOT_CACHE_DIR and DOCSHOT_CACHE_SIZE"))
		return
	}
	resp := map[string]any{}
	switch src := sourceID(q); {
	case q.Get("all") == "true":
		resp["purged"], resp["tag"] = s.cache.Purge(""), "docshot"
	case src != "":
		resp["purged"], resp["source"], resp["tag"] = s.cache.Purge(src), src, sourceTag(src)
	default:
		apierr.Write(w, apierr.New(http.StatusBadRequest, "DELETE /cache needs ?path=, ?s3=, ?url= or ?all=true"))
		return
	}
	metrics.CachePurged.Add(float64(resp["purged"].(int)))
	slog.Info("cache: purged", "source", resp["source"], "entries", resp["purged"])
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func statusOf(err error) int {
	var he *apierr.Error
	if errors.As(err, &he) {
		return he.Status
	}
	return http.StatusInternalServerError
}

// discard is the ResponseWriter of background revalidation.
type discard struct{}

func (discard) Header() http.Header         { return http.Header{} }
func (discard) Write(b []byte) (int, error) { return len(b), nil }
func (discard) WriteHeader(int)             {}

// cacheCollector reports the disk cache at scrape time.
type cacheCollector struct{ s *Server }

var (
	cacheBytesDesc   = prometheus.NewDesc("docshot_cache_bytes", "Bytes held by the disk cache, by input type.", []string{"type"}, nil)
	cacheEntriesDesc = prometheus.NewDesc("docshot_cache_entries", "Entries in the disk cache, by input type.", []string{"type"}, nil)
	cacheMaxDesc     = prometheus.NewDesc("docshot_cache_max_bytes", "Disk cache limit (DOCSHOT_CACHE_SIZE); 0 = disabled.", nil, nil)
	cacheOldestDesc  = prometheus.NewDesc("docshot_cache_oldest_access_seconds", "Seconds since the least recently used entry was used: how long the cache remembers.", nil, nil)
	cacheEvictDesc   = prometheus.NewDesc("docshot_cache_evictions_total", "Entries evicted to stay under the limit.", nil, nil)
	cacheEvBytesDesc = prometheus.NewDesc("docshot_cache_evicted_bytes_total", "Bytes evicted to stay under the limit.", nil, nil)
)

func (c cacheCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{cacheBytesDesc, cacheEntriesDesc, cacheMaxDesc, cacheOldestDesc, cacheEvictDesc, cacheEvBytesDesc} {
		ch <- d
	}
}

func (c cacheCollector) Collect(ch chan<- prometheus.Metric) {
	if c.s.cache == nil {
		ch <- prometheus.MustNewConstMetric(cacheMaxDesc, prometheus.GaugeValue, 0)
		return
	}
	st := c.s.cache.Stats()
	ch <- prometheus.MustNewConstMetric(cacheMaxDesc, prometheus.GaugeValue, float64(st.Max))
	ch <- prometheus.MustNewConstMetric(cacheEvictDesc, prometheus.CounterValue, float64(st.Evictions))
	ch <- prometheus.MustNewConstMetric(cacheEvBytesDesc, prometheus.CounterValue, float64(st.EvictedBytes))
	if !st.Oldest.IsZero() {
		ch <- prometheus.MustNewConstMetric(cacheOldestDesc, prometheus.GaugeValue, time.Since(st.Oldest).Seconds())
	}
	for t, ts := range st.Types {
		if t == "" {
			t = "unknown"
		}
		ch <- prometheus.MustNewConstMetric(cacheBytesDesc, prometheus.GaugeValue, float64(ts.Bytes), t)
		ch <- prometheus.MustNewConstMetric(cacheEntriesDesc, prometheus.GaugeValue, float64(ts.Entries), t)
	}
}

// parseSize reads "10GB", "512m", "1GiB" or plain bytes; units are binary, like Docker's.
func parseSize(v string) (int64, error) {
	s := strings.ToUpper(strings.TrimSpace(v))
	num := strings.TrimRight(s, "KMGTIB")
	mul := map[string]int64{"": 1, "B": 1, "K": 1 << 10, "M": 1 << 20, "G": 1 << 30, "T": 1 << 40}[strings.TrimSuffix(strings.TrimSuffix(s[len(num):], "B"), "I")]
	n, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
	if err != nil || n < 0 || mul == 0 {
		return 0, fmt.Errorf("%q is not a size (e.g. 10GB, 512MB)", v)
	}
	return int64(n * float64(mul)), nil
}

// parseTTL is time.ParseDuration plus days ("7d"); a bare number is seconds.
func parseTTL(v string) (time.Duration, error) {
	v = strings.TrimSpace(v)
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	if d, ok := strings.CutSuffix(v, "d"); ok {
		n, err := strconv.ParseFloat(d, 64)
		if err != nil {
			return 0, fmt.Errorf("%q is not a duration (e.g. 7d, 12h, 0)", v)
		}
		return time.Duration(n * float64(24*time.Hour)), nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%q is not a duration (e.g. 7d, 12h, 0)", v)
	}
	return d, nil
}
