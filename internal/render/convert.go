package render

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/dockette/docshot/internal/apierr"
	"github.com/dockette/docshot/internal/detect"
	"github.com/dockette/docshot/internal/htmlconv"
	"github.com/dockette/docshot/internal/media"
)

type Meta struct {
	Path      string
	PageCount int
	Dpi       int
	Duration  float64 // video, seconds
}

func (r *Renderer) toPDF(ctx context.Context, k detect.Kind, body []byte) ([]byte, error) {
	switch k.Route {
	case detect.PDF:
		return body, nil
	case detect.Office:
		return r.gotenberg(ctx, "/forms/libreoffice/convert", map[string][]byte{"document." + loExt(k.Ext): body}, nil)
	case detect.Sheet:
		// Otherwise wide sheets are chopped into print-width strips; with it page N = sheet N.
		return r.gotenberg(ctx, "/forms/libreoffice/convert", map[string][]byte{"document." + loExt(k.Ext): body},
			map[string]string{"singlePageSheets": "true"})
	case detect.HTML:
		return r.chromiumHTML(ctx, htmlconv.EnsureCharset(htmlconv.DecodeText(body)))
	case detect.Markdown:
		return r.gotenberg(ctx, "/forms/chromium/convert/markdown", map[string][]byte{
			"index.html":  []byte(htmlconv.MarkdownTemplate),
			"document.md": []byte(htmlconv.DecodeText(body)),
		}, a4())
	case detect.Email:
		page, err := htmlconv.EmailToHTML(body, k.Ext == "mht" || k.Ext == "mhtml")
		if err != nil {
			return nil, apierr.New(http.StatusUnprocessableEntity, "parsing e-mail: %v", err)
		}
		return r.chromiumHTML(ctx, page)
	case detect.TIFF:
		return media.TIFFToPDF(ctx, body)
	case detect.DjVu:
		return media.DjVuToPDF(ctx, body)
	case detect.XPS:
		return media.XPSToPDF(ctx, body, k.Ext)
	case detect.Graphviz:
		return media.GraphvizToPDF(ctx, body)
	case detect.SVG:
		page, w, h := htmlconv.SVGPage(body)
		return r.gotenberg(ctx, "/forms/chromium/convert/html", map[string][]byte{"index.html": []byte(page)}, map[string]string{
			"paperWidth": strconv.FormatFloat(w, 'f', 2, 64), "paperHeight": strconv.FormatFloat(h, 'f', 2, 64), "marginTop": "0", "marginBottom": "0", "marginLeft": "0", "marginRight": "0",
			"printBackground": "true", "omitBackground": "true",
		})
	case detect.Table:
		page, err := htmlconv.CSVTable(body, k.Ext)
		if err != nil {
			return nil, err
		}
		return r.gotenberg(ctx, "/forms/chromium/convert/html", map[string][]byte{"index.html": []byte(page)}, map[string]string{
			"paperWidth": "11.69", "paperHeight": "8.27", "marginTop": "0.4", "marginBottom": "0.4", "marginLeft": "0.4", "marginRight": "0.4",
			"printBackground": "true",
		})
	case detect.JSON:
		return r.chromiumHTML(ctx, htmlconv.TextPage(htmlconv.CapText(htmlconv.PrettyJSON(body))))
	case detect.Notebook:
		return r.htmlBuilder(ctx, func() (string, error) { return htmlconv.NotebookHTML(body) })
	case detect.Calendar:
		return r.htmlBuilder(ctx, func() (string, error) { return htmlconv.CalendarHTML(body) })
	case detect.Contacts:
		return r.htmlBuilder(ctx, func() (string, error) { return htmlconv.ContactsHTML(body) })
	case detect.Font:
		return r.htmlBuilder(ctx, func() (string, error) { return htmlconv.FontSpecimen(body, k.Ext, k.Name) })
	case detect.Msg:
		return r.htmlBuilder(ctx, func() (string, error) { return htmlconv.MsgToHTML(body) })
	case detect.EPUB:
		page, err := htmlconv.EPUBToHTML(body)
		if err != nil {
			return nil, err
		}
		return r.chromiumHTML(ctx, page)
	case detect.Text:
		return r.chromiumHTML(ctx, htmlconv.TextPage(htmlconv.CapText(htmlconv.DecodeText(body))))
	}
	return nil, apierr.New(http.StatusUnsupportedMediaType, "unsupported format %q", k.Ext)
}

// loAlias maps formats LibreOffice imports but Gotenberg's extension allow-list rejects to an accepted extension.
var loAlias = map[string]string{"pmd": "doc", "fb2": "xml", "lrf": "doc", "vstx": "vsdx", "vssx": "vsdx"}

func loExt(ext string) string {
	if a, ok := loAlias[ext]; ok {
		return a
	}
	return ext
}

func (r *Renderer) htmlBuilder(ctx context.Context, build func() (string, error)) ([]byte, error) {
	page, err := build()
	if err != nil {
		return nil, err
	}
	return r.chromiumHTML(ctx, page)
}

func (r *Renderer) chromiumHTML(ctx context.Context, page string) ([]byte, error) {
	return r.gotenberg(ctx, "/forms/chromium/convert/html", map[string][]byte{"index.html": []byte(page)}, a4())
}

// a4 overrides Gotenberg's Letter default.
func a4() map[string]string {
	return map[string]string{
		"paperWidth": "8.27", "paperHeight": "11.69",
		"marginTop": "0.5", "marginBottom": "0.5", "marginLeft": "0.5", "marginRight": "0.5",
		"printBackground": "true",
	}
}

func (r *Renderer) gotenberg(ctx context.Context, route string, files map[string][]byte, fields map[string]string) ([]byte, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, data := range files {
		fw, err := mw.CreateFormFile("files", name)
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write(data); err != nil {
			return nil, err
		}
	}
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.cfg.GotenbergURL+route, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, apierr.New(http.StatusBadGateway, "gotenberg: %v", err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, apierr.New(http.StatusBadGateway, "gotenberg: reading response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(out[:min(len(out), 300)]))
		// Gotenberg's 400 means unsupported or corrupt input: the caller's fault.
		status := http.StatusBadGateway
		if resp.StatusCode == http.StatusBadRequest {
			status = http.StatusUnprocessableEntity
		}
		return nil, apierr.New(status, "gotenberg %s %s: %s", route, resp.Status, msg)
	}
	return out, nil
}
