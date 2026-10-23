package media

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"net/http"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"github.com/dockette/docshot/internal/apierr"
	"github.com/dockette/docshot/internal/imaging"
)

type AudioInfo struct {
	Duration             float64
	Title, Artist, Album string
	Codec                string
	SampleRate, Channels int
	HasCover             bool
}

func ProbeAudio(ctx context.Context, path string) (AudioInfo, error) {
	out, err := output(exec.CommandContext(ctx, "ffprobe", "-v", "error", "-protocol_whitelist", "file",
		"-show_entries", "format=duration:format_tags=title,artist,album:stream=codec_type,codec_name,sample_rate,channels:stream_tags=title,artist,album:stream_disposition=attached_pic",
		"-of", "json", path))
	if err != nil {
		return AudioInfo{}, apierr.New(http.StatusUnprocessableEntity, "not a readable audio file: %v", ExitMsg(err))
	}
	var p struct {
		Streams []struct {
			Type        string            `json:"codec_type"`
			Codec       string            `json:"codec_name"`
			SampleRate  string            `json:"sample_rate"`
			Channels    int               `json:"channels"`
			Tags        map[string]string `json:"tags"`
			Disposition struct {
				AttachedPic int `json:"attached_pic"`
			} `json:"disposition"`
		} `json:"streams"`
		Format struct {
			Duration string            `json:"duration"`
			Tags     map[string]string `json:"tags"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &p); err != nil {
		return AudioInfo{}, apierr.New(http.StatusUnprocessableEntity, "not a readable audio file")
	}
	a := AudioInfo{}
	a.Duration, _ = strconv.ParseFloat(p.Format.Duration, 64)
	// Tags live on the container (mp3, m4a, flac) or on the stream (ogg, opus).
	tag := func(k string) string {
		maps := []map[string]string{p.Format.Tags}
		for _, st := range p.Streams {
			maps = append(maps, st.Tags)
		}
		for _, m := range maps {
			for kk, v := range m {
				if strings.EqualFold(kk, k) && v != "" {
					return v
				}
			}
		}
		return ""
	}
	a.Title, a.Artist, a.Album = tag("title"), tag("artist"), tag("album")
	found := false
	for _, st := range p.Streams {
		switch {
		case st.Type == "audio" && !found:
			a.Codec, a.Channels, found = st.Codec, st.Channels, true
			a.SampleRate, _ = strconv.Atoi(st.SampleRate)
		case st.Type == "video" && st.Disposition.AttachedPic == 1:
			a.HasCover = true
		}
	}
	if !found {
		return a, apierr.New(http.StatusUnprocessableEntity, "no audio stream")
	}
	return a, nil
}

// AudioSequence renders a single card page: cover art, tags, waveform.
func AudioSequence(ctx context.Context, body []byte, ext, name string, maxSide int) (*imaging.Sequence, error) {
	var card image.Image
	var dur float64
	err := WithTempFile(body, ext, func(path string) error {
		a, err := ProbeAudio(ctx, path)
		if err != nil {
			return err
		}
		dur = a.Duration
		card, err = drawAudioCard(ctx, path, a, name, ext)
		return err
	})
	if err != nil {
		return nil, err
	}
	q := imaging.Still(card, maxSide)
	q.Duration = dur
	return q, nil
}

func drawAudioCard(ctx context.Context, path string, a AudioInfo, name, ext string) (image.Image, error) {
	const W, H, pad, coverSize, waveH = 1600, 560, 40, 240, 220
	bg := color.NRGBA{17, 24, 39, 255}
	img := image.NewNRGBA(image.Rect(0, 0, W, H))
	draw.Draw(img, img.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)

	cover := image.Rect(pad, pad, pad+coverSize, pad+coverSize)
	drew := false
	if a.HasCover {
		if out, err := output(exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-protocol_whitelist", "file",
			"-i", path, "-map", "0:v:0", "-frames:v", "1", "-f", "image2pipe", "-c:v", "png", "-")); err == nil && len(out) > 0 {
			if pic, err := decodePNG(out); err == nil {
				pic = imaging.Fit(pic, imaging.Options{Width: coverSize, Height: coverSize, Fit: "cover", Position: "center"}, 4096)
				draw.Draw(img, cover, pic, pic.Bounds().Min, draw.Src)
				drew = true
			}
		}
	}
	if !drew {
		draw.Draw(img, cover, image.NewUniform(color.NRGBA{55, 65, 81, 255}), image.Point{}, draw.Src)
		imaging.DrawText(img, "♪", cover.Min.X+coverSize/2-40, cover.Min.Y+coverSize/2+45, 130, color.NRGBA{156, 163, 175, 255})
	}

	x := pad + coverSize + pad
	title := cmp.Or(a.Title, name)
	imaging.DrawText(img, ellipsize(title, 40), x, pad+64, 56, color.NRGBA{255, 255, 255, 255})
	line2 := strings.Join(slices.DeleteFunc([]string{a.Artist, a.Album}, isBlank), " — ")
	if line2 != "" {
		imaging.DrawText(img, ellipsize(line2, 60), x, pad+130, 36, color.NRGBA{209, 213, 219, 255})
	}
	facts := []string{clock(a.Duration), strings.ToUpper(cmp.Or(a.Codec, ext))}
	if a.SampleRate > 0 {
		facts = append(facts, fmt.Sprintf("%.1f kHz", float64(a.SampleRate)/1000))
	}
	if a.Channels == 1 {
		facts = append(facts, "mono")
	} else if a.Channels == 2 {
		facts = append(facts, "stereo")
	} else if a.Channels > 2 {
		facts = append(facts, fmt.Sprintf("%d ch", a.Channels))
	}
	imaging.DrawText(img, strings.Join(facts, "  ·  "), x, pad+190, 30, color.NRGBA{156, 163, 175, 255})

	waveW := W - 2*pad
	out, err := output(exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-protocol_whitelist", "file",
		"-i", path, "-filter_complex",
		fmt.Sprintf("aformat=channel_layouts=mono,showwavespic=s=%dx%d:colors=0x60a5fa:scale=sqrt", waveW, waveH),
		"-frames:v", "1", "-f", "image2pipe", "-c:v", "png", "-"))
	if err == nil {
		if wave, err := decodePNG(out); err == nil {
			r := image.Rect(pad, H-pad-waveH, pad+waveW, H-pad)
			draw.Draw(img, r, wave, wave.Bounds().Min, draw.Over)
		}
	}
	return img, nil
}

func ellipsize(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func isBlank(s string) bool { return strings.TrimSpace(s) == "" }
