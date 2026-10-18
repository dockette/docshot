package render

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"image"
	"math"
	"net/http"
	"sync"

	"github.com/dockette/docshot/internal/apierr"
	"github.com/dockette/docshot/internal/detect"
	"github.com/dockette/docshot/internal/imaging"
)

const (
	OpPage  = "page"
	OpGrid  = "grid"
	OpPages = "pages"
)

func (r *Renderer) produce(ctx context.Context, doc docEntry, body []byte, o Options) (Result, error) {
	res := Result{Kind: doc.kind, Meta: Meta{Path: doc.path}, ContentType: "image/" + o.Format}

	want := 1
	switch o.Op {
	case OpGrid:
		want = o.GridCols * o.GridRows
		if want == 0 {
			want = 9
		}
	case OpPages:
		want = o.Limit
	}
	if doc.kind.Route == detect.Video && o.Op == OpPage && o.Page > 1 {
		return res, apierr.New(http.StatusBadRequest, "a video has no pages: use t=<seconds> for a frame, or /grid and /pages for several")
	}
	q, err := r.openSequence(ctx, doc, body, o, want)
	if err != nil {
		return res, err
	}
	defer q.Done()
	res.Meta.PageCount = q.N
	res.Meta.Duration = q.Duration

	switch o.Op {
	case OpGrid:
		res.Img, err = r.gridOf(q, o)
	case OpPages:
		res.Img, err = r.zipOf(q, o, doc.kind.Ext)
		res.ContentType = "application/zip"
	default:
		if o.Page > q.N {
			return res, apierr.New(http.StatusRequestedRangeNotSatisfiable, "page %d requested, %s has %d", o.Page, doc.kind.Ext, q.N)
		}
		var img image.Image
		if img, err = q.Item(o.Page-1, o.Options); err == nil {
			res.Img, err = imaging.Encode(img, o.Options)
		}
		res.Meta.Dpi = q.DPI
	}
	return res, err
}

// pick chooses grid items: n from start for documents, n evenly spread for animations.
func pick(q *imaging.Sequence, start, n int) []int {
	if start > q.N {
		return nil
	}
	if q.Spread {
		n = min(n, q.N)
		idx := make([]int, n)
		for i := range idx {
			idx[i] = int((float64(i) + 0.5) * float64(q.N) / float64(n))
		}
		return idx
	}
	n = min(n, q.N-start+1)
	idx := make([]int, n)
	for i := range idx {
		idx[i] = start - 1 + i
	}
	return idx
}

// autoLayout picks columns and rows for n cells.
func autoLayout(n int) (cols, rows int) {
	switch {
	case n <= 3:
		cols = max(n, 1)
	case n == 4:
		cols = 2
	case n <= 9:
		cols = 3
	default:
		cols = 4
	}
	return cols, int(math.Ceil(float64(n) / float64(cols)))
}

func (r *Renderer) gridOf(q *imaging.Sequence, o Options) ([]byte, error) {
	n := o.GridCols * o.GridRows
	if n == 0 {
		n = 12
	}
	idx := pick(q, o.Page, n)
	if len(idx) == 0 {
		return nil, apierr.New(http.StatusRequestedRangeNotSatisfiable, "page %d requested, there are %d", o.Page, q.N)
	}
	og := o
	if og.GridCols == 0 {
		og.GridCols, og.GridRows = autoLayout(len(idx))
	}
	g := imaging.NewGrid(o.Options, og.GridCols, len(idx), o.Labels, q.Dark)
	cellOpts := imaging.Options{DPI: o.DPI, Width: g.CellW, Fit: "contain", Trim: o.Trim, BG: imaging.White}

	cells := make([]image.Image, len(idx))
	errs := make([]error, len(idx))
	if q.Parallel {
		// independent items: a few at a time
		var wg sync.WaitGroup
		sem := make(chan struct{}, 4)
		for k, i := range idx {
			wg.Add(1)
			go func(k, i int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				cells[k], errs[k] = q.Item(i, cellOpts)
			}(k, i)
		}
		wg.Wait()
	} else {
		for k, i := range idx { // one PDFium instance: sequential
			cells[k], errs[k] = q.Item(i, cellOpts)
		}
	}
	var got []image.Image
	var gotLabels []string
	for k, i := range idx {
		if errs[k] == nil && cells[k] != nil { // e.g. a frame past a truncated end
			got = append(got, cells[k])
			gotLabels = append(gotLabels, q.Label(i))
		}
	}
	if len(got) == 0 {
		return nil, errs[0]
	}
	return imaging.Encode(imaging.FitSheet(g.Compose(got, gotLabels), o.Options, r.cfg.MaxSide), o.Options)
}

func (r *Renderer) zipOf(q *imaging.Sequence, o Options, ext string) ([]byte, error) {
	idx := pick(&imaging.Sequence{N: q.N, Spread: q.Spread}, o.Page, o.Limit)
	if len(idx) == 0 {
		return nil, apierr.New(http.StatusRequestedRangeNotSatisfiable, "page %d requested, there are %d", o.Page, q.N)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	imgExt := o.Format
	if imgExt == "jpeg" {
		imgExt = "jpg"
	}
	for _, i := range idx {
		img, err := q.Item(i, o.Options)
		if err != nil {
			return nil, err
		}
		b, err := imaging.Encode(img, o.Options)
		if err != nil {
			return nil, err
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("%04d.%s", i+1, imgExt), Method: zip.Store})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(b); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
