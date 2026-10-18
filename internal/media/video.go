package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/dockette/docshot/internal/apierr"
	"github.com/dockette/docshot/internal/imaging"
)

// ffmpeg parses untrusted input: local files only (-protocol_whitelist file), no stdin, request timeout.
type videoInfo struct {
	Duration      float64
	Width, Height int
	Codec         string
	Fps           float64
}

// WithTempFile writes body to a temp file named *.ext; ffmpeg recognises formats without magic bytes (TGA, PAM) by extension only.
func WithTempFile(body []byte, ext string, fn func(path string) error) error {
	f, err := os.CreateTemp("", "docshot-*"+tempExt(ext))
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(body); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return fn(f.Name())
}

func ProbeVideo(ctx context.Context, body []byte) (videoInfo, error) {
	var v videoInfo
	err := WithTempFile(body, "", func(path string) (err error) {
		v, err = probeFile(ctx, path)
		return err
	})
	return v, err
}

func probeFile(ctx context.Context, path string) (videoInfo, error) {
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-protocol_whitelist", "file",
		"-select_streams", "v:0", "-show_entries", "stream=codec_name,width,height,avg_frame_rate:format=duration",
		"-of", "json", path).Output()
	if err != nil {
		return videoInfo{}, apierr.New(http.StatusUnprocessableEntity, "not a readable video: %v", ExitMsg(err))
	}
	var p struct {
		Streams []struct {
			Codec  string `json:"codec_name"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
			Rate   string `json:"avg_frame_rate"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &p); err != nil || len(p.Streams) == 0 {
		return videoInfo{}, apierr.New(http.StatusUnprocessableEntity, "no video stream")
	}
	st := p.Streams[0]
	v := videoInfo{Width: st.Width, Height: st.Height, Codec: st.Codec}
	v.Duration, _ = strconv.ParseFloat(p.Format.Duration, 64)
	if v.Duration <= 0 {
		// Streamed WebM/MKV has no duration header: walk packets (-c copy), take the last timestamp.
		v.Duration = scanDuration(ctx, path)
	}
	if n, d, ok := strings.Cut(st.Rate, "/"); ok {
		num, _ := strconv.ParseFloat(n, 64)
		den, _ := strconv.ParseFloat(d, 64)
		if den > 0 {
			v.Fps = float64(int(num/den*100+0.5)) / 100
		}
	}
	return v, nil
}

var timeRe = regexp.MustCompile(`time=(\d+):(\d+):(\d+(?:\.\d+)?)`)

func scanDuration(ctx context.Context, path string) float64 {
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-hide_banner", "-protocol_whitelist", "file",
		"-i", path, "-map", "0:v:0", "-c", "copy", "-f", "null", "-")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run()
	m := timeRe.FindAllStringSubmatch(stderr.String(), -1)
	if len(m) == 0 {
		return 0
	}
	last := m[len(m)-1]
	h, _ := strconv.ParseFloat(last[1], 64)
	mi, _ := strconv.ParseFloat(last[2], 64)
	sec, _ := strconv.ParseFloat(last[3], 64)
	return h*3600 + mi*60 + sec
}

// frameAt decodes one frame at t seconds; -ss before -i is the fast seek, still frame-exact.
func frameAt(ctx context.Context, path string, t float64, maxW int) (image.Image, error) {
	out, err := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-protocol_whitelist", "file",
		"-ss", strconv.FormatFloat(t, 'f', 3, 64), "-i", path,
		"-frames:v", "1", "-an", "-sn", "-dn",
		"-vf", fmt.Sprintf("scale='min(iw,%d)':-2", maxW),
		"-f", "image2pipe", "-c:v", "png", "-").Output()
	if err != nil {
		return nil, apierr.New(http.StatusUnprocessableEntity, "ffmpeg frame at %.2fs: %v", t, ExitMsg(err))
	}
	if len(out) == 0 {
		return nil, apierr.New(http.StatusUnprocessableEntity, "no frame at %.2fs", t)
	}
	return decodePNG(out)
}

