// Package htmlconv builds self-contained HTML pages from documents for Chromium.
package htmlconv

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"regexp"
	"strings"

	"golang.org/x/text/encoding/htmlindex"
)

// EmailToHTML renders an RFC 822 message with cid: images inlined as data: URIs (offline rendering);
// webArchive (mht) skips the header block and resolves images by Content-Location.
func EmailToHTML(raw []byte, webArchive bool) (string, error) {
	// mbox: first message only, minus its "From " separator line.
	if bytes.HasPrefix(raw, []byte("From ")) {
		if i := bytes.IndexByte(raw, '\n'); i >= 0 {
			raw = raw[i+1:]
		}
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	p := &mailParts{inline: map[string]string{}, located: map[string]string{}}
	if err := p.walk(textproto.MIMEHeader(msg.Header), msg.Body, 0); err != nil {
		return "", err
	}

	dec := &mime.WordDecoder{CharsetReader: charsetReader}
	hdr := func(name string) string {
		v := msg.Header.Get(name)
		if d, err := dec.DecodeHeader(v); err == nil {
			return d
		}
		return v
	}

	return mailPage(hdr, p, webArchive), nil
}

// mailPage is shared by .eml/.mbox/.mht and .msg.
func mailPage(hdr func(string) string, p *mailParts, webArchive bool) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><head><meta charset="utf-8"><style>` + baseCSS + `
.mailhdr { border-bottom: 2px solid #444; margin-bottom: 1em; padding-bottom: .5em; font-size: 10pt; }
.mailhdr td { border: 0; padding: 1px 8px 1px 0; } .mailhdr td:first-child { color: #555; white-space: nowrap; }
.mailhdr .subj { font-size: 14pt; font-weight: bold; margin-bottom: .4em; }
</style></head><body>`)
	if !webArchive {
		writeMailHeader(&b, hdr, p.attachments)
	}
	switch {
	case p.html != "":
		b.WriteString(p.rewriteRefs(bodyOnly(p.html)))
	case p.text != "":
		b.WriteString(`<pre>` + html.EscapeString(p.text) + `</pre>`)
	}
	b.WriteString(`</body></html>`)
	return b.String()
}

func writeMailHeader(b *strings.Builder, hdr func(string) string, attachments []string) {
	b.WriteString(`<div class="mailhdr">`)
	fmt.Fprintf(b, `<div class="subj">%s</div><table>`, html.EscapeString(hdr("Subject")))
	for _, h := range []string{"From", "To", "Cc", "Date"} {
		if v := hdr(h); v != "" {
			fmt.Fprintf(b, `<tr><td>%s:</td><td>%s</td></tr>`, h, html.EscapeString(v))
		}
	}
	if len(attachments) > 0 {
		fmt.Fprintf(b, `<tr><td>Attachments:</td><td>%s</td></tr>`, html.EscapeString(strings.Join(attachments, ", ")))
	}
	b.WriteString(`</table></div>`)
}

type mailParts struct {
	html, text  string
	inline      map[string]string // Content-ID → data: URI
	located     map[string]string // Content-Location → data: URI (mht web archives)
	attachments []string
}

func (p *mailParts) walk(h textproto.MIMEHeader, body io.Reader, depth int) error {
	if depth > 10 {
		return nil
	}
	ct := h.Get("Content-Type")
	if ct == "" {
		ct = "text/plain; charset=us-ascii"
	}
	mt, params, err := mime.ParseMediaType(ct)
	if err != nil {
		mt, params = "text/plain", map[string]string{}
	}

	if strings.HasPrefix(mt, "multipart/") {
		mr := multipart.NewReader(body, params["boundary"])
		for {
			part, err := mr.NextRawPart() // raw: we decode transfer-encoding ourselves, uniformly
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return nil // a truncated multipart still renders what we got so far
			}
			if err := p.walk(part.Header, part, depth+1); err != nil {
				return err
			}
		}
	}

	data, err := io.ReadAll(transferDecode(h.Get("Content-Transfer-Encoding"), body))
	if err != nil {
		return nil
	}
	disp, dparams, _ := mime.ParseMediaType(h.Get("Content-Disposition"))
	filename := dparams["filename"]
	if filename == "" {
		filename = params["name"]
	}
	cid := strings.Trim(h.Get("Content-Id"), "<> ")

	switch {
	case mt == "text/html" && disp != "attachment" && p.html == "":
		p.html = decodeCharset(data, params["charset"])
	case mt == "text/plain" && disp != "attachment" && p.text == "":
		p.text = decodeCharset(data, params["charset"])
	case strings.HasPrefix(mt, "image/") && (cid != "" || h.Get("Content-Location") != ""):
		uri := "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(data)
		if cid != "" {
			p.inline[cid] = uri
		}
		if loc := h.Get("Content-Location"); loc != "" {
			p.located[loc] = uri
		}
		if disp == "attachment" && filename != "" {
			p.attachments = append(p.attachments, filename)
		}
	case mt == "message/rfc822":
		p.attachments = append(p.attachments, cmp.Or(filename, "forwarded message"))
	default:
		if filename != "" {
			p.attachments = append(p.attachments, filename)
		}
	}
	return nil
}

func transferDecode(enc string, r io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(enc)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, &stripWS{r: r})
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	}
	return r
}

// stripWS drops CR/LF/space so line-wrapped base64 decodes.
type stripWS struct{ r io.Reader }

func (s *stripWS) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	j := 0
	for _, c := range p[:n] {
		if c != '\r' && c != '\n' && c != ' ' && c != '\t' {
			p[j] = c
			j++
		}
	}
	return j, err
}

func charsetReader(charset string, input io.Reader) (io.Reader, error) {
	enc, err := htmlindex.Get(charset)
	if err != nil {
		return nil, err
	}
	return enc.NewDecoder().Reader(input), nil
}

func decodeCharset(b []byte, charset string) string {
	if charset == "" || strings.EqualFold(charset, "utf-8") || strings.EqualFold(charset, "us-ascii") {
		return DecodeText(b)
	}
	if enc, err := htmlindex.Get(charset); err == nil {
		if out, err := enc.NewDecoder().Bytes(b); err == nil {
			return string(out)
		}
	}
	return DecodeText(b)
}

var bodyRe = regexp.MustCompile(`(?is)<body[^>]*>(.*)</body>`)

var headStyleRe = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)

// bodyOnly keeps <style> blocks and <body> content for embedding without a second <html> document.
func bodyOnly(page string) string {
	m := bodyRe.FindStringSubmatch(page)
	if m == nil {
		return page
	}
	styles := strings.Join(headStyleRe.FindAllString(page[:strings.Index(page, m[0])], -1), "")
	return styles + m[1]
}

var cidRe = regexp.MustCompile(`(?i)cid:([^"'\s>)]+)`)

func (p *mailParts) rewriteRefs(page string) string {
	for loc, uri := range p.located {
		page = strings.ReplaceAll(page, `"`+loc+`"`, `"`+uri+`"`)
	}
	return cidRe.ReplaceAllStringFunc(page, func(m string) string {
		if uri, ok := p.inline[m[4:]]; ok {
			return uri
		}
		return m
	})
}
