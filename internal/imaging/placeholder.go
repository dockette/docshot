package imaging

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Placeholder on error: status stays 200 (browsers skip error bodies), the real error goes into X-Docshot-Error.

var (
	labelFace     font.Face
	labelFaceOnce sync.Once
)

func placeholderFace(size float64) font.Face {
	for _, p := range []string{
		"/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf",
		"/usr/share/fonts/truetype/noto/NotoSans-Bold.ttf",
	} {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		f, err := opentype.Parse(data)
		if err != nil {
			continue
		}
		if face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull}); err == nil {
			return face
		}
	}
	return basicfont.Face7x13
}

func Placeholder(name string, o Options) image.Image {
	w, h := o.Width, o.Height
	switch {
	case w == 0 && h == 0:
		w, h = 300, 400
	case h == 0:
		h = w * 4 / 3
	case w == 0:
		w = h * 3 / 4
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.NRGBA{243, 244, 246, 255}), image.Point{}, draw.Src)

	side := min(w, h)
	pw, ph := side*55/100, side*55/100*4/3
	if ph > h*80/100 {
		ph = h * 80 / 100
		pw = ph * 3 / 4
	}
	x0, y0 := (w-pw)/2, (h-ph)/2
	fold := pw / 4
	border := color.NRGBA{156, 163, 175, 255}
	for y := y0; y < y0+ph; y++ {
		for x := x0; x < x0+pw; x++ {
			dx, dy := x-(x0+pw-fold), y-y0
			if dx > dy { // cut the top-right corner
				continue
			}
			c := color.NRGBA{255, 255, 255, 255}
			if x < x0+2 || x >= x0+pw-2 || y < y0+2 || y >= y0+ph-2 || (dx >= dy-2 && dx >= 0) {
				c = border
			}
			img.SetNRGBA(x, y, c)
		}
	}

	ext := strings.ToUpper(strings.TrimPrefix(filepath.Ext(name), "."))
	if ext == "" {
		ext = "?"
	}
	labelFaceOnce.Do(func() { labelFace = placeholderFace(float64(max(side/9, 10))) })
	d := font.Drawer{Dst: img, Src: image.NewUniform(color.NRGBA{75, 85, 99, 255}), Face: labelFace}
	adv := d.MeasureString(ext)
	d.Dot = fixed.P(x0+(pw-adv.Round())/2, y0+ph*2/3)
	d.DrawString(ext)
	return img
}
