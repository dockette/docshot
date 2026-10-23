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
		if err != nil && ctx.Err() == nil {
			if fixed := salvageICO(body); fixed != nil {
				if fimg, ferr := ffmpegImage(ctx, fixed, "ico"); ferr == nil {
					img, err = fimg, nil
				}
			}
		}
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
		// a broken GIF keeps its complete frames, else falls through to a still
		g, err := gif.DecodeAll(bytes.NewReader(body))
		if err != nil {
			if fixed := salvageGIF(body); fixed != nil {
				g, err = gif.DecodeAll(bytes.NewReader(fixed))
			}
		}
		if err == nil {
			if int64(len(g.Image))*int64(cfg.Width)*int64(cfg.Height) > 3*imaging.MaxPixels {
				return nil, apierr.New(http.StatusUnprocessableEntity, "gif has %d frames of %dx%d, over the pixel limit", len(g.Image), cfg.Width, cfg.Height)
			}
			frames = gifFrames(g)
		}
	case "webp":
		// a cut animation can decode as one frame or none without an error
		a, err := decodeWebPAll(body)
		if err != nil || len(a.Image) < 2 {
			if fixed := salvageWebP(body, cfg); fixed != nil {
				if f, ferr := decodeWebPAll(fixed); ferr == nil && (err != nil || len(f.Image) > len(a.Image)) {
					a, err = f, nil
				}
			}
		}
		if err == nil && len(a.Image) > 1 {
			if int64(len(a.Image))*int64(cfg.Width)*int64(cfg.Height) > 3*imaging.MaxPixels {
				return nil, apierr.New(http.StatusUnprocessableEntity, "webp has %d frames, over the pixel limit", len(a.Image))
			}
			frames = a.Image
		}
	}
	if frames == nil {
		img, err := decodeImage(body)
		if err != nil {
			if img, err = decodeBroken(ctx, body, cfg, format, ext, err); err != nil {
				return nil, err
			}
		}
		frames = []image.Image{img}
	}
	return imaging.Frames(frames, maxSide), nil
}

// safely runs a decoder, turning its panic on hostile input into an error
// (gen2brain/webp indexes an empty frame list on a cut animation).
func safely[T any](decode func() (T, error)) (v T, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = apierr.New(http.StatusUnprocessableEntity, "decoder panic: %v", r)
		}
	}()
	return decode()
}

func decodeImage(b []byte) (image.Image, error) {
	return safely(func() (image.Image, error) {
		img, _, err := image.Decode(bytes.NewReader(b))
		return img, err
	})
}

func decodeWebPAll(b []byte) (*webp.WEBP, error) {
	return safely(func() (*webp.WEBP, error) { return webp.DecodeAll(bytes.NewReader(b)) })
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
