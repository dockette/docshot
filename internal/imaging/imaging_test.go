package imaging

import (
	"image"
	"image/color"
	"testing"
)

func TestContentBounds(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 100, 80))
	for i := range img.Pix {
		img.Pix[i] = 255 // white page
	}
	for y := 20; y < 30; y++ { // a dark block: the "content"
		for x := 40; x < 70; x++ {
			img.SetNRGBA(x, y, color.NRGBA{0, 0, 0, 255})
		}
	}
	img.SetNRGBA(5, 5, color.NRGBA{250, 250, 250, 255}) // noise under the threshold
	if got, want := contentBounds(img, 25), image.Rect(40, 20, 70, 30); got != want {
		t.Errorf("bounds %v, want %v", got, want)
	}
	blank := image.NewNRGBA(image.Rect(0, 0, 10, 10))
	if got := contentBounds(blank, 25); got != blank.Bounds() {
		t.Errorf("uniform image must not be trimmed, got %v", got)
	}
}

func TestFitPadCanvas(t *testing.T) {
	src := page(1000, 1400)
	o := Options{Width: 400, Height: 300, Fit: "pad", Position: "center", Pad: 10, BG: color.NRGBA{0, 255, 0, 255}}
	img := Fit(src, o, 10000)
	if b := img.Bounds(); b.Dx() != 400 || b.Dy() != 300 {
		t.Fatalf("pad canvas %v, want 400x300", b)
	}
	if r, g, _, _ := img.At(5, 150).RGBA(); r != 0 || g == 0 {
		t.Error("left edge should be the green background")
	}
	if r, _, _, _ := img.At(200, 20).RGBA(); r == 0 {
		t.Error("centre top should be the page content (red half)")
	}
	// Padding without a box grows the image.
	img = Fit(src, Options{Pad: 8, BG: White}, 10000)
	if b := img.Bounds(); b.Dx() != 1016 || b.Dy() != 1416 {
		t.Errorf("outer padding %v, want 1016x1416", b)
	}
}

func TestPlaceholderSize(t *testing.T) {
	for _, o := range []Options{{}, {Width: 200}, {Height: 120}, {Width: 480, Height: 270}} {
		b := Placeholder("x.docx", o).Bounds()
		if (o.Width > 0 && b.Dx() != o.Width) || (o.Height > 0 && b.Dy() != o.Height) || b.Empty() {
			t.Errorf("placeholder for %+v is %v", o, b)
		}
	}
}

func TestPixelCap(t *testing.T) {
	old := MaxPixels
	MaxPixels = 1_000_000
	defer func() { MaxPixels = old }()
	// A4 at a huge dpi is scaled down to fit the pixel budget.
	if s := scaleFor(595, 842, Options{}, 20, 100000); 595*s*842*s > 1_000_001 {
		t.Errorf("area %v over the cap", 595*s*842*s)
	}
	// Cover of an extreme box: the intermediate must be the box, not page-sized.
	img := Fit(page(1000, 1400), Options{Width: 900, Height: 20, Fit: "cover", Position: "top"}, 10000)
	if b := img.Bounds(); b.Dx() != 900 || b.Dy() != 20 {
		t.Errorf("cover strip %v", b)
	}
}
