// Package server implements the docshot HTTP API.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/dockette/docshot/internal/apierr"
	"github.com/dockette/docshot/internal/detect"
	"github.com/dockette/docshot/internal/imaging"
	"github.com/dockette/docshot/internal/render"
	"github.com/minio/minio-go/v7"
)

// Config holds the settings loaded from the environment.
type Config struct {
	Listen       string
	GotenbergURL string
	maxBytes     int64
	defaultDPI   int
	maxSide      int // longest edge of the output image, in pixels
	Timeout      time.Duration
	workers      int

	root         string         // DOCSHOT_ROOT: ?path= is confined to this directory
	fetchAllow   *regexp.Regexp // DOCSHOT_FETCH_ALLOW: ?url= must match
	cacheMB      int
	cacheControl string
	s3Endpoint   string
	s3AccessKey  string
	s3SecretKey  string
	s3Region     string
	s3SSL        bool
	s3Buckets    map[string]bool

	storeBucket   string
	storePrefix   string
	storeStrategy string

	presets     map[string]url.Values // DOCSHOT_PRESETS on top of the built-ins
	onlyPresets bool                  // DOCSHOT_ONLY_PRESETS: reject ad-hoc processing params
	signKey     []byte                // DOCSHOT_SIGN_KEY: GET /render and /info need ?sign=
	fallback    bool                  // DOCSHOT_FALLBACK: placeholder image instead of an error
	concurrency int                   // DOCSHOT_CONCURRENCY: renders/conversions at once
	queue       int                   // DOCSHOT_QUEUE: waiting requests before 429
	maxPixels   int64                 // DOCSHOT_MAX_PIXELS: area cap of any bitmap; 0 = default
	Version     string
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}

func LoadConfig() (Config, error) {
	cfg := Config{
		Listen:        env("DOCSHOT_LISTEN", ":3000"), // Gotenberg's port: drop-in for its clients
		GotenbergURL:  strings.TrimRight(env("GOTENBERG_URL", "http://gotenberg:3000"), "/"),
		maxBytes:      int64(envInt("DOCSHOT_MAX_BYTES", 100<<20)),
		defaultDPI:    envInt("DOCSHOT_DEFAULT_DPI", 150),
		maxSide:       envInt("DOCSHOT_MAX_SIDE", 10000),
		Timeout:       time.Duration(envInt("DOCSHOT_TIMEOUT_SECONDS", 180)) * time.Second,
		workers:       envInt("DOCSHOT_PDFIUM_WORKERS", 4),
		root:          os.Getenv("DOCSHOT_ROOT"),
		fetchAllow:    compileAllow(os.Getenv("DOCSHOT_FETCH_ALLOW")),
		cacheMB:       envInt("DOCSHOT_CACHE_MB", 256),
		cacheControl:  "public, max-age=" + strconv.Itoa(envInt("DOCSHOT_MAX_AGE", 3600)),
		s3Endpoint:    os.Getenv("DOCSHOT_S3_ENDPOINT"),
		s3AccessKey:   os.Getenv("DOCSHOT_S3_ACCESS_KEY"),
		s3SecretKey:   os.Getenv("DOCSHOT_S3_SECRET_KEY"),
		s3Region:      os.Getenv("DOCSHOT_S3_REGION"),
		s3SSL:         os.Getenv("DOCSHOT_S3_SSL") != "false",
		s3Buckets:     bucketSet(splitList(os.Getenv("DOCSHOT_S3_BUCKETS"))),
		storeBucket:   os.Getenv("DOCSHOT_STORE_BUCKET"),
		storePrefix:   env("DOCSHOT_STORE_PREFIX", "docshot/"),
		storeStrategy: env("DOCSHOT_STORE_STRATEGY", "append"),
		onlyPresets:   os.Getenv("DOCSHOT_ONLY_PRESETS") == "true",
		signKey:       []byte(os.Getenv("DOCSHOT_SIGN_KEY")),
		fallback:      os.Getenv("DOCSHOT_FALLBACK") == "true",
		concurrency:   envInt("DOCSHOT_CONCURRENCY", runtime.NumCPU()),
		queue:         envInt("DOCSHOT_QUEUE", 64),
		maxPixels:     int64(envInt("DOCSHOT_MAX_PIXELS", 0)),
	}
	presets, err := parsePresets(os.Getenv("DOCSHOT_PRESETS"))
	if err != nil {
		return cfg, fmt.Errorf("DOCSHOT_PRESETS: %w", err)
	}
	cfg.presets = presets
	return cfg, nil
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
	rc := render.Config{GotenbergURL: cfg.GotenbergURL, MaxBytes: cfg.maxBytes, MaxSide: cfg.maxSide,
		Timeout: cfg.Timeout, CacheMB: cfg.cacheMB, Concurrency: cfg.concurrency, Queue: cfg.queue}
	return &Server{cfg: cfg, render: render.New(rc, r), s3: s3c}, nil
}

