package imaging

import (
	"image"
	"image/draw"
)

// contentBounds returns the box of non-background pixels (background = top-left pixel, threshold on 0-255),
// or the full bounds for a uniform image.
func contentBounds(img image.Image, threshold int) image.Rectangle {
	b := img.Bounds()
	n, ok := img.(*image.NRGBA)
	if !ok {
		n = image.NewNRGBA(b)
		draw.Draw(n, b, img, b.Min, draw.Src)
	}
	ref := n.Pix[0:4]
	differs := func(x, y int) bool {
		i := n.PixOffset(x, y)
		for c := 0; c < 4; c++ {
			d := int(n.Pix[i+c]) - int(ref[c])
			if d > threshold || -d > threshold {
				return true
			}
		}
		return false
	}
	rowHas := func(y int) bool {
		for x := b.Min.X; x < b.Max.X; x++ {
			if differs(x, y) {
				return true
			}
		}
		return false
	}
	colHas := func(x, y0, y1 int) bool {
		for y := y0; y < y1; y++ {
			if differs(x, y) {
				return true
			}
		}
		return false
	}

	top := b.Min.Y
	for top < b.Max.Y && !rowHas(top) {
		top++
	}
	if top == b.Max.Y {
		return b // uniform
	}
	bottom := b.Max.Y
	for bottom > top && !rowHas(bottom-1) {
		bottom--
	}
	left := b.Min.X
	for left < b.Max.X && !colHas(left, top, bottom) {
		left++
	}
	right := b.Max.X
	for right > left && !colHas(right-1, top, bottom) {
		right--
	}
	return image.Rect(left, top, right, bottom)
}

func Trim(img image.Image, threshold int) image.Image {
	r := contentBounds(img, threshold)
	if r == img.Bounds() {
		return img
	}
	return crop(img, r)
}
