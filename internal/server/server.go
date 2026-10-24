// Package server implements the docshot HTTP API.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dockette/docshot/internal/apierr"
	"github.com/dockette/docshot/internal/detect"
	"github.com/dockette/docshot/internal/diskcache"
	"github.com/dockette/docshot/internal/imaging"
	"github.com/dockette/docshot/internal/metrics"
	"github.com/dockette/docshot/internal/render"
	"github.com/minio/minio-go/v7"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sync/semaphore"
	"golang.org/x/sync/singleflight"
)

// Config holds the settings loaded from the environment.
type Config struct {
	Listen       string
	GotenbergURL string
	maxBytes     int64
	defaultDPI   int
	defaultW     int // DOCSHOT_DEFAULT_WIDTH: used when a request sets neither width nor height; 0 = by DPI
	defaultH     int // DOCSHOT_DEFAULT_HEIGHT
	maxSide      int // longest edge of the output image, in pixels
	Timeout      time.Duration
	workers      int

	fetchAllow    *regexp.Regexp // DOCSHOT_FETCH_ALLOW: ?url= must match (anchored at the start)
	fetchLoopback bool           // tests only: let ?url= reach loopback httptest servers
	cacheMB       int
	cacheControl  string // DOCSHOT_CACHE_CONTROL, or built from DOCSHOT_MAX_AGE
	cdnMaxAge     int    // DOCSHOT_CDN_MAX_AGE: CDN-Cache-Control (RFC 9213); 0 = not sent

	cacheDir    string        // DOCSHOT_CACHE_DIR: rendered results on disk; "" = off
	cacheSize   int64         // DOCSHOT_CACHE_SIZE: LRU limit of the disk cache
	cacheTTL    time.Duration // DOCSHOT_CACHE_TTL: re-check the source after this; 0 = never
	cacheStale  bool          // DOCSHOT_CACHE_STALE: past TTL, answer stale and re-check in the background
	cacheBuster string        // DOCSHOT_CACHE_BUSTER: part of every cache key and ETag
	cacheTags   bool          // DOCSHOT_CACHE_TAGS: Surrogate-Key / Cache-Tag per source
	s3Endpoint  string
	s3AccessKey string
	s3SecretKey string
	s3Region    string
	s3SSL       bool
	s3Buckets   map[string]bool

	storeBucket   string
	storePrefix   string
	storeStrategy string

	presets      map[string]url.Values // DOCSHOT_PRESETS on top of the built-ins
	onlyPresets  bool                  // DOCSHOT_ONLY_PRESETS: reject ad-hoc processing params
	signKey      []byte                // DOCSHOT_SIGN_KEY: GET /render and /info need ?sign=
	fallback     bool                  // DOCSHOT_FALLBACK: placeholder image instead of an error
	concurrency  int                   // DOCSHOT_CONCURRENCY: renders/conversions at once
	queue        int                   // DOCSHOT_QUEUE: waiting requests before 429
	maxPixels    int64                 // DOCSHOT_MAX_PIXELS: area cap of any bitmap; 0 = default
	uploadMemory int64                 // DOCSHOT_UPLOAD_MEMORY: bytes of uploads buffered at once
	logFormat    string                // DOCSHOT_LOG_FORMAT: text or json
	Version      string
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envs parses settings; a value that does not parse is an error at startup, never a silent default.
type envs struct{ errs []error }

func (e *envs) fail(key, v, want string) {
	e.errs = append(e.errs, fmt.Errorf("%s: %q is not %s", key, v, want))
}

// int reads a whole number of at least lo.
func (e *envs) int(key string, def, lo int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo {
		e.fail(key, v, fmt.Sprintf("a whole number >= %d", lo))
		return def
	}
	return n
}

func (e *envs) bool(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		e.fail(key, v, "true or false")
		return def
	}
	return b
}

func (e *envs) size(key, def string) int64 {
	n, err := parseSize(env(key, def))
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: %w", key, err))
	}
	return n
}

func (e *envs) oneOf(key, def string, allowed ...string) string {
	v := env(key, def)
	if !slices.Contains(allowed, v) {
		e.fail(key, v, "one of "+strings.Join(allowed, ", "))
		return def
	}
	return v
}

