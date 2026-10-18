package server

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dockette/docshot/internal/apierr"
	"github.com/dockette/docshot/internal/imaging"
	"github.com/dockette/docshot/internal/metrics"
	"github.com/dockette/docshot/internal/render"
)

// input is one request's document, wherever it came from.
type input struct {
	name string
	// load is lazy so a stored hit never downloads the source.
	load func(ctx context.Context) ([]byte, error)
	// version identifies the source revision without reading it (size+mtime, S3 ETag); empty means hash the content.
	version      string
	s3           *s3Ref // set for ?s3= sources: results can be stored next to them
	storeBase    string // key stem for ?store=s3 of non-S3 sources (path, file name, URL path)
	cacheControl string
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request, in input, o options) {
	start := time.Now()
	h := w.Header()
	if in.cacheControl != "" {
		h.Set("Cache-Control", in.cacheControl)
	}
	if o.autoFormat {
		h.Set("Vary", "Accept")
	}
	fail := func(err error) { s.fail(w, err, in.name, o) }
	notModified := func(etag string) bool {
		h.Set("ETag", etag)
		if !match(r, etag) {
			return false
		}
		metrics.Inc("docshot_requests_total", metrics.Label("cache", "not_modified"))
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	fromStore := func(t storeTarget, data []byte) {
		metrics.Inc("docshot_requests_total", metrics.Label("cache", "store"))
		s.attachment(h, in.name, o)
		h.Set("Content-Type", contentTypeOf(o.Options))
		h.Set("X-Cache", "store")
		h.Set("X-Stored-As", t.String())
		h.Set("X-Render-Ms", strconv.FormatInt(time.Since(start).Milliseconds(), 10))
		_, _ = w.Write(data)
	}

	// Fast 304 when the source revision is known up front: no read, no render.
	if in.version != "" && notModified(`"`+render.Hash(in.version, o.Key())+`"`) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.Timeout)
	defer cancel()

	// Name-based strategy on an S3 source: target and freshness come from its ETag.
	var target storeTarget
	resolved := false
	if o.store {
		var err error
		if target, resolved, err = s.storeTarget(in, o, ""); err != nil {
			fail(err)
			return
		}
		if resolved {
			if data, ok := s.fetchStored(ctx, target); ok {
				fromStore(target, data)
				return
			}
		}
	}

	body, err := in.load(ctx)
	if err != nil {
		fail(err)
		return
	}
	if len(body) == 0 {
		fail(apierr.New(http.StatusBadRequest, "empty document"))
		return
	}
	content := render.Hash(string(body))
	if in.version == "" && notModified(`"`+render.Hash(content, o.Key())+`"`) {
		return
	}
	if o.store && !resolved {
		if target, resolved, err = s.storeTarget(in, o, content); err != nil {
			fail(err)
			return
		}
		if data, ok := s.fetchStored(ctx, target); ok {
			fromStore(target, data)
			return
		}
	}

	res, cacheState, err := s.render.Render(content, in.name, body, o.Options)
	if err != nil {
		slog.Warn("render failed", "name", in.name, "op", o.Op, "err", err)
		fail(err)
		return
	}
	metrics.Inc("docshot_requests_total", metrics.Label("cache", cacheState))

	if o.store && resolved {
		if err := s.putStored(ctx, target, res.Img, res.ContentType); err != nil {
			slog.Warn("store failed", "target", target.String(), "err", err)
			h.Set("X-Store-Error", err.Error())
		} else {
			h.Set("X-Stored-As", target.String())
		}
	}

	s.attachment(h, in.name, o)
	h.Set("Content-Type", res.ContentType)
	h.Set("X-Cache", cacheState)
	h.Set("X-Source-Type", res.Kind.Ext)
	h.Set("X-Render-Path", res.Meta.Path)
	h.Set("X-Page", strconv.Itoa(o.Page))
	h.Set("X-Page-Count", strconv.Itoa(res.Meta.PageCount))
	if res.Meta.Dpi > 0 {
		h.Set("X-Effective-DPI", strconv.Itoa(res.Meta.Dpi))
	}
	if res.Meta.Duration > 0 {
		h.Set("X-Duration", strconv.FormatFloat(res.Meta.Duration, 'f', 2, 64))
	}
	h.Set("X-Render-Ms", strconv.FormatInt(time.Since(start).Milliseconds(), 10))
	_, _ = w.Write(res.Img)
	slog.Info("rendered", "name", in.name, "op", o.Op, "kind", res.Kind.Ext, "path", res.Meta.Path, "page", o.Page,
		"pages", res.Meta.PageCount, "bytes", len(res.Img), "cache", cacheState, "ms", time.Since(start).Milliseconds())
}

