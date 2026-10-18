package media

import (
	"bytes"
	"context"
	"image"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dockette/docshot/internal/apierr"
)

// toolPDF runs a converter via a temp file (tools only read files); args may use {in} and {out}.
func toolPDF(ctx context.Context, body []byte, ext string, args ...string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "docshot-tool-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	in := filepath.Join(dir, "in"+tempExt(ext))
	out := filepath.Join(dir, "out.pdf")
	if err := os.WriteFile(in, body, 0o600); err != nil {
		return nil, err
	}
	argv := make([]string, len(args))
	for i, a := range args {
		argv[i] = strings.NewReplacer("{in}", in, "{out}", out).Replace(a)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		return nil, apierr.New(http.StatusUnprocessableEntity, "%s: %s", filepath.Base(argv[0]), firstLine(stderr.String(), err))
	}
	pdf, err := os.ReadFile(out)
	if err != nil || len(pdf) == 0 {
		return nil, apierr.New(http.StatusUnprocessableEntity, "%s produced no output", filepath.Base(argv[0]))
	}
	return pdf, nil
}

func firstLine(s string, err error) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return err.Error()
	}
	return strings.SplitN(s, "\n", 2)[0]
}

// TIFFToPDF keeps every page of a multipage TIFF; callers fall back to the image route if tiff2pdf rejects it.
func TIFFToPDF(ctx context.Context, body []byte) ([]byte, error) {
	return toolPDF(ctx, body, "tif", "tiff2pdf", "-o", "{out}", "{in}")
}

// DjVuToPDF renders every page into one raster PDF.
func DjVuToPDF(ctx context.Context, body []byte) ([]byte, error) {
	return toolPDF(ctx, body, "djvu", "ddjvu", "-format=pdf", "-quality=85", "{in}", "{out}")
}

// XPSToPDF converts XPS/OpenXPS to a vector PDF.
func XPSToPDF(ctx context.Context, body []byte, ext string) ([]byte, error) {
	return toolPDF(ctx, body, ext, "xpstopdf", "{in}", "{out}")
}

// GraphvizToPDF lays the graph out as a vector PDF.
func GraphvizToPDF(ctx context.Context, body []byte) ([]byte, error) {
	return toolPDF(ctx, body, "gv", "dot", "-Tpdf", "-o", "{out}", "{in}")
}

// RawPreview extracts the embedded JPEG preview (simple_dcraw -e); without one LibRaw develops at half size.
func RawPreview(ctx context.Context, body []byte, ext string) (image.Image, error) {
	dir, err := os.MkdirTemp("", "docshot-raw-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	in := filepath.Join(dir, "in"+tempExt(ext))
	if err := os.WriteFile(in, body, 0o600); err != nil {
		return nil, err
	}
	if err := exec.CommandContext(ctx, "simple_dcraw", "-e", in).Run(); err == nil {
		for _, cand := range []string{in + ".thumb.jpg", in + ".thumb.ppm"} {
			if b, err := os.ReadFile(cand); err == nil && len(b) > 0 {
				if img, err := decodeAny(ctx, b, filepath.Ext(cand)[1:]); err == nil {
					return img, nil
				}
			}
		}
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "dcraw_emu", "-h", "-w", "-T", in) // half size, camera white balance, TIFF
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, apierr.New(http.StatusUnprocessableEntity, "not a readable camera RAW: %s", firstLine(stderr.String(), err))
	}
	b, err := os.ReadFile(in + ".tiff")
	if err != nil {
		return nil, apierr.New(http.StatusUnprocessableEntity, "LibRaw produced no image")
	}
	return decodeAny(ctx, b, "tiff")
}

// decodeAny decodes with Go when it can, else with ffmpeg.
func decodeAny(ctx context.Context, b []byte, ext string) (image.Image, error) {
	if img, _, err := image.Decode(bytes.NewReader(b)); err == nil {
		return img, nil
	}
	return ffmpegImage(ctx, b, ext)
}