func LoadConfig() (Config, error) {
	var e envs
	cfg := Config{
		Listen:        env("DOCSHOT_LISTEN", ":3000"), // Gotenberg's port: drop-in for its clients
		GotenbergURL:  strings.TrimRight(env("GOTENBERG_URL", "http://gotenberg:3000"), "/"),
		maxBytes:      e.size("DOCSHOT_MAX_BYTES", "100MB"),
		defaultDPI:    e.int("DOCSHOT_DEFAULT_DPI", 150, 1),
		defaultW:      e.int("DOCSHOT_DEFAULT_WIDTH", 0, 0),
		defaultH:      e.int("DOCSHOT_DEFAULT_HEIGHT", 0, 0),
		maxSide:       e.int("DOCSHOT_MAX_SIDE", 10000, 1),
		Timeout:       time.Duration(e.int("DOCSHOT_TIMEOUT_SECONDS", 180, 1)) * time.Second,
		workers:       e.int("DOCSHOT_PDFIUM_WORKERS", 4, 1),
		cacheMB:       e.int("DOCSHOT_CACHE_MB", 256, 0),
		cdnMaxAge:     e.int("DOCSHOT_CDN_MAX_AGE", 0, 0),
		cacheStale:    e.bool("DOCSHOT_CACHE_STALE", true),
		cacheBuster:   os.Getenv("DOCSHOT_CACHE_BUSTER"),
		cacheTags:     e.bool("DOCSHOT_CACHE_TAGS", false),
		s3Endpoint:    os.Getenv("DOCSHOT_S3_ENDPOINT"),
		s3AccessKey:   os.Getenv("DOCSHOT_S3_ACCESS_KEY"),
		s3SecretKey:   os.Getenv("DOCSHOT_S3_SECRET_KEY"),
		s3Region:      os.Getenv("DOCSHOT_S3_REGION"),
		s3SSL:         e.bool("DOCSHOT_S3_SSL", true),
		s3Buckets:     bucketSet(splitList(os.Getenv("DOCSHOT_S3_BUCKETS"))),
		storeBucket:   os.Getenv("DOCSHOT_STORE_BUCKET"),
		storePrefix:   env("DOCSHOT_STORE_PREFIX", "docshot/"),
		storeStrategy: e.oneOf("DOCSHOT_STORE_STRATEGY", "append", "append", "replace", "suffix", "hash"),
		onlyPresets:   e.bool("DOCSHOT_ONLY_PRESETS", false),
		signKey:       []byte(os.Getenv("DOCSHOT_SIGN_KEY")),
		fallback:      e.bool("DOCSHOT_FALLBACK", false),
		concurrency:   e.int("DOCSHOT_CONCURRENCY", runtime.NumCPU(), 1),
		queue:         e.int("DOCSHOT_QUEUE", 64, 0),
		maxPixels:     int64(e.int("DOCSHOT_MAX_PIXELS", 0, 0)),
		logFormat:     e.oneOf("DOCSHOT_LOG_FORMAT", "text", "text", "json"),
	}
	cfg.cacheControl = env("DOCSHOT_CACHE_CONTROL", "public, max-age="+strconv.Itoa(e.int("DOCSHOT_MAX_AGE", 3600, 0))+
		", stale-while-revalidate=86400, stale-if-error=604800")
	cfg.uploadMemory = e.size("DOCSHOT_UPLOAD_MEMORY", "1GB")
	cfg.cacheDir = "/tmp/docshot"
	if v, ok := os.LookupEnv("DOCSHOT_CACHE_DIR"); ok {
		cfg.cacheDir = v // set but empty: off
	}
	cfg.cacheSize = e.size("DOCSHOT_CACHE_SIZE", "10GB")
	var err error
	if cfg.cacheTTL, err = parseTTL(env("DOCSHOT_CACHE_TTL", "0")); err != nil {
		e.errs = append(e.errs, fmt.Errorf("DOCSHOT_CACHE_TTL: %w", err))
	}
	if cfg.fetchAllow, err = compileAllow(os.Getenv("DOCSHOT_FETCH_ALLOW")); err != nil {
		e.errs = append(e.errs, fmt.Errorf("DOCSHOT_FETCH_ALLOW: %w", err))
	}
	if cfg.presets, err = parsePresets(os.Getenv("DOCSHOT_PRESETS")); err != nil {
		e.errs = append(e.errs, fmt.Errorf("DOCSHOT_PRESETS: %w", err))
	}
	return cfg, errors.Join(e.errs...)
}

// LogHandler is the slog handler DOCSHOT_LOG_FORMAT asks for.
func (cfg Config) LogHandler(w io.Writer) slog.Handler {
	if cfg.logFormat == "json" {
		return slog.NewJSONHandler(w, nil)
	}
	return slog.NewTextHandler(w, nil)
}

