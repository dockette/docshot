package imaging

import (
	"image"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dockette/docshot/internal/apierr"
	"github.com/klippa-app/go-pdfium/requests"
)

// Sequence is anything shown as one or more pictures: document pages, video frames, archive files.
type Sequence struct {
	N        int                                         // items available
	Spread   bool                                        // grids sample evenly (animations) instead of taking the next N
	Parallel bool                                        // items are independent and may be fetched concurrently (video)
	Dark     bool                                        // grid background: dark for video, light for paper
	Item     func(i int, o Options) (image.Image, error) // item i (0-based) at o's geometry
	Label    func(i int) string                          // page number, timestamp, file name
	Close    func()
	DPI      int     // effective dpi of the last rendered document page
	Duration float64 // video and audio, seconds
}

func (q *Sequence) Done() {
	if q.Close != nil {
		q.Close()
	}
}

// PDFSequence holds one PDFium instance with the document open until Done.
func (r *Rasterizer) PDFSequence(pdf []byte, maxSide int) (*Sequence, error) {
	inst, err := r.pool.GetInstance(60 * time.Second)
	if err != nil {
		return nil, apierr.New(http.StatusServiceUnavailable, "pdfium busy: %v", err)
	}
	doc, err := inst.OpenDocument(&requests.OpenDocument{File: &pdf})
	if err != nil {
		inst.Close()
		return nil, PDFError(err)
	}
	count, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})
		inst.Close()
		return nil, apierr.New(http.StatusUnprocessableEntity, "counting pages: %v", err)
	}
	q := &Sequence{N: count.PageCount}
	q.Item = func(i int, o Options) (image.Image, error) {
		img, dpi, err := pageImage(inst, doc.Document, i, o, maxSide)
		q.DPI = dpi
		return img, err
	}
	q.Label = func(i int) string { return strconv.Itoa(i + 1) }
	q.Close = func() {
		inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})
		inst.Close()
	}
	return q, nil
}

func Still(img image.Image, maxSide int) *Sequence {
	return Frames([]image.Image{img}, maxSide)
}

func Frames(frames []image.Image, maxSide int) *Sequence {
	return &Sequence{
		N:      len(frames),
		Spread: len(frames) > 1,
		Item: func(i int, o Options) (image.Image, error) {
			img := frames[i]
			if o.Trim > 0 {
				img = Trim(img, o.Trim*255/100)
			}
			return Fit(img, o, maxSide), nil
		},
		Label: func(i int) string { return strconv.Itoa(i + 1) },
	}
}

// PDFError turns PDFium's numeric errors into words.
func PDFError(err error) error {
	m := strings.ToLower(err.Error())
	switch {
	case strings.Contains(m, "password"):
		return &apierr.Error{Status: http.StatusUnprocessableEntity, Code: "password_protected", Msg: "the PDF is password-protected"}
	case strings.Contains(m, "format") || strings.Contains(m, "file"):
		return &apierr.Error{Status: http.StatusUnprocessableEntity, Code: "unreadable_file", Msg: "not a valid PDF, or damaged beyond repair"}
	}
	return apierr.New(http.StatusUnprocessableEntity, "opening PDF: %v", err)
}
