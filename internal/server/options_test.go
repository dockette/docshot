package server

import (
	"image/color"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/dockette/docshot/internal/imaging"
	"github.com/dockette/docshot/internal/render"
	"github.com/minio/minio-go/v7"
)

func TestParseColor(t *testing.T) {
	for in, want := range map[string]color.NRGBA{
		"fff": {255, 255, 255, 255}, "#000000": {0, 0, 0, 255}, "ff000080": {255, 0, 0, 128}, "transparent": {},
	} {
		got, err := parseColor(in)
		if err != nil || got != want {
			t.Errorf("%q → %v %v, want %v", in, got, err, want)
		}
	}
	if _, err := parseColor("nope"); err == nil {
		t.Error("bad colour accepted")
	}
}

func testServer() *Server {
	presets, _ := parsePresets("tiny:width=64&format=jpeg")
	return &Server{cfg: Config{defaultDPI: 150, maxSide: 10000, presets: presets}}
}

func TestPresetsAndDPR(t *testing.T) {
	s := testServer()
	r := httptest.NewRequest("GET", "/render?preset=thumb&quality=50&dpr=2", nil)
	o, err := s.parseOpts(r, render.OpPage)
	if err != nil {
		t.Fatal(err)
	}
	// thumb = 300x400 cover top webp q80; explicit quality wins; dpr doubles the box.
	if o.Width != 600 || o.Height != 800 || o.Fit != "cover" || o.Position != "top" || o.Format != "webp" || o.Quality != 50 {
		t.Errorf("preset+dpr gave %+v", o)
	}
	if _, err := s.parseOpts(httptest.NewRequest("GET", "/render?preset=nope", nil), render.OpPage); err == nil {
		t.Error("unknown preset accepted")
	}
	s.cfg.onlyPresets = true
	if _, err := s.parseOpts(httptest.NewRequest("GET", "/render?width=10000", nil), render.OpPage); err == nil {
		t.Error("only-presets mode accepted an ad-hoc width")
	}
	if _, err := s.parseOpts(httptest.NewRequest("GET", "/render?preset=tiny&page=3", nil), render.OpPage); err != nil {
		t.Errorf("only-presets mode must still allow page: %v", err)
	}
}

func TestFormatAuto(t *testing.T) {
	s := testServer()
	r := httptest.NewRequest("GET", "/render?format=auto", nil)
	r.Header.Set("Accept", "image/avif,image/webp,*/*")
	if o, _ := s.parseOpts(r, render.OpPage); o.Format != "webp" || !o.autoFormat {
		t.Errorf("Accept with webp → %q", o.Format)
	}
	r.Header.Set("Accept", "image/png,*/*")
	if o, _ := s.parseOpts(r, render.OpPage); o.Format != "png" {
		t.Errorf("Accept without webp → %q", o.Format)
	}
}

func TestSignature(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	signed, err := SignURL(key, "/render?width=300&path=a/b.docx")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: Config{signKey: key}}
	if err := s.checkSignature(httptest.NewRequest("GET", signed, nil)); err != nil {
		t.Errorf("valid signature rejected: %v", err)
	}
	u, _ := url.Parse(signed)
	q := u.Query()
	q.Set("width", "3000") // tamper
	u.RawQuery = q.Encode()
	if err := s.checkSignature(httptest.NewRequest("GET", u.String(), nil)); err == nil {
		t.Error("tampered URL accepted")
	}
	if err := s.checkSignature(httptest.NewRequest("GET", "/render?path=a/b.docx", nil)); err == nil {
		t.Error("unsigned URL accepted")
	}
}

func TestPixelCapParams(t *testing.T) {
	old := imaging.MaxPixels
	imaging.MaxPixels = 1_000_000
	defer func() { imaging.MaxPixels = old }()
	s := &Server{cfg: Config{defaultDPI: 150, maxSide: 10000}}
	if _, err := s.parseOpts(httptest.NewRequest("GET", "/render?width=2000&height=2000", nil), render.OpPage); err == nil {
		t.Error("4 MP box accepted under a 1 MP cap")
	}
}

func dummyS3(t *testing.T) *minio.Client {
	c, err := minio.New("127.0.0.1:1", &minio.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
