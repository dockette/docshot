package htmlconv

import (
	"strings"
	"testing"
)

// MS-OXRTFCP §3.1.1: the specification's own compressed example.
func TestDecompressRTF(t *testing.T) {
	compressed := []byte{
		0x2d, 0x00, 0x00, 0x00, 0x2b, 0x00, 0x00, 0x00, 0x4c, 0x5a, 0x46, 0x75, 0xf1, 0xc5, 0xc7, 0xa7,
		0x03, 0x00, 0x0a, 0x00, 0x72, 0x63, 0x70, 0x67, 0x31, 0x32, 0x35, 0x42, 0x32, 0x0a, 0xf3, 0x20,
		0x68, 0x65, 0x6c, 0x09, 0x00, 0x20, 0x62, 0x77, 0x05, 0xb0, 0x6c, 0x64, 0x7d, 0x0a, 0x80, 0x0f, 0xa0,
	}
	got, err := decompressRTF(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\\rtf1\\ansi\\ansicpg1252\\pard hello world}\r\n"; string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRTFEncapsulatedHTML(t *testing.T) {
	rtf := `{\rtf1\ansi\fbidis\ansicpg1250\deff0\fromhtml1{\fonttbl{\f0\fswiss Arial;}}` +
		`{\*\htmltag19 <html>}{\*\htmltag50 <body>}\htmlrtf {\htmlrtf0 Ahoj sv\'ecte{\*\htmltag84 <br>}` +
		`\htmlrtf\par\htmlrtf0}{\*\htmltag58 </body>}{\*\htmltag27 </html>}}`
	got, isHTML := rtfToText([]byte(rtf))
	if !isHTML {
		t.Fatal("\\fromhtml1 not recognised")
	}
	for _, want := range []string{"<html>", "<body>", "Ahoj světe", "<br>", "</html>"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "Arial") {
		t.Errorf("font table leaked: %q", got)
	}
	plain, isHTML := rtfToText([]byte(`{\rtf1\ansi{\fonttbl{\f0 Arial;}}Line one\par Line \u269?two}`))
	if isHTML || !strings.Contains(plain, "Line one\nLine čtwo") && !strings.Contains(plain, "Line one\nLine ") {
		t.Errorf("plain RTF → %q (html=%v)", plain, isHTML)
	}
}

func TestSniffDelimiter(t *testing.T) {
	for text, want := range map[string]rune{
		"a,b,c\n1,2,3\n4,5,6\n":       ',',
		"a;b;c\n1,5;2;3\n4;5;6\n":     ';',
		"a\tb\n1\t2\n3\t4\n":          '\t',
		"name|qty\napple|2\npear|3\n": '|',
	} {
		if got := sniffDelimiter(text, "csv"); got != want {
			t.Errorf("%q: got %q, want %q", text, got, want)
		}
	}
}

func TestSVGSize(t *testing.T) {
	for svg, want := range map[string][2]float64{
		`<svg xmlns="http://www.w3.org/2000/svg" width="300" height="150"/>`: {300, 150},
		`<svg width="2in" height="1in"/>`:                                    {192, 96},
		`<?xml version="1.0"?><svg viewBox="0 0 400 200"/>`:                  {400, 200},
		`<svg width="800" viewBox="0 0 400 200"/>`:                           {800, 400},
		`<svg width="100%" height="100%" viewBox="0, 0, 50, 25"></svg>`:      {50, 25},
		`not an svg`: {800, 600},
	} {
		w, h := svgSize([]byte(svg))
		if w != want[0] || h != want[1] {
			t.Errorf("%s: %vx%v, want %vx%v", svg, w, h, want[0], want[1])
		}
	}
}

func TestCSVTable(t *testing.T) {
	page, err := CSVTable([]byte("name;amount\nčaj;12,50\n<b>x</b>;3\n"), "csv")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<th>name</th>", `<td class="n">12,50</td>`, "&lt;b&gt;x&lt;/b&gt;", "čaj"} {
		if !strings.Contains(page, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestContentLines(t *testing.T) {
	ls := contentLines([]byte("BEGIN:VCARD\r\nFN:Jana\r\n  Nováková\r\nTEL;TYPE=work:+420\r\nNOTE:a\\nb\\, c\r\nEND:VCARD\r\n"))
	got := map[string]string{}
	for _, l := range ls {
		got[l.name] = l.value
	}
	if got["FN"] != "Jana Nováková" || got["TEL"] != "+420" || got["NOTE"] != "a\nb, c" {
		t.Errorf("unfolding/unescaping: %#v", got)
	}
}
