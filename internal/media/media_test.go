package media

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/color/palette"
	"image/gif"
	"image/png"
	"math/rand/v2"
	"testing"

	"github.com/dockette/docshot/internal/imaging"
	"golang.org/x/image/bmp"
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

func TestSalvage(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	rnd := rand.New(rand.NewPCG(1, 2))
	for i := range src.Pix {
		src.Pix[i] = byte(rnd.Uint32()) | 1 // noise: barely compresses, no zero pixels
	}
	var pngBuf bytes.Buffer
	_ = png.Encode(&pngBuf, src)
	full := pngBuf.Bytes()

	pal := image.NewPaletted(src.Bounds(), palette.Plan9)
	copy(pal.Pix, src.Pix)
	var palBuf bytes.Buffer
	_ = png.Encode(&palBuf, pal)

	flipped := bytes.Clone(full)
	copy(flipped[len(full)*3/5:], "UUUUUUUU")
	badCRC := bytes.Clone(full)
	badCRC[len(full)-20] ^= 0xff

	var bmpBuf bytes.Buffer
	_ = bmp.Encode(&bmpBuf, src)

	for name, b := range map[string][]byte{
		"png half":     full[:len(full)/2],
		"png no IEND":  full[:len(full)-12],
		"png corrupt":  flipped,
		"png bad crc":  badCRC,
		"palette half": palBuf.Bytes()[:palBuf.Len()/2],
		"bmp half":     bmpBuf.Bytes()[:bmpBuf.Len()/2],
	} {
		q, err := ImageSequence(context.Background(), b, "", 10000)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		img, _ := q.Item(0, imaging.Options{})
		if img.Bounds().Dx() != 64 || img.Bounds().Dy() != 64 {
			t.Errorf("%s: decoded %v, want 64x64", name, img.Bounds())
		}
	}

	frames := make([]*image.Paletted, 3)
	for i := range frames {
		frames[i] = image.NewPaletted(src.Bounds(), palette.Plan9)
		copy(frames[i].Pix, src.Pix[i:])
	}
	var gifBuf bytes.Buffer
	_ = gif.EncodeAll(&gifBuf, &gif.GIF{Image: frames, Delay: []int{10, 10, 10}})
	if q, err := ImageSequence(context.Background(), gifBuf.Bytes()[:gifBuf.Len()-100], "gif", 10000); err != nil || q.N != 2 {
		t.Errorf("gif cut in its last frame: %v, want the 2 complete frames", err)
	}

	img, err := png.Decode(bytes.NewReader(salvagePNG(full[:len(full)/2])))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := img.At(5, 0), src.At(5, 0); got != want {
		t.Errorf("first row of a truncated png: %v, want %v", got, want)
	}
	if _, _, _, a := img.At(5, 63).RGBA(); a != 0 {
		t.Error("missing rows of a truncated png should be transparent")
	}
	if salvagePNG(full) == nil {
		t.Error("an intact png should round-trip")
	}
	if salvagePNG([]byte("not an image")) != nil || salvageGIF([]byte("GIF89a")) != nil || salvageICO(make([]byte, 30)) != nil {
		t.Error("garbage was salvaged")
	}
}