func contentTypeOf(o render.Options) string {
	if o.Op == render.OpPages {
		return "application/zip"
	}
	return "image/" + o.Format
}

func outputExt(o render.Options) string {
	switch {
	case o.Op == render.OpPages:
		return "zip"
	case o.Format == "jpeg":
		return "jpg"
	}
	return o.Format
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

func match(r *http.Request, etag string) bool {
	for _, t := range strings.Split(r.Header.Get("If-None-Match"), ",") {
		if strings.TrimSpace(t) == etag || strings.TrimSpace(t) == "*" {
			return true
		}
	}
	return false
}

// fail answers JSON, or with fallback a placeholder picture (status 200, error in a header).
func (s *Server) fail(w http.ResponseWriter, err error, name string, o options) {
	status := http.StatusInternalServerError
	var he *apierr.Error
	if errors.As(err, &he) {
		status = he.Status
	}
	metrics.Inc("docshot_errors_total", metrics.Label("status", strconv.Itoa(status)))
	// Errors must not keep the ETag/Cache-Control set earlier, or caches pin the failure.
	h := w.Header()
	h.Del("ETag")
	h.Del("Vary")
	h.Set("Cache-Control", "no-store")
	if !o.fallback || status == http.StatusTooManyRequests {
		apierr.Write(w, err)
		return
	}
	pix := o.Options.Options
	img, encErr := imaging.Encode(imaging.Placeholder(name, pix), pix)
	if encErr != nil {
		apierr.Write(w, err)
		return
	}
	h.Set("Cache-Control", "public, max-age=60")
	h.Set("Content-Type", "image/"+o.Format)
	h.Set("X-Docshot-Error", fmt.Sprintf("%d %s", status, err.Error()))
	_, _ = w.Write(img)
}

func (s *Server) attachment(h http.Header, name string, o options) {
	if !o.download {
		return
	}
	fn := o.downloadName
	if fn == "" {
		op := ""
		if o.Op != render.OpPage {
			op = "." + o.Op
		}
		fn = cmp.Or(name, "document") + op + variant(o.Options, s.cfg.defaultDPI) + "." + outputExt(o.Options)
	}
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": fn}))
}

// variant encodes non-default options as a stable name part (".p2", ".p1-4x2") for store keys and download names.
func variant(o render.Options, defaultDPI int) string {
	var parts []string
	if o.DPI != defaultDPI && o.Width == 0 && o.Height == 0 {
		parts = append(parts, fmt.Sprintf("d%d", o.DPI))
	}
	if o.Width > 0 || o.Height > 0 {
		box := fmt.Sprintf("%dx%d", o.Width, o.Height)
		if o.Height == 0 {
			box = fmt.Sprintf("w%d", o.Width)
		} else if o.Width == 0 {
			box = fmt.Sprintf("h%d", o.Height)
		}
		parts = append(parts, box)
		if o.Width > 0 && o.Height > 0 && o.Fit != "contain" {
			parts = append(parts, o.Fit)
			if (o.Fit == "cover" || o.Fit == "pad") && o.Position != "center" {
				parts = append(parts, o.Position)
			}
		}
	}
	if o.Op == render.OpGrid {
		if o.GridCols > 0 {
			parts = append(parts, fmt.Sprintf("%dx%d", o.GridCols, o.GridRows))
		}
		if !o.Labels {
			parts = append(parts, "nolabels")
		}
	}
	if o.Op == render.OpPages && o.Limit != defaultLimit {
		parts = append(parts, fmt.Sprintf("l%d", o.Limit))
	}
	if o.AtSet {
		parts = append(parts, fmt.Sprintf("at%gs", o.At))
	}
	if o.Trim > 0 {
		parts = append(parts, fmt.Sprintf("t%d", o.Trim))
	}
	if o.Pad > 0 {
		parts = append(parts, fmt.Sprintf("pad%d", o.Pad))
	}
	if o.BGSet {
		parts = append(parts, fmt.Sprintf("bg%02x%02x%02x%02x", o.BG.R, o.BG.G, o.BG.B, o.BG.A))
	}
	switch {
	case (o.Format == "jpeg" || o.Format == "webp") && o.Quality != 85:
		parts = append(parts, fmt.Sprintf("q%d", o.Quality))
	}
	if o.Page == 1 && len(parts) == 0 {
		return ""
	}
	return "." + strings.Join(append([]string{fmt.Sprintf("p%d", o.Page)}, parts...), "-")
}
