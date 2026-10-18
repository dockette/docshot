package render

import (
	"bytes"
	"context"
	"image"
	"net/http"
	"time"

	"github.com/dockette/docshot/internal/detect"
	"github.com/dockette/docshot/internal/media"
)

// info describes the document (type, route, pages, sizes) without rendering.
func (r *Renderer) Info(ctx context.Context, name string, body []byte) (map[string]any, error) {
	doc, cached, err := r.document(ctx, Hash(string(body)), name, body)
	if err != nil {
		return nil, err
	}
	info := map[string]any{
		"name": name, "bytes": len(body), "type": doc.kind.Ext, "route": doc.path, "cached": cached,
	}
	switch doc.kind.Route {
	case detect.Image:
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(body)); err == nil {
			info["width_px"], info["height_px"] = cfg.Width, cfg.Height
		}
		q, err := media.ImageSequence(ctx, body, doc.kind.Ext, r.cfg.MaxSide)
		if err != nil {
			return nil, err
		}
		info["pages"] = q.N
	case detect.Archive:
		q, err := r.archiveSequence(ctx, body, name, doc.kind.Ext)
		if err != nil {
			return nil, err
		}
		files := make([]string, q.N)
		for i := range files {
			files[i] = q.Label(i)
		}
		info["pages"], info["files"] = q.N, files
	case detect.Audio:
		var a media.AudioInfo
		err := media.WithTempFile(body, doc.kind.Ext, func(p string) (err error) { a, err = media.ProbeAudio(ctx, p); return err })
		if err != nil {
			return nil, err
		}
		info["pages"], info["duration_s"], info["codec"] = 1, a.Duration, a.Codec
		info["title"], info["artist"], info["album"] = a.Title, a.Artist, a.Album
		info["sample_rate"], info["channels"], info["has_cover"] = a.SampleRate, a.Channels, a.HasCover
	case detect.Raw:
		info["pages"] = 1
	case detect.Video:
		v, err := media.ProbeVideo(ctx, body)
		if err != nil {
			return nil, err
		}
		info["duration_s"], info["width_px"], info["height_px"] = v.Duration, v.Width, v.Height
		info["codec"], info["fps"] = v.Codec, v.Fps
	default:
		sizes, err := r.raster.PageSizes(doc.pdf)
		if err != nil {
			return nil, err
		}
		info["pages"] = len(sizes)
		info["page_sizes_pt"] = sizes // PDF points
	}
	return info, nil
}

// ready needs Gotenberg and PDFium.
func (r *Renderer) Ready(ctx context.Context) (map[string]string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	probe := func(url string) string {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := r.http.Do(req)
		if err != nil {
			return "down: " + err.Error()
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "down: " + resp.Status
		}
		return "ok"
	}
	st := map[string]string{"gotenberg": probe(r.cfg.GotenbergURL + "/health")}
	st["pdfium"] = "ok"
	if err := r.raster.Ping(3 * time.Second); err != nil {
		st["pdfium"] = "down: " + err.Error()
	}
	return st, st["gotenberg"] == "ok" && st["pdfium"] == "ok"
}