func decodePNG(b []byte) (image.Image, error) { return png.Decode(bytes.NewReader(b)) }

func ExitMsg(err error) string {
	if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
		return strings.TrimSpace(string(ee.Stderr[:min(len(ee.Stderr), 300)]))
	}
	return err.Error()
}

// VideoSequence samples one frame (t= or 10 % in) or `want` frames from the middle of equal slices; the temp file lives until done().
func VideoSequence(ctx context.Context, body []byte, ext string, at float64, atSet bool, want int, maxSide int) (*imaging.Sequence, error) {
	f, err := os.CreateTemp("", "docshot-*"+tempExt(ext))
	if err != nil {
		return nil, err
	}
	cleanup := func() { os.Remove(f.Name()) }
	if _, err := f.Write(body); err != nil {
		f.Close()
		cleanup()
		return nil, err
	}
	f.Close()
	v, err := probeFile(ctx, f.Name())
	if err != nil {
		cleanup()
		return nil, err
	}

	var times []float64
	switch {
	case atSet:
		if v.Duration > 0 && at > v.Duration {
			cleanup()
			return nil, apierr.New(http.StatusRequestedRangeNotSatisfiable, "t=%.2f is past the end (%.2f s)", at, v.Duration)
		}
		times = []float64{at}
	case want <= 1:
		times = []float64{v.Duration * 0.1} // past intros and black lead-in frames
	default:
		for i := 0; i < want; i++ {
			times = append(times, (float64(i)+0.5)*v.Duration/float64(want))
		}
	}

	return &imaging.Sequence{
		N:        len(times),
		Parallel: true, // each frame is its own ffmpeg run
		Dark:     true,
		Item: func(i int, eo imaging.Options) (image.Image, error) {
			w := maxSide
			if eo.Width > 0 {
				w = eo.Width
			}
			img, err := frameAt(ctx, f.Name(), times[i], w)
			if err != nil {
				return nil, err
			}
			if eo.Trim > 0 {
				img = imaging.Trim(img, eo.Trim*255/100)
			}
			return imaging.Fit(img, eo, maxSide), nil
		},
		Label:    func(i int) string { return clock(times[i]) },
		Close:    cleanup,
		Duration: v.Duration,
	}, nil
}

func clock(t float64) string {
	s := int(t + 0.5)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// ffmpegImage decodes stills Go cannot, after an ffprobe size check (decompression bomb).
func ffmpegImage(ctx context.Context, body []byte, ext string) (image.Image, error) {
	var img image.Image
	err := WithTempFile(body, ext, func(path string) error {
		out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-protocol_whitelist", "file",
			"-select_streams", "v:0", "-show_entries", "stream=width,height", "-of", "csv=p=0:s=x", path).Output()
		if err != nil {
			return apierr.New(http.StatusUnprocessableEntity, "not a readable image: %v", ExitMsg(err))
		}
		var w, h int64
		if _, err := fmt.Sscanf(strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0]), "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
			return apierr.New(http.StatusUnprocessableEntity, "not a readable image")
		}
		if w*h > 3*imaging.MaxPixels {
			return apierr.New(http.StatusUnprocessableEntity, "image is %dx%d, over the %d pixel source limit", w, h, 3*imaging.MaxPixels)
		}
		png, err := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-protocol_whitelist", "file",
			"-i", path, "-frames:v", "1", "-f", "image2pipe", "-c:v", "png", "-").Output()
		if err != nil || len(png) == 0 {
			return apierr.New(http.StatusUnprocessableEntity, "decoding image: %v", ExitMsg(err))
		}
		img, err = decodePNG(png)
		return err
	})
	return img, err
}

func tempExt(ext string) string {
	if ext == "" || strings.ContainsAny(ext, "/\\") {
		return ""
	}
	return "." + ext
}
