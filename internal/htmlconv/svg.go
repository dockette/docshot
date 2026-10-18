package htmlconv

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// SVGPage wraps an SVG in a page of exactly its size for Chromium (better SVG support than LibreOffice) to print as a vector PDF.
func SVGPage(body []byte) (page string, wIn, hIn float64) {
	w, h := svgSize(body)
	page = `<!doctype html><html><head><meta charset="utf-8"><style>
@page { size: ` + ftoa(w) + `px ` + ftoa(h) + `px; margin: 0 }
html, body { margin: 0; padding: 0; background: transparent }
svg { display: block; width: ` + ftoa(w) + `px; height: ` + ftoa(h) + `px }
</style></head><body>` + stripXMLProlog(DecodeText(body)) + `</body></html>`
	return page, w / 96, h / 96
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', 2, 64) }

var prologRe = regexp.MustCompile(`(?is)^\s*(<\?xml[^>]*\?>|<!DOCTYPE[^>]*>|<!--.*?-->|\s)*`)

func stripXMLProlog(s string) string { return prologRe.ReplaceAllString(s, "") }

// svgSize reads width/height (px, pt, mm, cm, in) or falls back to the viewBox.
func svgSize(b []byte) (float64, float64) {
	var root struct {
		Width   string `xml:"width,attr"`
		Height  string `xml:"height,attr"`
		ViewBox string `xml:"viewBox,attr"`
	}
	d := xml.NewDecoder(bytes.NewReader(b))
	d.Strict = false
	for {
		t, err := d.Token()
		if err != nil {
			return 800, 600
		}
		if se, ok := t.(xml.StartElement); ok && se.Name.Local == "svg" {
			_ = d.DecodeElement(&root, &se)
			break
		}
	}
	w, h := cssLength(root.Width), cssLength(root.Height)
	var vb [4]float64
	if n, _ := fmt.Sscanf(strings.ReplaceAll(root.ViewBox, ",", " "), "%g %g %g %g", &vb[0], &vb[1], &vb[2], &vb[3]); n == 4 && vb[2] > 0 && vb[3] > 0 {
		switch {
		case w <= 0 && h <= 0:
			w, h = vb[2], vb[3]
		case w <= 0:
			w = h * vb[2] / vb[3]
		case h <= 0:
			h = w * vb[3] / vb[2]
		}
	}
	if w <= 0 || h <= 0 {
		return 800, 600
	}
	// Keep the page sane: tiny icons get scaled by PDFium anyway.
	if w > 20000 || h > 20000 {
		s := 20000 / max(w, h)
		w, h = w*s, h*s
	}
	return w, h
}

func cssLength(v string) float64 {
	v = strings.TrimSpace(v)
	if v == "" || strings.HasSuffix(v, "%") {
		return 0
	}
	units := map[string]float64{"px": 1, "pt": 96.0 / 72, "pc": 16, "mm": 96 / 25.4, "cm": 96 / 2.54, "in": 96, "em": 16, "ex": 8}
	for u, f := range units {
		if strings.HasSuffix(v, u) {
			n, _ := strconv.ParseFloat(strings.TrimSuffix(v, u), 64)
			return n * f
		}
	}
	n, _ := strconv.ParseFloat(v, 64)
	return n
}
