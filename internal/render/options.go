package render

import (
	"fmt"

	"github.com/dockette/docshot/internal/imaging"
)

// Options is everything that changes the output bytes.
type Options struct {
	imaging.Options
	Op   string // page | grid | pages
	Page int    // 1-based

	// 0×0 = no grid.
	GridCols, GridRows int
	At                 float64 // video: seconds of the single frame (when AtSet)
	AtSet              bool
	Labels             bool
	Limit              int
}

// Key is the canonical cache-key form of the options.
func (o Options) Key() string {
	return fmt.Sprintf("%s p%d d%d w%d h%d %s %s %s q%d l%t t%d pad%d bg%x g%dx%d at%g/%t lab%t lim%d",
		o.Op, o.Page, o.DPI, o.Width, o.Height, o.Fit, o.Position, o.Format, o.Quality, false, o.Trim, o.Pad,
		[]uint8{o.BG.R, o.BG.G, o.BG.B, o.BG.A}, o.GridCols, o.GridRows, o.At, o.AtSet, o.Labels, o.Limit)
}
