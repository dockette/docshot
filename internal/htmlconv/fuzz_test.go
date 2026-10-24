package htmlconv

import (
	"archive/zip"
	"bytes"
	"testing"
)

// The parsers read untrusted uploads: errors are fine, panics and hangs are not.

func FuzzEmail(f *testing.F) {
	f.Add([]byte("From: a@b\r\nSubject: =?utf-8?q?hi?=\r\nContent-Type: multipart/mixed; boundary=x\r\n\r\n--x\r\nContent-Type: text/html\r\nContent-Transfer-Encoding: base64\r\n\r\nPGI+aGk8L2I+\r\n--x--\r\n"), false)
	f.Add([]byte("MIME-Version: 1.0\r\nContent-Type: multipart/related; boundary=y\r\n\r\n--y\r\nContent-Location: a.html\r\n\r\n<img src=b.png>\r\n--y--"), true)
	f.Fuzz(func(t *testing.T, raw []byte, webArchive bool) {
		_, _ = EmailToHTML(raw, webArchive)
	})
}

func FuzzMsg(f *testing.F) {
	f.Add([]byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = MsgToHTML(raw)
	})
}

func FuzzRTF(f *testing.F) {
	f.Add([]byte{0x2d, 0x00, 0x00, 0x00, 0x2b, 0x00, 0x00, 0x00, 0x4c, 0x5a, 0x46, 0x75, 0xf1, 0xc5, 0xc7, 0xa7, 0x03, 0x00, 0x0a, 0x00})
	f.Fuzz(func(t *testing.T, raw []byte) {
		if b, err := decompressRTF(raw); err == nil {
			_, _ = rtfToText(b)
		}
	})
}

func FuzzEPUB(f *testing.F) {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for name, body := range map[string]string{
		"META-INF/container.xml": `<container><rootfiles><rootfile full-path="c.opf"/></rootfiles></container>`,
		"c.opf":                  `<package><manifest><item id="a" href="a.xhtml"/></manifest><spine><itemref idref="a"/></spine></package>`,
		"a.xhtml":                `<html><body><p>hi</p><img src="i.png"/></body></html>`,
	} {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	f.Add(b.Bytes())
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = EPUBToHTML(raw)
	})
}

func FuzzStructured(f *testing.F) {
	f.Add([]byte(`{"cells":[{"cell_type":"markdown","source":["# a"]}],"nbformat":4}`), []byte("BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nSUMMARY:a\r\nEND:VEVENT\r\nEND:VCALENDAR"), []byte("a;b\n1;2\n"))
	f.Fuzz(func(t *testing.T, nb, cal, csv []byte) {
		_, _ = NotebookHTML(nb)
		_, _ = CalendarHTML(cal)
		_, _ = ContactsHTML(cal)
		_, _ = CSVTable(csv, "csv")
	})
}
