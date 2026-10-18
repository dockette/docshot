package imaging

import (
	"bytes"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"net/http"
	"time"

	"github.com/dockette/docshot/internal/apierr"
	"github.com/gen2brain/webp"
	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// Rasterizer pools PDFium WebAssembly instances; PDFium is not thread-safe, so each request gets its own.
type Rasterizer struct {
	pool pdfium.Pool
}

func NewRasterizer(workers int) (*Rasterizer, error) {
	pool, err := webassembly.Init(webassembly.Config{MinIdle: 1, MaxIdle: workers, MaxTotal: workers})
	if err != nil {
		return nil, err
	}
	return &Rasterizer{pool: pool}, nil
}

func (r *Rasterizer) Close() { _ = r.pool.Close() }

func (r *Rasterizer) Ping(timeout time.Duration) error {
	inst, err := r.pool.GetInstance(timeout)
	if err != nil {
		return err
	}
	return inst.Close()
}

// pageImage renders page idx (0-based) to its final geometry as a Go-owned copy: the WebAssembly pixel buffer dies at Cleanup.
func pageImage(inst pdfium.Pdfium, doc references.FPDF_DOCUMENT, idx int, o Options, maxSide int) (image.Image, int, error) {
	page := requests.Page{ByIndex: &requests.PageByIndex{Document: doc, Index: idx}}
	size, err := inst.GetPageSize(&requests.GetPageSize{Page: page})
	if err != nil {
		return nil, 0, apierr.New(http.StatusUnprocessableEntity, "page size: %v", err)
	}

	// trim: a cheap low-res pass finds the content, then only that region is rendered (margins cut in vector space).
	region := requests.RenderPageCrop{Width: size.Width, Height: size.Height}
	if o.Trim > 0 {
		if r, ok := contentRegion(inst, page, doc, size.Width, size.Height, o.Trim); ok {
			region = r
		}
	}

	// Render at output size (vector-sharp); width/height beat dpi, longest edge is capped against gigapixel sheets.
	s := scaleFor(region.Width, region.Height, innerBox(o), float64(o.DPI)/72, maxSide)
	w := round(region.Width * s)
	h := round(region.Height * s)

	req := &requests.RenderPageInPixels{
		Page: page, Width: max(w, 1), Height: max(h, 1), RenderForm: true, Document: &doc,
	}
	if region.Width < size.Width || region.Height < size.Height {
		req.Crop = &region
	}
	res, err := inst.RenderPageInPixels(req)
	if err != nil {
		return nil, 0, apierr.New(http.StatusUnprocessableEntity, "rendering page %d: %v", idx+1, err)
	}
	defer res.Cleanup()
	img := Fit(res.Result.RenderedImage, o, maxSide)
	if img == res.Result.RenderedImage {
		img = clone(img)
	}
	return img, round(s * 72), nil
}

func clone(img image.Image) image.Image {
	dst := image.NewRGBA(img.Bounds())
	draw.Draw(dst, dst.Bounds(), img, img.Bounds().Min, draw.Src)
	return dst
}

// contentRegion finds the content box on a small render, in PDF points with a 1 px margin.
func contentRegion(inst pdfium.Pdfium, page requests.Page, doc references.FPDF_DOCUMENT, pw, ph float64, trim int) (requests.RenderPageCrop, bool) {
	s := 800 / max(pw, ph)
	res, err := inst.RenderPageInPixels(&requests.RenderPageInPixels{
		Page: page, Width: max(round(pw*s), 1), Height: max(round(ph*s), 1), RenderForm: true, Document: &doc,
	})
	if err != nil {
		return requests.RenderPageCrop{}, false
	}
	defer res.Cleanup()
	img := res.Result.RenderedImage
	r := contentBounds(img, trim*255/100)
	if r == img.Bounds() {
		return requests.RenderPageCrop{}, false
	}
	px := pw / float64(img.Bounds().Dx()) // points per detection pixel
	x0, y0 := max(float64(r.Min.X-1), 0)*px, max(float64(r.Min.Y-1), 0)*px
	x1, y1 := min(float64(r.Max.X+1)*px, pw), min(float64(r.Max.Y+1)*px, ph)
	return requests.RenderPageCrop{X: x0, Y: y0, Width: x1 - x0, Height: y1 - y0}, true
}

func Encode(img image.Image, o Options) ([]byte, error) {
	var buf bytes.Buffer
	var err error
	switch o.Format {
	case "jpeg":
		bg := o.BG
		if bg.A == 0 {
			bg = White // JPEG has no alpha; "transparent" means white here
		}
		err = jpeg.Encode(&buf, flatten(img, bg), &jpeg.Options{Quality: o.Quality})
	case "webp":
		// libwebp transpiled to Go (-tags nodynamic); lossy by default.
		err = webp.Encode(&buf, img, webp.Options{Quality: o.Quality, Method: 4})
	default:
		err = png.Encode(&buf, img)
	}
	return buf.Bytes(), err
}

// flatten composites onto bg: JPEG has no alpha.
func flatten(img image.Image, bg color.NRGBA) image.Image {
	b := img.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, image.NewUniform(bg), image.Point{}, draw.Src)
	draw.Draw(dst, b, img, b.Min, draw.Over)
	return dst
}

// PageSizes returns every page's [width, height] in PDF points.
func (r *Rasterizer) PageSizes(pdf []byte) ([][2]float64, error) {
	inst, err := r.pool.GetInstance(60 * time.Second)
	if err != nil {
		return nil, apierr.New(http.StatusServiceUnavailable, "pdfium busy: %v", err)
	}
	defer inst.Close()
	doc, err := inst.OpenDocument(&requests.OpenDocument{File: &pdf})
	if err != nil {
		return nil, PDFError(err)
	}
	defer inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})
	count, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		return nil, apierr.New(http.StatusUnprocessableEntity, "counting pages: %v", err)
	}
	out := make([][2]float64, 0, count.PageCount)
	for i := 0; i < count.PageCount; i++ {
		sz, err := inst.GetPageSize(&requests.GetPageSize{Page: requests.Page{ByIndex: &requests.PageByIndex{Document: doc.Document, Index: i}}})
		if err != nil {
			return nil, apierr.New(http.StatusUnprocessableEntity, "page %d size: %v", i+1, err)
		}
		out = append(out, [2]float64{math.Round(sz.Width*10) / 10, math.Round(sz.Height*10) / 10})
	}
	return out, nil
}