func New(cfg Config) (*Server, error) {
	if cfg.maxPixels > 0 {
		imaging.MaxPixels = cfg.maxPixels
	}
	r, err := imaging.NewRasterizer(cfg.workers)
	if err != nil {
		return nil, fmt.Errorf("pdfium: %w", err)
	}
	s3c, err := newS3(cfg)
	if err != nil {
		r.Close()
		return nil, fmt.Errorf("s3: %w", err)
	}
	return newServer(cfg, r, s3c), nil
}

func newServer(cfg Config, raster *imaging.Rasterizer, s3c *minio.Client) *Server {
	cfg.uploadMemory = max(cfg.uploadMemory, cfg.maxBytes) // uploads are buffered whole: the largest must fit
	rc := render.Config{GotenbergURL: cfg.GotenbergURL, MaxBytes: cfg.maxBytes, MaxSide: cfg.maxSide,
		Timeout: cfg.Timeout, CacheMB: cfg.cacheMB, Concurrency: cfg.concurrency, Queue: cfg.queue}
	s := &Server{cfg: cfg, render: render.New(rc, raster), s3: s3c, fetch: newFetchClient(cfg),
		uploads: semaphore.NewWeighted(cfg.uploadMemory)}
	if cfg.cacheDir != "" && cfg.cacheSize > 0 {
		// A cache that cannot be written is not worth failing the service for.
		var err error
		if s.cache, err = diskcache.Open(cfg.cacheDir, cfg.cacheSize); err != nil {
			slog.Warn("disk cache disabled", "dir", cfg.cacheDir, "err", err)
			s.cache = nil
		}
	}
	s.reg = metrics.NewRegistry(s.render.Collector(), cacheCollector{s}, buildInfo(cfg.Version))
	return s
}

// Close waits for background revalidations, then stops the rasteriser.
func (s *Server) Close() {
	s.background.Wait()
	s.render.Close()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// GET names the document in the URL (works in <img>); POST /upload carries it.
	for _, op := range []string{render.OpPage, render.OpGrid, render.OpPages} {
		suffix := ""
		if op != render.OpPage {
			suffix = "/" + op
		}
		mux.HandleFunc("GET /render"+suffix, s.handleGet(op))
		mux.HandleFunc("POST /upload"+suffix, s.handleUpload(op))
	}
	mux.HandleFunc("GET /info", s.handleInfo)
	mux.HandleFunc("POST /upload/info", s.handleUploadInfo)
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.reg, promhttp.HandlerOpts{Registry: s.reg}))
	mux.HandleFunc("DELETE /cache", s.handlePurge)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("GET /readyz", s.handleReady)
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"name": "docshot", "version": s.cfg.Version})
	})
	mux.HandleFunc("GET /formats", s.handleFormats)

	// Same JSON errors for unknown paths and wrong methods; the patterns above win.
	allow := map[string]string{}
	for _, p := range []string{"/upload", "/upload/grid", "/upload/pages", "/upload/info"} {
		allow[p] = "POST"
	}
	for _, p := range []string{"/render", "/render/grid", "/render/pages", "/info", "/healthz", "/readyz", "/metrics", "/version", "/formats"} {
		allow[p] = "GET, HEAD"
	}
	allow["/cache"] = "DELETE"
	for p, methods := range allow {
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Allow", methods)
			apierr.Write(w, apierr.New(http.StatusMethodNotAllowed, "%s %s: use %s", r.Method, r.URL.Path, methods))
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		apierr.Write(w, apierr.New(http.StatusNotFound, "no endpoint %s — POST /upload[/grid|/pages|/info], GET /render[/grid|/pages], /info, /formats, /readyz", r.URL.Path))
	})
	return instrument(s.reg, mux)
}

type Server struct {
	cfg    Config
	render *render.Renderer
	s3     *minio.Client // nil unless DOCSHOT_S3_ENDPOINT is set
	fetch  *http.Client  // ?url= sources
	// uploads bounds the bytes of request bodies held in memory at once (DOCSHOT_UPLOAD_MEMORY).
	uploads *semaphore.Weighted
	cache   *diskcache.Cache // nil unless DOCSHOT_CACHE_DIR is usable
	reg     *prometheus.Registry

	revalidating singleflight.Group // background re-checks of stale entries, one per key
	background   sync.WaitGroup
}

