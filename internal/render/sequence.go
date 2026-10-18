package render

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"image"
	"io"
	"net/http"
	"os/exec"
	"path"
	"sort"
	"strings"

	"github.com/dockette/docshot/internal/apierr"
	"github.com/dockette/docshot/internal/detect"
	"github.com/dockette/docshot/internal/imaging"
	"github.com/dockette/docshot/internal/media"
)

// openSequence opens a converted document's items; want only matters for video (frames are sampled over the duration).
func (r *Renderer) openSequence(ctx context.Context, doc docEntry, body []byte, o Options, want int) (*imaging.Sequence, error) {
	switch doc.kind.Route {
	case detect.Image:
		return media.ImageSequence(ctx, body, doc.kind.Ext, r.cfg.MaxSide)
	case detect.Video:
		q, err := media.VideoSequence(ctx, body, doc.kind.Ext, o.At, o.AtSet, want, r.cfg.MaxSide)
		if err != nil && strings.Contains(err.Error(), "no video stream") {
			// .webm/.mka/.ogg can be audio-only
			return media.AudioSequence(ctx, body, doc.kind.Ext, doc.kind.Name, r.cfg.MaxSide)
		}
		return q, err
	case detect.Archive:
		return r.archiveSequence(ctx, body, doc.kind.Name, doc.kind.Ext)
	case detect.Audio:
		return media.AudioSequence(ctx, body, doc.kind.Ext, doc.kind.Name, r.cfg.MaxSide)
	case detect.Raw:
		img, err := media.RawPreview(ctx, body, doc.kind.Ext)
		if err != nil {
			return nil, err
		}
		return imaging.Still(img, r.cfg.MaxSide), nil
	default:
		return r.raster.PDFSequence(doc.pdf, r.cfg.MaxSide)
	}
}

const archiveMaxEntries = 200

type archiveEntry struct {
	name string
	data []byte
}

// archiveSequence previews each member through its own pipeline; nested archives and unknown members are skipped.
func (r *Renderer) archiveSequence(ctx context.Context, body []byte, name, ext string) (*imaging.Sequence, error) {
	entries, err := readArchive(ctx, body, name, ext, r.cfg.MaxBytes)
	if err != nil {
		return nil, apierr.New(http.StatusUnprocessableEntity, "reading archive: %v", err)
	}
	var keep []archiveEntry
	for _, e := range entries {
		k := detect.Detect(e.name, e.data)
		if k.Route == detect.Archive || k.Route == detect.Unknown || k.Route == detect.Video || k.Route == detect.Audio {
			continue
		}
		keep = append(keep, e)
	}
	if len(keep) == 0 {
		return nil, apierr.New(http.StatusUnsupportedMediaType, "archive has no previewable files")
	}
	return &imaging.Sequence{
		N: len(keep),
		Item: func(i int, eo imaging.Options) (image.Image, error) {
			e := keep[i]
			doc, _, err := r.document(ctx, Hash(string(e.data)), e.name, e.data)
			if err != nil {
				return imaging.Placeholder(e.name, eo), nil // one broken member must not sink the sheet
			}
			sub, err := r.openSequence(ctx, doc, e.data, Options{Options: eo}, 1)
			if err != nil {
				return imaging.Placeholder(e.name, eo), nil
			}
			defer sub.Done()
			img, err := sub.Item(0, eo)
			if err != nil {
				return imaging.Placeholder(e.name, eo), nil
			}
			return img, nil
		},
		Label: func(i int) string { return path.Base(keep[i].name) },
	}, nil
}

// readArchive caps entries and total decompressed bytes (zip-bomb guard); unpreviewable members are skipped unread.
func readArchive(ctx context.Context, body []byte, name, ext string, maxBytes int64) ([]archiveEntry, error) {
	var out []archiveEntry
	budget := maxBytes
	add := func(name string, r io.Reader, size int64) error {
		if len(out) >= archiveMaxEntries || budget <= 0 {
			return nil
		}
		base := path.Base(name)
		if strings.HasPrefix(base, ".") || strings.HasPrefix(name, "__MACOSX/") {
			return nil
		}
		ext := strings.TrimPrefix(strings.ToLower(path.Ext(base)), ".")
		if detect.ArchiveExts[ext] || detect.VideoExts[ext] || size > budget {
			return nil
		}
		data, err := io.ReadAll(io.LimitReader(r, budget+1))
		if err != nil {
			return err
		}
		if int64(len(data)) > budget { // the header lied about the size
			budget = 0
			return nil
		}
		budget -= int64(len(data))
		out = append(out, archiveEntry{name, data})
		return nil
	}
	native := bytes.HasPrefix(body, []byte("PK\x03\x04")) || isTar(body) ||
		(bytes.HasPrefix(body, []byte{0x1f, 0x8b}) && ext != "gz")
	if !native {
		if err := libarchive(ctx, body, name, ext, add); err != nil {
			return nil, err
		}
	} else if bytes.HasPrefix(body, []byte("PK\x03\x04")) {
		zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				continue
			}
			err = add(f.Name, rc, int64(f.UncompressedSize64))
			rc.Close()
			if err != nil {
				return nil, err
			}
		}
	} else {
		var r io.Reader = bytes.NewReader(body)
		if bytes.HasPrefix(body, []byte{0x1f, 0x8b}) {
			gz, err := gzip.NewReader(r)
			if err != nil {
				return nil, err
			}
			r = gz
		}
		tr := tar.NewReader(r)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			if h.Typeflag != tar.TypeReg {
				continue
			}
			if err := add(h.Name, tr, h.Size); err != nil {
				return nil, err
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

func isTar(b []byte) bool { return len(b) > 262 && string(b[257:262]) == "ustar" }

// libarchive reads what Go cannot: 7z, rar, iso, cab, tar.xz/bz2/zst via bsdtar, single compressed files via bsdcat.
func libarchive(ctx context.Context, body []byte, archiveName, ext string, add func(name string, r io.Reader, size int64) error) error {
	return media.WithTempFile(body, ext, func(file string) error {
		if detect.CompressedExts[ext] {
			cmd := exec.CommandContext(ctx, "bsdcat", file)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				return err
			}
			if err := cmd.Start(); err != nil {
				return err
			}
			err = add(singleName(archiveName, ext), stdout, 0)
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return err
		}
		list, err := exec.CommandContext(ctx, "bsdtar", "-tf", file).Output()
		if err != nil {
			return fmt.Errorf("bsdtar: %v", media.ExitMsg(err))
		}
		for _, name := range strings.Split(strings.TrimSpace(string(list)), "\n") {
			if name == "" || strings.HasSuffix(name, "/") {
				continue
			}
			cmd := exec.CommandContext(ctx, "bsdtar", "-xOf", file, name)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				return err
			}
			if err := cmd.Start(); err != nil {
				return err
			}
			err = add(name, stdout, 0)
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// singleName maps x.txt.gz to x.txt so the member is detected by its own extension.
func singleName(archive, ext string) string {
	n := strings.TrimSuffix(path.Base(archive), "."+ext)
	if n == "" || n == path.Base(archive) {
		return "content"
	}
	return n
}
