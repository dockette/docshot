package server

import (
	"testing"

	"github.com/dockette/docshot/internal/imaging"
	"github.com/dockette/docshot/internal/render"
)

func TestVariant(t *testing.T) {
	def := render.Options{Options: imaging.Options{DPI: 150, Format: "png", Quality: 85, Fit: "contain", Position: "center"}, Op: render.OpPage, Page: 1, Labels: true, Limit: defaultLimit}
	with := func(f func(*render.Options)) render.Options { o := def; f(&o); return o }
	cases := map[string]render.Options{
		"":                      def,
		".p2":                   with(func(o *render.Options) { o.Page = 2 }),
		".p1-d300":              with(func(o *render.Options) { o.DPI = 300 }),
		".p1-w300":              with(func(o *render.Options) { o.Width = 300; o.Format = "jpeg" }),
		".p1-300x200":           with(func(o *render.Options) { o.Width, o.Height = 300, 200 }),
		".p3-480x270-cover-top": with(func(o *render.Options) { o.Page, o.Width, o.Height, o.Fit, o.Position = 3, 480, 270, "cover", "top" }),
		".p1-q60":               with(func(o *render.Options) { o.Format, o.Quality = "jpeg", 60 }),
		".p1-4x2":               with(func(o *render.Options) { o.Op, o.GridCols, o.GridRows = render.OpGrid, 4, 2 }),
		".p1-l50":               with(func(o *render.Options) { o.Op, o.Limit = render.OpPages, 50 }),
		".p1-at12.5s":           with(func(o *render.Options) { o.At, o.AtSet = 12.5, true }),
	}
	for want, o := range cases {
		if got := variant(o, 150); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestStoreTargets(t *testing.T) {
	s := &Server{cfg: Config{defaultDPI: 150, storeBucket: "previews", storePrefix: "docshot/"}, s3: dummyS3(t)}
	page := options{Options: render.Options{Options: imaging.Options{DPI: 150, Format: "png", Quality: 85, Fit: "contain", Position: "center"}, Op: render.OpPage, Page: 1, Labels: true, Limit: defaultLimit}}
	grid, pages := page, page
	grid.Op, pages.Op = render.OpGrid, render.OpPages
	src := input{s3: &s3Ref{bucket: "docs", key: "smlouvy/a.docx", etag: "e1"}}
	for _, c := range []struct {
		strategy string
		o        options
		want     string
	}{
		{"append", page, "s3://docs/smlouvy/a.docx.png"},
		{"append", grid, "s3://docs/smlouvy/a.docx.grid.png"},
		{"append", pages, "s3://docs/smlouvy/a.docx.pages.zip"},
		{"replace", page, "s3://docs/smlouvy/a.png"},
		{"suffix", page, "s3://docs/smlouvy/a_preview.png"},
		{"suffix", grid, "s3://docs/smlouvy/a_grid.png"},
		{"suffix", pages, "s3://docs/smlouvy/a_pages.zip"},
		{"hash", page, "s3://docs/docshot/_hash/ab/abcdef.png"},
	} {
		c.o.store, c.o.storeStrategy = true, c.strategy
		tg, ok, err := s.storeTarget(src, c.o, "abcdef")
		if err != nil || !ok || tg.String() != c.want {
			t.Errorf("%s/%s: %v %v %q, want %q", c.strategy, c.o.Op, ok, err, tg.String(), c.want)
		}
		if c.strategy != "hash" && tg.tag != "etag:e1" {
			t.Errorf("S3 source freshness tag %q", tg.tag)
		}
	}
	// Non-S3 source: the store bucket + prefix, freshness by content hash.
	up := input{storeBase: "report.docx"}
	o := page
	o.store, o.storeStrategy = true, "append"
	if tg, ok, _ := s.storeTarget(up, o, ""); ok {
		t.Errorf("upload resolved before its content hash was known: %v", tg)
	}
	tg, ok, err := s.storeTarget(up, o, "abcdef")
	if err != nil || !ok || tg.String() != "s3://previews/docshot/report.docx.png" || tg.tag != "sha:abcdef" {
		t.Errorf("upload target %v %v %q %q", ok, err, tg.String(), tg.tag)
	}
	// No filename → hash, whatever was asked.
	tg, _, _ = s.storeTarget(input{}, o, "abcdef")
	if tg.String() != "s3://previews/docshot/_hash/ab/abcdef.png" {
		t.Errorf("nameless upload %q", tg.String())
	}
}
