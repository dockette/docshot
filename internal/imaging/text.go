package imaging

import (
	"image"
	"image/color"
	"image/draw"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

func DrawText(dst draw.Image, s string, x, baseline, size int, c color.Color) {
	d := font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: Face(size), Dot: fixed.P(x, baseline)}
	d.DrawString(s)
}
