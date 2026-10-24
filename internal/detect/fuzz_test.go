package detect

import (
	"os"
	"testing"
)

// Detect runs on every upload: it must not panic or hang on any bytes.
func FuzzDetect(f *testing.F) {
	for _, n := range []string{"testdata/report.doc", "testdata/sheet.xls"} {
		if b, err := os.ReadFile(n); err == nil {
			f.Add("", b)
		}
	}
	f.Add("a.docx", []byte("PK\x03\x04"))
	f.Add("", []byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"))
	f.Add("x.eml", []byte("From: a\r\nSubject: b\r\n\r\nhi"))
	f.Fuzz(func(t *testing.T, name string, body []byte) {
		_ = Detect(name, body)
	})
}