func bucketSet(names []string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

func splitList(v string) []string {
	var out []string
	for _, x := range strings.Split(v, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// filenameHint: ?name= first, then Content-Disposition, else none (content sniffing).
func filenameHint(r *http.Request) string {
	if n := r.URL.Query().Get("name"); n != "" {
		return filepath.Base(n)
	}
	if cd := r.Header.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil && params["filename"] != "" {
			return filepath.Base(params["filename"])
		}
		// Tolerate the unquoted form curl users type by hand.
		if i := strings.Index(strings.ToLower(cd), "filename="); i >= 0 {
			return filepath.Base(strings.Trim(cd[i+len("filename="):], `"' `))
		}
	}
	return ""
}

// readUpload reads multipart (first file part; other fields act as query
// parameters) or a raw body named by Content-Disposition / ?name=. The body
// counts against DOCSHOT_UPLOAD_MEMORY until release is called.
func (s *Server) readUpload(w http.ResponseWriter, r *http.Request) (in input, release func(), err error) {
	// Multipart framing and form fields ride on top of the document.
	limit := s.cfg.maxBytes + 1<<20
	if r.ContentLength > limit {
		return input{}, nil, apierr.New(http.StatusRequestEntityTooLarge, "file larger than %d bytes", s.cfg.maxBytes)
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	reserved := s.cfg.maxBytes
	if r.ContentLength > 0 {
		reserved = min(r.ContentLength, s.cfg.maxBytes)
	}
	wait, cancel := context.WithTimeout(r.Context(), s.cfg.Timeout)
	defer cancel()
	if err := s.uploads.Acquire(wait, reserved); err != nil {
		metrics.Rejected.Inc()
		return input{}, nil, apierr.New(http.StatusTooManyRequests, "busy: %d bytes of uploads in memory (DOCSHOT_UPLOAD_MEMORY)", s.cfg.uploadMemory)
	}
	held := reserved
	free := func() { s.uploads.Release(held) }
	defer func() {
		if err != nil {
			free()
		}
	}()
	tooLarge := func(err error) error {
		if mbe := (*http.MaxBytesError)(nil); errors.As(err, &mbe) {
			return apierr.New(http.StatusRequestEntityTooLarge, "file larger than %d bytes", s.cfg.maxBytes)
		}
		return nil
	}

	var body []byte
	var name string
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt == "multipart/form-data" {
		mr, err := r.MultipartReader()
		if err != nil {
			return input{}, nil, apierr.New(http.StatusBadRequest, "multipart: %v", err)
		}
		q := r.URL.Query()
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				if e := tooLarge(err); e != nil {
					return input{}, nil, e
				}
				return input{}, nil, apierr.New(http.StatusBadRequest, "multipart: %v", err)
			}
			if part.FileName() == "" {
				if len(s.cfg.signKey) > 0 {
					// The signature covers the URL only; a form field would change the request unsigned.
					return input{}, nil, apierr.New(http.StatusBadRequest, "form field %q: with DOCSHOT_SIGN_KEY, put parameters in the signed URL", part.FormName())
				}
				v, _ := io.ReadAll(io.LimitReader(part, 4096))
				if !q.Has(part.FormName()) { // the URL wins over the form
					q.Set(part.FormName(), string(v))
				}
				continue
			}
			if body != nil {
				continue // one document per request; ignore extra files
			}
			name = filepath.Base(part.FileName())
			if body, err = io.ReadAll(io.LimitReader(part, s.cfg.maxBytes+1)); err != nil {
				if e := tooLarge(err); e != nil {
					return input{}, nil, e
				}
				return input{}, nil, apierr.New(http.StatusBadRequest, "reading upload: %v", err)
			}
		}
		r.URL.RawQuery = q.Encode()
		if body == nil {
			return input{}, nil, apierr.New(http.StatusBadRequest, "multipart upload has no file part (curl -F file=@doc.pdf)")
		}
	} else {
		if body, err = io.ReadAll(io.LimitReader(r.Body, s.cfg.maxBytes+1)); err != nil {
			if e := tooLarge(err); e != nil {
				return input{}, nil, e
			}
			return input{}, nil, apierr.New(http.StatusBadRequest, "reading body: %v", err)
		}
		name = filenameHint(r)
	}
	if n := r.URL.Query().Get("name"); n != "" {
		name = filepath.Base(n)
	}
	if int64(len(body)) > s.cfg.maxBytes {
		return input{}, nil, apierr.New(http.StatusRequestEntityTooLarge, "file larger than %d bytes", s.cfg.maxBytes)
	}
	if len(body) == 0 {
		return input{}, nil, apierr.New(http.StatusBadRequest, "empty upload: curl -F file=@doc.pdf …/upload")
	}
	// Keep only what the body really holds.
	if n := int64(len(body)); n < held {
		s.uploads.Release(held - n)
		held = n
	}
	return input{name: name, source: "upload", load: func(context.Context) ([]byte, error) { return body, nil }}, free, nil
}

func (s *Server) handleUpload(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Before reading the body: the signature covers the URL as sent.
		if err := s.checkSignature(r); err != nil {
			apierr.Write(w, err)
			return
		}
		in, release, err := s.readUpload(w, r) // then: multipart fields may carry parameters
		if err != nil {
			apierr.Write(w, err)
			return
		}
		defer release()
		opts, err := s.parseOpts(r, op)
		if err != nil {
			apierr.Write(w, err)
			return
		}
		in.storeBase = in.name
		s.serve(w, r, in, opts)
	}
}