func (s *Server) Close() { s.render.Close() }

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
	mux.HandleFunc("GET /metrics", s.handleMetrics)
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
	for p, methods := range allow {
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Allow", methods)
			apierr.Write(w, apierr.New(http.StatusMethodNotAllowed, "%s %s: use %s", r.Method, r.URL.Path, methods))
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		apierr.Write(w, apierr.New(http.StatusNotFound, "no endpoint %s — POST /upload[/grid|/pages|/info], GET /render[/grid|/pages], /info, /formats, /readyz", r.URL.Path))
	})
	return mux
}

type Server struct {
	cfg    Config
	render *render.Renderer
	s3     *minio.Client // nil unless DOCSHOT_S3_ENDPOINT is set
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
// parameters) or a raw body named by Content-Disposition / ?name=.
func (s *Server) readUpload(r *http.Request) (input, error) {
	var body []byte
	var name string
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt == "multipart/form-data" {
		mr, err := r.MultipartReader()
		if err != nil {
			return input{}, apierr.New(http.StatusBadRequest, "multipart: %v", err)
		}
		q := r.URL.Query()
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return input{}, apierr.New(http.StatusBadRequest, "multipart: %v", err)
			}
			if part.FileName() == "" {
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
				return input{}, apierr.New(http.StatusBadRequest, "reading upload: %v", err)
			}
		}
		r.URL.RawQuery = q.Encode()
		if body == nil {
			return input{}, apierr.New(http.StatusBadRequest, "multipart upload has no file part (curl -F file=@doc.pdf)")
		}
	} else {
		var err error
		if body, err = io.ReadAll(io.LimitReader(r.Body, s.cfg.maxBytes+1)); err != nil {
			return input{}, apierr.New(http.StatusBadRequest, "reading body: %v", err)
		}
		name = filenameHint(r)
	}
	if n := r.URL.Query().Get("name"); n != "" {
		name = filepath.Base(n)
	}
	if int64(len(body)) > s.cfg.maxBytes {
		return input{}, apierr.New(http.StatusRequestEntityTooLarge, "file larger than %d bytes", s.cfg.maxBytes)
	}
	if len(body) == 0 {
		return input{}, apierr.New(http.StatusBadRequest, "empty upload: curl -F file=@doc.pdf …/upload")
	}
	return input{name: name, load: func(context.Context) ([]byte, error) { return body, nil }}, nil
}

func (s *Server) handleUpload(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		in, err := s.readUpload(r) // first: multipart fields may carry parameters
		if err != nil {
			apierr.Write(w, err)
			return
		}
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
		if err := s.checkSignature(r); err != nil {
			s.fail(w, err, "", opts)
			return
		}
		in, err := s.queryInput(r)
		if err != nil {
			s.fail(w, err, r.URL.Query().Get("path")+r.URL.Query().Get("s3")+r.URL.Query().Get("url"), opts)
			return
		}
		s.serve(w, r, in, opts)
	}
}

func (s *Server) queryInput(r *http.Request) (input, error) {
	q := r.URL.Query()
	var in input
	var err error
	switch {
	case q.Get("path") != "":
		in, err = s.pathInput(q.Get("path"))
	case q.Get("s3") != "":
		in, err = s.s3Input(r.Context(), q.Get("s3"))
	case q.Get("url") != "":
		var src source
		src, err = s.fromURL(r.Context(), q.Get("url"))
		in = input{name: src.name, cacheControl: s.cfg.cacheControl, storeBase: urlStoreBase(q.Get("url")),
			load: func(context.Context) ([]byte, error) { return src.body, nil }}
	default:
		err = apierr.New(http.StatusBadRequest, "GET needs ?path=, ?s3= or ?url= (to upload a file, POST it to /upload)")
	}
	if err != nil {
		return input{}, err
	}
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
	in, err := s.readUpload(r)
	if err != nil {
		apierr.Write(w, err)
		return
	}
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
