package detect

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSniffNewFormats(t *testing.T) {
	cases := map[string]string{
		"AT&TFORM\x00\x00": "djvu", "7z\xbc\xaf\x27\x1c\x00": "7z", "Rar!\x1a\x07\x00": "rar",
		"\xfd7zXZ\x00\x00": "xz", "BZh91AY&SY": "bz2", "\x28\xb5\x2f\xfd\x00": "zst", "\x1f\x8b\x08\x00": "gz",
		"ID3\x04\x00": "mp3", "fLaC\x00": "flac", "OggS\x00": "ogg", "RIFF\x00\x00\x00\x00WAVEfmt ": "wav",
		"wOF2\x00": "woff2", "wOFF\x00": "woff", "OTTO\x00": "otf",
		"BEGIN:VCALENDAR\r\nVERSION:2.0": "ics", "BEGIN:VCARD\r\n": "vcf",
		`{"cells": [], "nbformat": 4}`: "ipynb", `[{"a":1}]`: "json",
		"digraph G { a -> b }": "gv", "a,b,c\n1,2,3\n4,5,6\n7,8,9\n": "csv",
	}
	for in, want := range cases {
		if got := sniff([]byte(in)); got != want {
			t.Errorf("sniff(%q) = %q, want %q", in, got, want)
		}
	}
	if !isGraphviz([]byte("// c\nstrict digraph x {}")) || isGraphviz([]byte("\xd0\xcf\x11\xe0")) {
		t.Error("isGraphviz")
	}
}

func TestDetectOLEWithoutName(t *testing.T) {
	for file, want := range map[string]string{"report.doc": "doc", "sheet.xls": "xls"} {
		b, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Fatal(err)
		}
		if k := Detect("", b); k.Ext != want || k.Route != Office && k.Route != Sheet {
			t.Errorf("%s without a name → %s/%s, want %s", file, k.Ext, k.Route, want)
		}
	}
	if k := Detect("", []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1, 0, 0}); k.Route != Unknown {
		t.Errorf("broken OLE2 → %s, want unknown", k.Route)
	}
}

func TestDetectPostScript(t *testing.T) {
	if k := Detect("a.ps", []byte("%!PS-Adobe-3.0\n%%Pages: 1\nshowpage\n")); k.Route != Unknown {
		t.Errorf("PostScript → %s, want unknown (415), not a text listing", k.Route)
	}
	if k := Detect("", []byte("%!PS-Adobe-3.0 EPSF-3.0\n%%BoundingBox: 0 0 10 10\n")); k.Ext != "eps" || k.Route != Office {
		t.Errorf("EPS without a name → %s/%s", k.Ext, k.Route)
	}
}

func TestDetectImageBehindWrongExtension(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00")
	jpg := []byte{0xff, 0xd8, 0xff, 0xe0, 0, 0}
	bmp := append([]byte("BM\x00\x00\x00\x00\x00\x00\x00\x00\x36\x00\x00\x00"), 40, 0, 0, 0)
	for name, tc := range map[string]struct {
		body []byte
		ext  string
	}{
		"image.php": {png, "png"}, "photo.txt": {jpg, "jpg"}, "noext": {png, "png"},
		"scan.pdf": {png, "png"}, "photo.html": {jpg, "jpg"}, "pic.json": {png, "png"},
		"doc.docx": {jpg, "jpg"}, "clip.mp4": {png, "png"}, "a.csv": {bmp, "bmp"},
		"x.tif": {png, "png"},
	} {
		if k := Detect(name, tc.body); k.Ext != tc.ext || k.Route != Image {
			t.Errorf("%s → %s/%s, want %s/image", name, k.Ext, k.Route, tc.ext)
		}
	}
	if k := Detect("index.php", []byte("<?php echo 1;")); k.Route != Text {
		t.Errorf("php source → %s, want text", k.Route)
	}
	if k := Detect("notes.md", []byte("BMW service notes, long enough to pass.\n")); k.Route != Markdown {
		t.Errorf("text starting with BM → %s, want markdown", k.Route)
	}
	// RAW files carry TIFF magic and must stay RAW.
	if k := Detect("shot.nef", []byte("II*\x00\x08\x00\x00\x00")); k.Route != Raw {
		t.Errorf("nef → %s, want raw", k.Route)
	}
}
