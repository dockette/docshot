package media

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"testing"

	"github.com/dockette/docshot/internal/imaging"
)

func TestDecompressionBomb(t *testing.T) {
	old := imaging.MaxPixels
	imaging.MaxPixels = 1000
	defer func() { imaging.MaxPixels = old }()
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewGray(image.Rect(0, 0, 100, 100))) // 10k px > 3×1000
	if _, err := ImageSequence(context.Background(), buf.Bytes(), "png", 10000); err == nil {
		t.Error("oversized source image was decoded")
	}
}

func TestGIFFrames(t *testing.T) {
	pal := color.Palette{color.Transparent, color.NRGBA{255, 0, 0, 255}, color.NRGBA{0, 0, 255, 255}}
	f1 := image.NewPaletted(image.Rect(0, 0, 4, 4), pal)
	for i := range f1.Pix {
		f1.Pix[i] = 1 // red everywhere
	}
	f2 := image.NewPaletted(image.Rect(0, 0, 2, 2), pal) // a blue patch over the corner
	for i := range f2.Pix {
		f2.Pix[i] = 2
	}
	frames := gifFrames(&gif.GIF{Image: []*image.Paletted{f1, f2}, Delay: []int{0, 0}, Disposal: []byte{0, 0},
		Config: image.Config{Width: 4, Height: 4}})
	if r, _, b, _ := frames[1].At(3, 3).RGBA(); r == 0 || b != 0 {
		t.Error("frame 2 lost the red from frame 1 (no compositing)")
	}
	if r, _, b, _ := frames[1].At(0, 0).RGBA(); r != 0 || b == 0 {
		t.Error("frame 2 corner should be blue")
	}
}
