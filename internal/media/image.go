// Package media decodes images, animations, video, audio and other media via Go and ffmpeg.
package media

import (
	"bytes"
	"context"
	"image"
	"image/draw"
	"image/gif"
	"net/http"

	"github.com/dockette/docshot/internal/apierr"
	"github.com/dockette/docshot/internal/imaging"
	"github.com/gen2brain/webp"
)

// ImageSequence returns one item for a still, every frame for an animated GIF/WebP; formats Go cannot decode go through ffmpeg.
func ImageSequence(ctx context.Context, body []byte, ext string, maxSide int) (*imaging.Sequence, error) {
	// Check the header first: decompression bomb (30000×30000 PNG = 3.6 GB decoded).
	cfg, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		img, err := ffmpegImage(ctx, body, ext)
		if err != nil {
			return nil, err
		}
		return imaging.Still(img, maxSide), nil
	}
	if int64(cfg.Width)*int64(cfg.Height) > 3*imaging.MaxPixels {
		return nil, apierr.New(http.StatusUnprocessableEntity, "image is %dx%d, over the %d pixel source limit", cfg.Width, cfg.Height, 3*imaging.MaxPixels)
	}

	var frames []image.Image
	switch format {
	case "gif":
		g, err := gif.DecodeAll(bytes.NewReader(body))
		if err != nil {
			return nil, apierr.New(http.StatusUnprocessableEntity, "decoding gif: %v", err)
		}
		if int64(len(g.Image))*int64(cfg.Width)*int64(cfg.Height) > 3*imaging.MaxPixels {
			return nil, apierr.New(http.StatusUnprocessableEntity, "gif has %d frames of %dx%d, over the pixel limit", len(g.Image), cfg.Width, cfg.Height)
		}
		frames = gifFrames(g)
	case "webp":
		if a, err := webp.DecodeAll(bytes.NewReader(body)); err == nil && len(a.Image) > 1 {
			if int64(len(a.Image))*int64(cfg.Width)*int64(cfg.Height) > 3*imaging.MaxPixels {
				return nil, apierr.New(http.StatusUnprocessableEntity, "webp has %d frames, over the pixel limit", len(a.Image))
			}
			frames = a.Image
		}
	}
	if frames == nil {
		img, _, err := image.Decode(bytes.NewReader(body))
		if err != nil {
			if img, err = ffmpegImage(ctx, body, ext); err != nil {
				return nil, err
			}
		}
		frames = []image.Image{img}
	}
	return imaging.Frames(frames, maxSide), nil
}

// gifFrames composites frames over the previous canvas, honouring disposal.
func gifFrames(g *gif.GIF) []image.Image {
	w, h := g.Config.Width, g.Config.Height
	if w == 0 || h == 0 {
		b := g.Image[0].Bounds()
		w, h = b.Dx(), b.Dy()
	}
	canvas := image.NewNRGBA(image.Rect(0, 0, w, h))
	out := make([]image.Image, 0, len(g.Image))
	for i, fr := range g.Image {
		var restore *image.NRGBA
		disposal := byte(0)
		if i < len(g.Disposal) {
			disposal = g.Disposal[i]
		}
		if disposal == gif.DisposalPrevious {
			restore = image.NewNRGBA(canvas.Bounds())
			copy(restore.Pix, canvas.Pix)
		}
		draw.Draw(canvas, fr.Bounds(), fr, fr.Bounds().Min, draw.Over)
		snap := image.NewNRGBA(canvas.Bounds())
		copy(snap.Pix, canvas.Pix)
		out = append(out, snap)
		switch disposal {
		case gif.DisposalBackground:
			draw.Draw(canvas, fr.Bounds(), image.Transparent, image.Point{}, draw.Src)
		case gif.DisposalPrevious:
			canvas = restore
		}
	}
	return out
}
