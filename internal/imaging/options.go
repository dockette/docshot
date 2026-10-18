// Package imaging renders documents to pictures and lays them out.
package imaging

import (
	"image/color"
)

// Options is what changes the pixels of one picture.
type Options struct {
	DPI      int
	Width    int // target box; either may be 0 (follows aspect). Overrides dpi.
	Height   int
	Fit      string // contain | cover | fill | pad
	Position string // top | center | bottom — what a cover crop keeps, where pad places
	Format   string // png | jpeg | webp (auto is resolved from Accept before this)
	Quality  int
	Trim     int         // 0 = off; else colour threshold (1–100) for cutting uniform margins
	Pad      int         // px of bg around the content (inside the box when there is one)
	BG       color.NRGBA // background for pad/fit=pad/JPEG flattening
	BGSet    bool
}

var White = color.NRGBA{255, 255, 255, 255}
