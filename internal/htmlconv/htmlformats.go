package htmlconv

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/dockette/docshot/internal/apierr"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"golang.org/x/image/font/sfnt"
)

func htmlDoc(title, css, body string) string {
	return `<!doctype html><html><head><meta charset="utf-8"><title>` + html.EscapeString(title) +
		`</title><style>` + baseCSS + css + `</style></head><body>` + body + `</body></html>`
}

const maxTableRows = 5000

// CSVTable renders a delimited file as a table.
func CSVTable(b []byte, ext string) (string, error) {
	text := CapText(DecodeText(b))
	delim := sniffDelimiter(text, ext)
	r := csv.NewReader(strings.NewReader(text))
	r.Comma = delim
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	var rows [][]string
	for len(rows) <= maxTableRows {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		rows = append(rows, rec)
	}
	if len(rows) == 0 {
		return "", apierr.New(422, "empty table")
	}
	var bld strings.Builder
	bld.WriteString(`<table><thead><tr>`)
	for _, c := range rows[0] {
		bld.WriteString(`<th>` + html.EscapeString(c) + `</th>`)
	}
	bld.WriteString(`</tr></thead><tbody>`)
	for _, row := range rows[1:] {
		bld.WriteString(`<tr>`)
		for _, c := range row {
			cls := ""
			if looksNumeric(c) {
				cls = ` class="n"`
			}
			bld.WriteString(`<td` + cls + `>` + html.EscapeString(c) + `</td>`)
		}
		bld.WriteString(`</tr>`)
	}
	bld.WriteString(`</tbody></table>`)
	if len(rows) > maxTableRows {
		bld.WriteString(fmt.Sprintf(`<p class="more">… more rows not shown (first %d)</p>`, maxTableRows))
	}
	return htmlDoc("table", `
@page { size: A4 landscape; margin: 10mm }
body { font-size: 8.5pt }
table { width: 100%; border-collapse: collapse }
thead { display: table-header-group }
th { background: #e5e7eb; text-align: left; font-weight: 600 }
th, td { border: 1px solid #d1d5db; padding: 2px 5px; overflow-wrap: anywhere }
tr:nth-child(even) td { background: #f9fafb }
td.n { text-align: right; font-variant-numeric: tabular-nums; white-space: nowrap }
.more { color: #6b7280 }`, bld.String()), nil
}

func sniffDelimiter(text, ext string) rune {
	if ext == "tsv" || ext == "tab" {
		return '\t'
	}
	lines := strings.SplitN(text, "\n", 6)
	best, bestScore := ',', -1
	for _, d := range []rune{',', ';', '\t', '|'} {
		score, first := 0, -1
		for _, l := range lines[:max(len(lines)-1, 1)] {
			n := strings.Count(l, string(d))
			if first < 0 {
				first = n
			}
			if n > 0 && n == first {
				score++
			}
		}
		if first > 0 && score > bestScore {
			best, bestScore = d, score
		}
	}
	return best
}

func looksNumeric(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	digits := 0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case strings.ContainsRune(" .,-+%€$£ ", r):
		default:
			return false
		}
	}
	return digits > 0
}

// PrettyJSON re-indents JSON; unparsable input is returned as is.
func PrettyJSON(b []byte) string {
	text := DecodeText(b)
	var out bytes.Buffer
	if json.Indent(&out, []byte(text), "", "  ") == nil {
		return out.String()
	}
	return text
}

var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

func markdownHTML(src string) string {
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return "<pre>" + html.EscapeString(src) + "</pre>"
	}
	return buf.String()
}

