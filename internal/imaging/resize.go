package imaging

import (
	"image"
	"image/color"
	"math"

	"golang.org/x/image/draw"
)

// MaxPixels caps any bitmap's area (DOCSHOT_MAX_PIXELS): the edge cap alone allows 400 MB of RGBA per render, a few in parallel OOM.
var MaxPixels int64 = 40_000_000

// scaleFor returns the source-to-output factor before cropping, capped so the longest edge stays <= maxSide.
func scaleFor(srcW, srcH float64, o Options, def float64, maxSide int) float64 {
	s := def
	sx := float64(o.Width) / srcW
	sy := float64(o.Height) / srcH
	switch {
	case o.Width > 0 && o.Height > 0:
		if o.Fit == "contain" {
			s = math.Min(sx, sy)
		} else { // cover and fill both need the larger factor, then crop or stretch
			s = math.Max(sx, sy)
		}
	case o.Width > 0:
		s = sx
	case o.Height > 0:
		s = sy
	}
	if longest := math.Max(srcW, srcH); longest*s > float64(maxSide) {
		s = float64(maxSide) / longest
	}
	if area := srcW * s * srcH * s; area > float64(MaxPixels) {
		s *= math.Sqrt(float64(MaxPixels) / area)
	}
	return s
}

// innerBox is the requested box minus padding.
func innerBox(o Options) Options {
	if o.Pad > 0 {
		if o.Width > 0 {
			o.Width = max(o.Width-2*o.Pad, 1)
		}
		if o.Height > 0 {
			o.Height = max(o.Height-2*o.Pad, 1)
		}
	}
	return o
}

// Fit brings an image rendered near the target size to the exact requested geometry.
func Fit(img image.Image, o Options, maxSide int) image.Image {
	content := fitContent(img, innerBox(o), maxSide)
	cb := content.Bounds()

	// pad: contain, then centre on a width×height canvas.
	if o.Fit == "pad" && o.Width > 0 && o.Height > 0 {
		x := (o.Width - cb.Dx()) / 2
		y := (o.Height - cb.Dy()) / 2
		switch o.Position {
		case "top":
			y = min(o.Pad, y)
		case "bottom":
			y = o.Height - cb.Dy() - min(o.Pad, y)
		}
		return onCanvas(content, o.Width, o.Height, x, y, o.BG)
	}
	if o.Pad > 0 {
		return onCanvas(content, cb.Dx()+2*o.Pad, cb.Dy()+2*o.Pad, o.Pad, o.Pad, o.BG)
	}
	return content
}

func fitContent(img image.Image, o Options, maxSide int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if o.Width == 0 && o.Height == 0 {
		s := scaleFor(float64(w), float64(h), o, 1, maxSide)
		if s >= 1 {
			return img
		}
		return scale(img, round(float64(w)*s), round(float64(h)*s))
	}

	tw, th := o.Width, o.Height
	if tw == 0 {
		tw = round(float64(w) * float64(th) / float64(h))
	}
	if th == 0 {
		th = round(float64(h) * float64(tw) / float64(w))
	}
	if o.Width == 0 || o.Height == 0 || o.Fit == "contain" || o.Fit == "pad" {
		s := math.Min(float64(tw)/float64(w), float64(th)/float64(h))
		return scale(img, round(float64(w)*s), round(float64(h)*s))
	}
	if o.Fit == "fill" {
		return scale(img, tw, th)
	}

	// cover: crop the box-shaped source region first so the intermediate is never larger than the box.
	sc := math.Max(float64(tw)/float64(w), float64(th)/float64(h))
	cw, ch := min(round(float64(tw)/sc), w), min(round(float64(th)/sc), h)
	x := b.Min.X + (w-cw)/2
	y := b.Min.Y + (h-ch)/2
	switch o.Position {
	case "top":
		y = b.Min.Y
	case "bottom":
		y = b.Max.Y - ch
	}
	return scaleExact(crop(img, image.Rect(x, y, x+cw, y+ch)), tw, th)
}

func onCanvas(img image.Image, w, h, x, y int, bg color.NRGBA) image.Image {
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)
	b := img.Bounds()
	draw.Draw(dst, image.Rect(x, y, x+b.Dx(), y+b.Dy()), img, b.Min, draw.Over)
	return dst
}

func scaleExact(img image.Image, w, h int) image.Image {
	if b := img.Bounds(); b.Dx() == w && b.Dy() == h {
		return img
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)
	return dst
}

// scale resamples only when the size changes (±1 px is rounding noise).
func scale(img image.Image, w, h int) image.Image {
	b := img.Bounds()
	w, h = max(w, 1), max(h, 1)
	if abs(b.Dx()-w) <= 1 && abs(b.Dy()-h) <= 1 {
		return img
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

func crop(img image.Image, r image.Rectangle) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(dst, dst.Bounds(), img, r.Min, draw.Src)
	return dst
}

func round(f float64) int { return int(f + 0.5) }

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
