// Package render turns document bytes into images: detect, convert, rasterise, cache.
package render

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/dockette/docshot/internal/apierr"
	"github.com/dockette/docshot/internal/detect"
	"github.com/dockette/docshot/internal/imaging"
	"github.com/dockette/docshot/internal/lru"
	"github.com/dockette/docshot/internal/metrics"
	"golang.org/x/sync/singleflight"
)

type Result struct {
	Img         []byte // image, or ZIP for pages
	ContentType string
	Meta        Meta
	Kind        detect.Kind
}

type docEntry struct {
	pdf  []byte // nil for raster inputs
	kind detect.Kind
	path string
}

// caches converts hot documents once; later pages/sizes are cheap PDFium renders or memory hits.
type caches struct {
	docs   *lru.Cache[docEntry]
	images *lru.Cache[Result]
	flight singleflight.Group
}

// Renderer turns document bytes into pictures.
type Renderer struct {
	cfg    Config
	http   *http.Client
	raster *imaging.Rasterizer
	cache  *caches
	limit  *limiter
}

type Config struct {
	GotenbergURL string
	MaxBytes     int64
	MaxSide      int // longest output edge, px
	Timeout      time.Duration
	CacheMB      int
	Concurrency  int
	Queue        int
}

func (r *Renderer) Close() {
	if r.raster != nil {
		r.raster.Close()
	}
}

func New(cfg Config, raster *imaging.Rasterizer) *Renderer {
	client := &http.Client{Timeout: cfg.Timeout}
	return &Renderer{
		cfg:    cfg,
		http:   client,
		raster: raster,
		cache:  newCaches(cfg.CacheMB),
		limit:  newLimiter(cfg.Concurrency, cfg.Queue),
	}
}

func newCaches(mb int) *caches {
	n := int64(mb) << 20
	return &caches{
		docs:   lru.New(n, func(d docEntry) int64 { return int64(len(d.pdf)) + 64 }),
		images: lru.New(n, func(r Result) int64 { return int64(len(r.Img)) + 64 }),
	}
}

func Hash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// HashBytes is Hash(string(b)) without copying a document-sized b into a string.
func HashBytes(b []byte) string {
	h := sha256.New()
	h.Write(b)
	h.Write([]byte{0})
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// Render goes memory cache, then singleflight document cache, then render.
// Shared work uses a detached context so one client hanging up cannot fail the others.
func (r *Renderer) Render(content, name string, body []byte, o Options) (Result, string, error) {
	ext := strings.ToLower(filepath.Ext(name)) // the route depends on the name
	imgKey := Hash(content, ext, o.Key())
	if res, ok := r.cache.images.Get(imgKey); ok {
		return res, "memory", nil
	}
	state, leader := "miss", false
	v, err, _ := r.cache.flight.Do("img:"+imgKey, safely(func() (any, error) {
		leader = true
		work, cancel := context.WithTimeout(context.Background(), r.cfg.Timeout)
		defer cancel()

		doc, cached, err := r.document(work, content, name, body)
		if err != nil {
			return nil, err
		}
		if cached {
			state = "document"
		}
		release, err := r.limit.acquire(work)
		if err != nil {
			return nil, err
		}
		defer release()

		t0 := time.Now()
		defer func() {
			metrics.Renders.WithLabelValues(o.Op, TypeLabel(doc.kind)).Inc()
			metrics.RenderSeconds.WithLabelValues(o.Op).Observe(time.Since(t0).Seconds())
		}()
		res, err := r.produce(context.WithValue(work, slotHeld{}, true), doc, body, o)
		if err != nil {
			return nil, err
		}
		r.cache.images.Put(imgKey, res)
		return res, nil
	}))
	if !leader {
		state = "shared"
	}
	if err != nil {
		return Result{}, state, err
	}
	return v.(Result), state, nil
}

// document returns the PDF (or just the kind for raster inputs), converting once per content hash.
func (r *Renderer) document(ctx context.Context, content, name string, body []byte) (docEntry, bool, error) {
	docKey := Hash(content, strings.ToLower(filepath.Ext(name)))
	if doc, ok := r.cache.docs.Get(docKey); ok {
		return doc, true, nil
	}
	d, err, _ := r.cache.flight.Do("doc:"+docKey, safely(func() (any, error) {
		k := detect.Detect(name, body)
		switch k.Route {
		case detect.Image, detect.Video, detect.Archive, detect.Audio, detect.Raw:
			return docEntry{kind: k, path: string(k.Route)}, nil
		}
		// members are converted while the parent holds a slot; a second could deadlock a small pool
		if held, _ := ctx.Value(slotHeld{}).(bool); !held {
			release, err := r.limit.acquire(ctx)
			if err != nil {
				return nil, err
			}
			defer release()
		}
		t0 := time.Now()
		pdf, err := r.toPDF(ctx, k, body)
		path := string(k.Route)
		if err != nil && k.Route == detect.TIFF {
			// tiff2pdf rejects some TIFFs: fall back to one page via the image route
			return docEntry{kind: detect.Kind{Ext: k.Ext, Route: detect.Image, Name: k.Name}, path: string(detect.Image)}, nil
		}
		if err != nil {
			metrics.ConversionErrors.WithLabelValues(path, TypeLabel(k)).Inc()
			return nil, err
		}
		took := time.Since(t0)
		if k.Route != detect.PDF {
			metrics.Conversions.WithLabelValues(path, TypeLabel(k)).Inc()
			metrics.ConversionSeconds.WithLabelValues(path).Observe(took.Seconds())
		}
		slog.Info("converted", "name", name, "kind", k.Ext, "path", path, "pdf_bytes", len(pdf), "ms", took.Milliseconds())
		entry := docEntry{pdf: pdf, kind: k, path: path}
		r.cache.docs.Put(docKey, entry)
		return entry, nil
	}))
	if err != nil {
		return docEntry{}, false, err
	}
	return d.(docEntry), false, nil
}

type slotHeld struct{}

// safely turns a panic in a parser or the rasteriser into a 500 for this document,
// instead of a dropped connection (or a crash, off the request goroutine).
func safely(fn func() (any, error)) func() (any, error) {
	return func() (v any, err error) {
		defer func() {
			if p := recover(); p != nil {
				slog.Error("render panicked", "panic", p, "stack", string(debug.Stack()))
				v, err = nil, apierr.New(http.StatusInternalServerError, "internal error while rendering")
			}
		}()
		return fn()
	}
}

// TypeLabel is the input type as a metric label: unrecognised extensions are "other", so uploads cannot grow the series set.
func TypeLabel(k detect.Kind) string {
	if k.Route == detect.Unknown || k.Ext == "" {
		return "other"
	}
	return k.Ext
}
