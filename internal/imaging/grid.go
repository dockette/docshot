package imaging

import (
	"image"
	"image/color"
	"image/draw"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

// grid lays cells out as a contact sheet.
type grid struct {
	cols, rows   int
	sheetW, gap  int
	CellW        int
	bg           color.NRGBA
	labels, dark bool
}

func NewGrid(o Options, cols, n int, labels, dark bool) grid {
	g := grid{cols: min(cols, n), sheetW: 1200, labels: labels, dark: dark}
	g.cols = max(g.cols, 1)
	g.rows = (n + g.cols - 1) / g.cols
	if o.Width > 0 {
		g.sheetW = o.Width
	}
	g.gap = max(g.sheetW/120, 4)
	g.CellW = max((g.sheetW-g.gap*(g.cols+1))/g.cols, 16)
	g.bg = color.NRGBA{229, 231, 235, 255} // documents: light grey around white pages
	if dark {
		g.bg = color.NRGBA{17, 17, 17, 255} // video: dark, like a film strip
	}
	if o.BGSet {
		g.bg = o.BG
	}
	return g
}

// Compose centres each cell in a slot sized by the first cell's aspect ratio.
func (g grid) Compose(cells []image.Image, labels []string) image.Image {
	if len(cells) == 0 {
		return image.NewNRGBA(image.Rect(0, 0, 1, 1))
	}
	fb := cells[0].Bounds()
	cellH := max(g.CellW*fb.Dy()/max(fb.Dx(), 1), 16)
	sheet := image.NewNRGBA(image.Rect(0, 0, g.sheetW, g.gap+g.rows*(cellH+g.gap)))
	draw.Draw(sheet, sheet.Bounds(), image.NewUniform(g.bg), image.Point{}, draw.Src)

	for i, c := range cells {
		col, row := i%g.cols, i/g.cols
		x0 := g.gap + col*(g.CellW+g.gap)
		y0 := g.gap + row*(cellH+g.gap)
		c = fitContent(c, Options{Width: g.CellW, Height: cellH, Fit: "contain"}, 1<<30)
		b := c.Bounds()
		x := x0 + (g.CellW-b.Dx())/2
		y := y0 + (cellH-b.Dy())/2
		draw.Draw(sheet, image.Rect(x, y, x+b.Dx(), y+b.Dy()), c, b.Min, draw.Src)
		if g.labels && i < len(labels) {
			drawLabel(sheet, labels[i], x0, y0+cellH, g.CellW)
		}
	}
	return sheet
}

func drawLabel(dst draw.Image, text string, x0, yBottom, cellW int) {
	size := max(cellW/14, 10)
	face := Face(size)
	d := font.Drawer{Face: face}
	w := d.MeasureString(text).Round()
	pad := max(size/3, 3)
	m := face.Metrics()
	h := (m.Ascent + m.Descent).Round()
	box := image.Rect(x0+pad, yBottom-pad-h-2*pad, x0+pad+w+2*pad, yBottom-pad)
	draw.Draw(dst, box, image.NewUniform(color.NRGBA{0, 0, 0, 170}), image.Point{}, draw.Over)
	d.Dst = dst
	d.Src = image.NewUniform(color.NRGBA{255, 255, 255, 255})
	d.Dot = fixed.P(box.Min.X+pad, box.Max.Y-pad-m.Descent.Round())
	d.DrawString(text)
}

// FitSheet applies the caller's box to the finished sheet.
func FitSheet(sheet image.Image, o Options, maxSide int) image.Image {
	if o.Height > 0 {
		return Fit(sheet, Options{Width: o.Width, Height: o.Height, Fit: o.Fit, Position: o.Position, BG: o.BG}, maxSide)
	}
	return Fit(sheet, Options{}, maxSide)
}

var (
	faces   = map[int]font.Face{}
	facesMu sync.Mutex
)

// Face returns a cached font face (DejaVu/Noto Bold, else the bitmap font).
func Face(size int) font.Face {
	facesMu.Lock()
	defer facesMu.Unlock()
	if f, ok := faces[size]; ok {
		return f
	}
	f := placeholderFace(float64(size))
	faces[size] = f
	return f
}
