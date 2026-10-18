package htmlconv

import (
	"bytes"
	"html"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// maxTextBytes caps text handed to Chromium (256 KiB ~ 80 pages); ~1 MB of long-lined XML crashed it.
const maxTextBytes = 256 << 10

func CapText(t string) string {
	if len(t) <= maxTextBytes {
		return t
	}
	cut := maxTextBytes
	for cut > 0 && !utf8.RuneStart(t[cut]) {
		cut--
	}
	return t[:cut] + "\n\n[… truncated by docshot: the rest is not rendered]"
}

// DecodeText returns UTF-8; invalid UTF-8 is assumed Windows-1250 (Czech/Slovak legacy).
func DecodeText(b []byte) string {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	if utf8.Valid(b) {
		return string(b)
	}
	if out, err := charmap.Windows1250.NewDecoder().Bytes(b); err == nil {
		return string(out)
	}
	return string(b)
}

// EnsureCharset adds <meta charset> when missing; Chromium would guess Latin-1.
func EnsureCharset(page string) string {
	l := strings.ToLower(page[:min(len(page), 4096)])
	if strings.Contains(l, "charset") {
		return page
	}
	return `<meta charset="utf-8">` + page
}

const baseCSS = `
@page { size: A4; }
body { font-family: "DejaVu Sans", "Noto Sans", "Liberation Sans", sans-serif; font-size: 11pt; line-height: 1.45; color: #111; }
pre, code { font-family: "DejaVu Sans Mono", "Liberation Mono", monospace; font-size: 9pt; }
pre { white-space: pre-wrap; word-break: break-word; }
table { border-collapse: collapse; } td, th { border: 1px solid #bbb; padding: 3px 6px; vertical-align: top; }
img { max-width: 100%; }
`

const MarkdownTemplate = `<!doctype html>
<html><head><meta charset="utf-8"><style>` + baseCSS + `
h1, h2, h3 { line-height: 1.2; } blockquote { border-left: 3px solid #ccc; margin-left: 0; padding-left: 1em; color: #444; }
</style></head><body>{{ toHTML "document.md" }}</body></html>`

func TextPage(text string) string {
	return `<!doctype html><html><head><meta charset="utf-8"><style>` + baseCSS +
		`</style></head><body><pre>` + html.EscapeString(hardWrap(text, 200)) + `</pre></body></html>`
}

// hardWrap breaks lines over n runes; Chromium takes tens of seconds on 40 000-char lines.
func hardWrap(text string, n int) string {
	var b strings.Builder
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		r := []rune(line)
		for len(r) > n {
			b.WriteString(string(r[:n]))
			b.WriteByte('\n')
			r = r[n:]
		}
		b.WriteString(string(r))
	}
	return b.String()
}