func (s *Server) handleGet(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		opts, err := s.parseOpts(r, op)
		if err != nil {
			apierr.Write(w, err)
			return
		}
		q := r.URL.Query()
		if err := s.checkSignature(r); err != nil {
			s.fail(w, err, "", sourceKind(q), opts)
			return
		}
		// The disk cache is keyed by the request, so a hit costs no S3 or file access at all.
		key, src := s.requestKey(q, opts)
		var pre *input
		if s.cache != nil && src != "" {
			var done bool
			if done, pre = s.fromCache(w, r, key, opts); done {
				return
			}
		}
		var in input
		if pre != nil {
			in = *pre
		} else if in, err = s.queryInput(r); err != nil {
			s.fail(w, err, q.Get("s3")+q.Get("url"), sourceKind(q), opts)
			return
		}
		in.cacheKey, in.sourceID, in.cacheControl = key, src, s.cacheControlFor(q)
		s.serve(w, r, in, opts)
	}
}

func (s *Server) queryInput(r *http.Request) (input, error) {
	q := r.URL.Query()
	var in input
	var err error
	switch {
	case q.Get("s3") != "":
		in, err = s.s3Input(r.Context(), q.Get("s3"))
	case q.Get("url") != "":
		var src source
		src, err = s.fromURL(r.Context(), q.Get("url"))
		in = input{name: src.name, cacheControl: s.cfg.cacheControl, storeBase: urlStoreBase(q.Get("url")),
			load: func(context.Context) ([]byte, error) { return src.body, nil }}
	default:
		err = apierr.New(http.StatusBadRequest, "GET needs ?s3= or ?url= (to upload a file, POST it to /upload)")
	}
	if err != nil {
		return input{}, err
	}
	in.source = sourceKind(q)
	if n := q.Get("name"); n != "" {
		in.name = filepath.Base(n)
	}
	return in, nil
}

// urlStoreBase turns a URL into a store key (host + path).
func urlStoreBase(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.Trim(u.Host+u.Path, "/")
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	if err := checkParams(r.URL.Query(), "info"); err != nil {
		apierr.Write(w, err)
		return
	}
	if err := s.checkSignature(r); err != nil {
		apierr.Write(w, err)
		return
	}
	in, err := s.queryInput(r)
	if err != nil {
		apierr.Write(w, err)
		return
	}
	s.writeInfo(w, r, in)
}

func (s *Server) handleUploadInfo(w http.ResponseWriter, r *http.Request) {
	if err := s.checkSignature(r); err != nil {
		apierr.Write(w, err)
		return
	}
	in, release, err := s.readUpload(w, r)
	if err != nil {
		apierr.Write(w, err)
		return
	}
	defer release()
	s.writeInfo(w, r, in)
}

func (s *Server) writeInfo(w http.ResponseWriter, r *http.Request, in input) {
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.Timeout)
	defer cancel()
	body, err := in.load(ctx)
	if err != nil {
		apierr.Write(w, err)
		return
	}
	info, err := s.render.Info(ctx, in.name, body)
	if err != nil {
		apierr.Write(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(info)
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	st, ok := s.render.Ready(r.Context())
	st["version"] = s.cfg.Version
	status := http.StatusOK
	st["status"] = "ok"
	if !ok {
		status, st["status"] = http.StatusServiceUnavailable, "unavailable"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(st)
}

func (s *Server) handleFormats(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(detect.Formats())
}
