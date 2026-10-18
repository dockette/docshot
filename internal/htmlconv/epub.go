package htmlconv

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"io"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strings"

	"github.com/dockette/docshot/internal/apierr"
)

// EPUBToHTML flattens an EPUB into one page (LibreOffice rejects EPUB); images are inlined as data: URIs, the backend has no network.
func EPUBToHTML(b []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return "", err
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	read := func(name string) ([]byte, bool) {
		f, ok := files[name]
		if !ok {
			return nil, false
		}
		rc, err := f.Open()
		if err != nil {
			return nil, false
		}
		defer rc.Close()
		data, err := io.ReadAll(io.LimitReader(rc, 64<<20))
		return data, err == nil
	}

	var container struct {
		Rootfiles []struct {
			Path string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	raw, ok := read("META-INF/container.xml")
	if !ok || xml.Unmarshal(raw, &container) != nil || len(container.Rootfiles) == 0 {
		return "", apierr.New(http.StatusUnprocessableEntity, "not an EPUB: META-INF/container.xml missing or invalid")
	}
	opfPath := container.Rootfiles[0].Path

	var opf struct {
		Title    string `xml:"metadata>title"`
		Manifest []struct {
			ID   string `xml:"id,attr"`
			Href string `xml:"href,attr"`
		} `xml:"manifest>item"`
		Spine []struct {
			IDRef string `xml:"idref,attr"`
		} `xml:"spine>itemref"`
	}
	raw, ok = read(opfPath)
	if !ok || xml.Unmarshal(raw, &opf) != nil {
		return "", apierr.New(http.StatusUnprocessableEntity, "EPUB package document %s unreadable", opfPath)
	}
	hrefs := map[string]string{}
	for _, it := range opf.Manifest {
		hrefs[it.ID] = path.Join(path.Dir(opfPath), it.Href)
	}

	var page strings.Builder
	page.WriteString(`<!doctype html><html><head><meta charset="utf-8"><style>` + baseCSS +
		`.chapter + .chapter { break-before: page; }</style></head><body>`)
	for _, ref := range opf.Spine {
		name, ok := hrefs[ref.IDRef]
		if !ok {
			continue
		}
		data, ok := read(name)
		if !ok {
			continue
		}
		chapter := inlineImages(bodyOnly(DecodeText(data)), path.Dir(name), read)
		page.WriteString(`<div class="chapter">` + chapter + `</div>`)
	}
	page.WriteString(`</body></html>`)
	return page.String(), nil
}

var srcRe = regexp.MustCompile(`(?i)((?:src|xlink:href|href)\s*=\s*["'])([^"'#]+\.(?:png|jpe?g|gif|svg|webp))(["'])`)

func inlineImages(chapter, dir string, read func(string) ([]byte, bool)) string {
	return srcRe.ReplaceAllStringFunc(chapter, func(m string) string {
		sub := srcRe.FindStringSubmatch(m)
		data, ok := read(path.Join(dir, sub[2]))
		if !ok {
			return m
		}
		mt := mime.TypeByExtension(path.Ext(sub[2]))
		if mt == "" {
			mt = "application/octet-stream"
		}
		return sub[1] + "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(data) + sub[3]
	})
}
