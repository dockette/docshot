package imaging

import (
	"image"
	"image/color"
	"testing"
)

// page is a portrait image, red top half and blue bottom, so crops can be checked by colour.
func page(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		c := color.RGBA{255, 0, 0, 255}
		if y >= h/2 {
			c = color.RGBA{0, 0, 255, 255}
		}
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

func TestFitImageGeometry(t *testing.T) {
	src := page(1000, 1400)
	cases := []struct {
		name         string
		o            Options
		wantW, wantH int
	}{
		{"no box keeps size", Options{}, 1000, 1400},
		{"width only keeps aspect", Options{Width: 500}, 500, 700},
		{"height only keeps aspect", Options{Height: 700}, 500, 700},
		{"contain fits inside box", Options{Width: 300, Height: 300, Fit: "contain"}, 214, 300},
		{"cover fills box exactly", Options{Width: 300, Height: 300, Fit: "cover"}, 300, 300},
		{"fill stretches", Options{Width: 300, Height: 100, Fit: "fill"}, 300, 100},
		{"upscale allowed", Options{Width: 2000}, 2000, 2800},
	}
	for _, c := range cases {
		b := Fit(src, c.o, 10000).Bounds()
		if b.Dx() != c.wantW || b.Dy() != c.wantH {
			t.Errorf("%s: got %dx%d, want %dx%d", c.name, b.Dx(), b.Dy(), c.wantW, c.wantH)
		}
	}
}

func TestFitImageMaxSide(t *testing.T) {
	b := Fit(page(1000, 20000), Options{}, 10000).Bounds()
	if b.Dy() != 10000 || b.Dx() != 500 {
		t.Errorf("cap: got %dx%d, want 500x10000", b.Dx(), b.Dy())
	}
}

func TestCoverPosition(t *testing.T) {
	src := page(1000, 1400)
	isRed := func(c color.Color) bool { r, _, b, _ := c.RGBA(); return r > b }
	for pos, wantRed := range map[string]bool{"top": true, "bottom": false} {
		img := Fit(src, Options{Width: 300, Height: 100, Fit: "cover", Position: pos}, 10000)
		mid := img.At(150, 50)
		if isRed(mid) != wantRed {
			t.Errorf("position=%s: crop came from the wrong end", pos)
		}
	}
}

func TestScaleFor(t *testing.T) {
	// A4 in points; 150 dpi default.
	const pw, ph = 595.0, 842.0
	if s := scaleFor(pw, ph, Options{}, 150.0/72, 10000); s != 150.0/72 {
		t.Errorf("default scale %v", s)
	}
	if s := scaleFor(pw, ph, Options{Width: 595}, 2, 10000); s != 1 {
		t.Errorf("width scale %v, want 1", s)
	}
	// cover needs the larger factor so the crop has pixels to give.
	if s := scaleFor(pw, ph, Options{Width: 595, Height: 595, Fit: "cover"}, 2, 10000); s != 1 {
		t.Errorf("cover scale %v, want 1", s)
	}
	if s := scaleFor(pw, ph, Options{Width: 595, Height: 421, Fit: "contain"}, 2, 10000); s != 0.5 {
		t.Errorf("contain scale %v, want 0.5", s)
	}
	if s := scaleFor(100, 100000, Options{}, 2, 10000); s != 0.1 {
		t.Errorf("maxSide cap %v, want 0.1", s)
	}
}