// NotebookHTML renders an .ipynb without executing anything.
func NotebookHTML(b []byte) (string, error) {
	var nb struct {
		Cells []struct {
			Type    string          `json:"cell_type"`
			Source  json.RawMessage `json:"source"`
			Count   *int            `json:"execution_count"`
			Outputs []struct {
				Type string                     `json:"output_type"`
				Text json.RawMessage            `json:"text"`
				Data map[string]json.RawMessage `json:"data"`
				Name string                     `json:"ename"`
				Val  string                     `json:"evalue"`
			} `json:"outputs"`
		} `json:"cells"`
		Meta struct {
			Kernel struct {
				Name string `json:"display_name"`
			} `json:"kernelspec"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(b, &nb); err != nil {
		return "", apierr.New(422, "not a Jupyter notebook: %v", err)
	}
	var bld strings.Builder
	if nb.Meta.Kernel.Name != "" {
		bld.WriteString(`<div class="kernel">` + html.EscapeString(nb.Meta.Kernel.Name) + `</div>`)
	}
	for _, c := range nb.Cells {
		src := joinSource(c.Source)
		switch c.Type {
		case "markdown":
			bld.WriteString(`<div class="md">` + markdownHTML(src) + `</div>`)
		case "code":
			n := " "
			if c.Count != nil {
				n = fmt.Sprint(*c.Count)
			}
			bld.WriteString(`<div class="cell"><div class="prompt">In [` + n + `]:</div><pre class="code">` + html.EscapeString(src) + `</pre></div>`)
			for _, o := range c.Outputs {
				bld.WriteString(`<div class="out">` + notebookOutput(o.Type, o.Text, o.Data, o.Name, o.Val) + `</div>`)
			}
		default:
			bld.WriteString(`<pre class="raw">` + html.EscapeString(src) + `</pre>`)
		}
	}
	return htmlDoc("notebook", `
.kernel { color: #6b7280; font-size: 9pt; text-align: right; margin-bottom: 1em }
.cell { display: flex; gap: 8px; margin: 8px 0 2px }
.prompt { color: #1d4ed8; font-family: "DejaVu Sans Mono", monospace; font-size: 8.5pt; min-width: 52px; padding-top: 4px }
pre.code { flex: 1; margin: 0; background: #f3f4f6; border: 1px solid #e5e7eb; border-radius: 3px; padding: 6px }
.out { margin: 2px 0 8px 60px; overflow-wrap: anywhere }
.out pre { margin: 0 }
.out img { max-width: 100% }
.err { color: #b91c1c }
.md img { max-width: 100% }`, bld.String()), nil
}

func joinSource(raw json.RawMessage) string {
	var lines []string
	if json.Unmarshal(raw, &lines) == nil {
		return strings.Join(lines, "")
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func notebookOutput(typ string, text json.RawMessage, data map[string]json.RawMessage, ename, evalue string) string {
	if typ == "error" {
		return `<pre class="err">` + html.EscapeString(ename+": "+evalue) + `</pre>`
	}
	if typ == "stream" {
		return `<pre>` + html.EscapeString(joinSource(text)) + `</pre>`
	}
	for _, mt := range []string{"image/png", "image/jpeg"} {
		if v, ok := data[mt]; ok {
			return `<img src="data:` + mt + `;base64,` + strings.ReplaceAll(joinSource(v), "\n", "") + `">`
		}
	}
	if v, ok := data["image/svg+xml"]; ok {
		return `<img src="data:image/svg+xml;base64,` + base64.StdEncoding.EncodeToString([]byte(joinSource(v))) + `">`
	}
	if v, ok := data["text/html"]; ok { // pandas tables; scripts do not run (JS is off)
		return joinSource(v)
	}
	if v, ok := data["text/markdown"]; ok {
		return markdownHTML(joinSource(v))
	}
	if v, ok := data["text/plain"]; ok {
		return `<pre>` + html.EscapeString(joinSource(v)) + `</pre>`
	}
	return ""
}

// contentLines unfolds RFC 5545/6350 lines and splits "NAME;PARAMS:VALUE".
type cline struct{ name, params, value string }

func contentLines(b []byte) []cline {
	text := strings.ReplaceAll(DecodeText(b), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\n ", "")
	text = strings.ReplaceAll(text, "\n\t", "")
	var out []cline
	for _, l := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		name, params, _ := strings.Cut(k, ";")
		v = strings.NewReplacer(`\n`, "\n", `\N`, "\n", `\,`, ",", `\;`, ";", `\\`, `\`).Replace(v)
		out = append(out, cline{strings.ToUpper(strings.TrimSpace(name)), strings.ToUpper(params), v})
	}
	return out
}

// CalendarHTML renders one card per event, sorted by start.
func CalendarHTML(b []byte) (string, error) {
	type event struct{ start, end, summary, location, organizer, description string }
	var evs []event
	var cur *event
	calName := ""
	for _, l := range contentLines(b) {
		switch {
		case l.name == "BEGIN" && strings.EqualFold(l.value, "VEVENT"):
			cur = &event{}
		case l.name == "END" && strings.EqualFold(l.value, "VEVENT") && cur != nil:
			evs = append(evs, *cur)
			cur = nil
		case l.name == "X-WR-CALNAME":
			calName = l.value
		case cur != nil:
			switch l.name {
			case "DTSTART":
				cur.start = icsTime(l.value)
			case "DTEND":
				cur.end = icsTime(l.value)
			case "SUMMARY":
				cur.summary = l.value
			case "LOCATION":
				cur.location = l.value
			case "ORGANIZER":
				cur.organizer = strings.TrimPrefix(strings.TrimPrefix(l.value, "mailto:"), "MAILTO:")
			case "DESCRIPTION":
				cur.description = l.value
			}
		}
	}
	if len(evs) == 0 {
		return "", apierr.New(422, "no events in this calendar")
	}
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].start < evs[j].start })
	var bld strings.Builder
	if calName != "" {
		bld.WriteString(`<h1>` + html.EscapeString(calName) + `</h1>`)
	}
	for _, e := range evs {
		when := e.start
		if e.end != "" {
			when += " – " + e.end
		}
		bld.WriteString(`<div class="ev"><div class="when">` + html.EscapeString(when) + `</div><div class="what">` +
			html.EscapeString(cmp.Or(e.summary, "(no title)")) + `</div>`)
		for _, kv := range [][2]string{{"Where", e.location}, {"Organizer", e.organizer}} {
			if kv[1] != "" {
				bld.WriteString(`<div class="f"><span>` + kv[0] + `</span> ` + html.EscapeString(kv[1]) + `</div>`)
			}
		}
		if e.description != "" {
			bld.WriteString(`<div class="desc">` + html.EscapeString(truncate(e.description, 600)) + `</div>`)
		}
		bld.WriteString(`</div>`)
	}
	return htmlDoc("calendar", `
h1 { font-size: 16pt }
.ev { border-left: 4px solid #2563eb; background: #f8fafc; padding: 8px 12px; margin: 0 0 10px; break-inside: avoid }
.when { color: #2563eb; font-weight: 600; font-size: 10pt }
.what { font-size: 13pt; font-weight: 600; margin: 2px 0 4px }
.f { font-size: 9.5pt } .f span { color: #6b7280; display: inline-block; min-width: 70px }
.desc { white-space: pre-wrap; font-size: 9.5pt; color: #374151; margin-top: 4px }`, bld.String()), nil
}

// icsTime formats iCalendar date-times as readable text.
func icsTime(v string) string {
	for _, layout := range []string{"20060102T150405Z", "20060102T150405", "20060102"} {
		if t, err := time.Parse(layout, v); err == nil {
			if layout == "20060102" {
				return t.Format("2 Jan 2006")
			}
			s := t.Format("2 Jan 2006 15:04")
			if strings.HasSuffix(v, "Z") {
				s += " UTC"
			}
			return s
		}
	}
	return v
}

// ContactsHTML renders a card per vCard.
func ContactsHTML(b []byte) (string, error) {
	type card struct {
		fn, org, title, photo string
		rows                  [][2]string
	}
	var cards []card
	var cur *card
	for _, l := range contentLines(b) {
		switch {
		case l.name == "BEGIN" && strings.EqualFold(l.value, "VCARD"):
			cur = &card{}
		case l.name == "END" && cur != nil:
			cards = append(cards, *cur)
			cur = nil
		case cur != nil:
			label := strings.ToLower(paramType(l.params))
			switch l.name {
			case "FN":
				cur.fn = l.value
			case "N":
				if cur.fn == "" {
					p := strings.Split(l.value, ";")
					if len(p) > 1 {
						cur.fn = strings.TrimSpace(p[1] + " " + p[0])
					}
				}
			case "ORG":
				cur.org = strings.ReplaceAll(l.value, ";", ", ")
			case "TITLE":
				cur.title = l.value
			case "TEL":
				cur.rows = append(cur.rows, [2]string{cmp.Or(label, "phone"), strings.TrimPrefix(l.value, "tel:")})
			case "EMAIL":
				cur.rows = append(cur.rows, [2]string{cmp.Or(label, "e-mail"), l.value})
			case "ADR":
				cur.rows = append(cur.rows, [2]string{cmp.Or(label, "address"), strings.Trim(strings.ReplaceAll(l.value, ";", " "), " ")})
			case "URL":
				cur.rows = append(cur.rows, [2]string{"web", l.value})
			case "BDAY":
				cur.rows = append(cur.rows, [2]string{"birthday", l.value})
			case "NOTE":
				cur.rows = append(cur.rows, [2]string{"note", l.value})
			case "PHOTO":
				switch {
				case strings.HasPrefix(l.value, "data:"):
					cur.photo = l.value
				case strings.Contains(l.params, "ENCODING=B") || strings.Contains(l.params, "ENCODING=BASE64"):
					cur.photo = "data:image/jpeg;base64," + l.value
				}
			}
		}
	}
	if len(cards) == 0 {
		return "", apierr.New(422, "no contacts in this file")
	}
	var bld strings.Builder
	for _, c := range cards {
		bld.WriteString(`<div class="card">`)
		if c.photo != "" {
			bld.WriteString(`<img src="` + html.EscapeString(c.photo) + `">`)
		} else {
			bld.WriteString(`<div class="avatar">` + html.EscapeString(initials(c.fn)) + `</div>`)
		}
		bld.WriteString(`<div class="body"><div class="fn">` + html.EscapeString(cmp.Or(c.fn, "(no name)")) + `</div>`)
		if sub := strings.Join(slices.DeleteFunc([]string{c.title, c.org}, func(s string) bool { return strings.TrimSpace(s) == "" }), " · "); sub != "" {
			bld.WriteString(`<div class="sub">` + html.EscapeString(sub) + `</div>`)
		}
		bld.WriteString(`<table>`)
		for _, r := range c.rows {
			bld.WriteString(`<tr><td>` + html.EscapeString(r[0]) + `</td><td>` + html.EscapeString(r[1]) + `</td></tr>`)
		}
		bld.WriteString(`</table></div></div>`)
	}
	return htmlDoc("contacts", `
.card { display: flex; gap: 16px; border: 1px solid #e5e7eb; border-radius: 6px; padding: 14px; margin: 0 0 12px; break-inside: avoid }
.card img, .avatar { width: 84px; height: 84px; border-radius: 50%; object-fit: cover; flex: none }
.avatar { background: #dbeafe; color: #1d4ed8; font-size: 30pt; font-weight: 700; display: flex; align-items: center; justify-content: center }
.fn { font-size: 16pt; font-weight: 700 } .sub { color: #4b5563; margin-bottom: 6px }
.card td { border: 0; padding: 1px 10px 1px 0; font-size: 10pt } .card td:first-child { color: #6b7280 }`, bld.String()), nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func paramType(params string) string {
	for _, p := range strings.Split(params, ";") {
		if v, ok := strings.CutPrefix(p, "TYPE="); ok {
			return strings.Split(v, ",")[0]
		}
	}
	return ""
}

func initials(name string) string {
	var out []rune
	for _, w := range strings.Fields(name) {
		out = append(out, []rune(w)[0])
		if len(out) == 2 {
			break
		}
	}
	return strings.ToUpper(string(out))
}

// FontSpecimen shows a font as a specimen page, embedding it as a data: URI.
func FontSpecimen(b []byte, ext, fileName string) (string, error) {
	mime := map[string]string{"ttf": "font/ttf", "otf": "font/otf", "woff": "font/woff", "woff2": "font/woff2", "ttc": "font/collection"}[ext]
	if mime == "" {
		mime = "font/ttf"
	}
	family, style := fileName, ""
	if f, err := sfnt.Parse(b); err == nil { // ttf/otf: read the real names
		var buf sfnt.Buffer
		if n, err := f.Name(&buf, sfnt.NameIDFamily); err == nil && n != "" {
			family = n
		}
		if n, err := f.Name(&buf, sfnt.NameIDSubfamily); err == nil {
			style = n
		}
	}
	src := `url(data:` + mime + `;base64,` + base64.StdEncoding.EncodeToString(b) + `)`
	sample := "Příliš žluťoučký kůň úpěl ďábelské ódy. Ľúbostný ťah ôsmich dní."
	var bld strings.Builder
	bld.WriteString(`<h1>` + html.EscapeString(family) + ` <span>` + html.EscapeString(style) + `</span></h1>`)
	bld.WriteString(`<div class="f big">ABCDEFGHIJKLMNOPQRSTUVWXYZ<br>abcdefghijklmnopqrstuvwxyz<br>0123456789 &amp;@#%€$?!()[]{}</div>`)
	for _, sz := range []int{12, 18, 24, 36, 48, 72} {
		bld.WriteString(fmt.Sprintf(`<div class="w"><span>%d</span><div class="f" style="font-size:%dpx">%s</div></div>`, sz, sz, html.EscapeString(sample)))
	}
	return htmlDoc(family, `
@font-face { font-family: "specimen"; src: `+src+`; }
h1 { font-size: 22pt; margin: 0 0 12px } h1 span { color: #6b7280; font-weight: 400 }
.f { font-family: "specimen"; } .big { font-size: 30px; line-height: 1.3; margin-bottom: 18px }
.w { display: flex; gap: 12px; align-items: baseline; border-top: 1px solid #e5e7eb; padding: 6px 0; white-space: nowrap; overflow: hidden }
.w span { color: #9ca3af; font-size: 9pt; min-width: 22px }`, bld.String()), nil
}
