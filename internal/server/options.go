package server

import (
	"fmt"
	"image/color"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/dockette/docshot/internal/apierr"
	"github.com/dockette/docshot/internal/imaging"
	"github.com/dockette/docshot/internal/render"
)

// options adds what never changes the pixels: storage and the response.
type options struct {
	render.Options

	store         bool
	storeStrategy string

	autoFormat   bool // format came from Accept → Vary: Accept
	download     bool
	downloadName string
	fallback     bool
}

// processingParams are rejected under DOCSHOT_ONLY_PRESETS, which fixes the variant set.
var processingParams = []string{"dpi", "width", "height", "fit", "position", "format", "quality", "trim", "pad", "bg", "dpr", "grid", "time", "labels", "limit"}

// Allowed parameters per operation; anything else is a 400, so typos and
// foreign parameters neither return a default picture nor split the cache.
var (
	sourceParams = []string{"path", "s3", "url", "name", "sign", "v"} // v: cache buster, part of the cache key
	commonParams = []string{"page", "dpi", "width", "height", "fit", "position", "trim", "pad", "bg", "dpr", "format", "quality", "preset", "store", "fallback", "download"}
	opParams     = map[string][]string{
		render.OpPage:  {"time"},
		render.OpGrid:  {"grid", "labels"},
		render.OpPages: {"limit"},
	}
)

func checkParams(q url.Values, op string) error {
	allowed := map[string]bool{}
	for _, l := range [][]string{sourceParams, commonParams, opParams[op]} {
		for _, p := range l {
			allowed[p] = true
		}
	}
	if op == "info" {
		allowed = map[string]bool{}
		for _, p := range sourceParams {
			allowed[p] = true
		}
	}
	for k := range q {
		if allowed[k] {
			continue
		}
		for o, ps := range opParams {
			for _, p := range ps {
				if p == k {
					return &apierr.Error{Status: http.StatusBadRequest, Code: "parameter_not_applicable",
						Msg: fmt.Sprintf("%s= applies to %s only", k, endpointsOf(o))}
				}
			}
		}
		msg := fmt.Sprintf("unknown parameter %q", k)
		if hint := closest(k, allowed); hint != "" {
			msg += fmt.Sprintf(" (did you mean %q?)", hint)
		}
		return &apierr.Error{Status: http.StatusBadRequest, Code: "unknown_parameter", Msg: msg}
	}
	return nil
}

func endpointsOf(op string) string {
	if op == render.OpPage {
		return "/render and /upload"
	}
	return "/render/" + op + " and /upload/" + op
}

// closest returns the allowed name within edit distance 2, if any.
func closest(k string, allowed map[string]bool) string {
	best, bestD := "", 3
	for a := range allowed {
		if d := editDistance(k, a); d < bestD || (d == bestD && a < best) {
			best, bestD = a, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// parseClock parses "42.5", "1:23", "1:02:03.5" → seconds.
func parseClock(v string) (float64, bool) {
	var t float64
	for _, part := range strings.Split(v, ":") {
		n, err := strconv.ParseFloat(part, 64)
		if err != nil || n < 0 {
			return 0, false
		}
		t = t*60 + n
	}
	return t, true
}

const defaultLimit = 20

// builtinPresets can be extended or overridden by DOCSHOT_PRESETS.
var builtinPresets = map[string]string{
	"thumb":   "width=300&height=400&fit=cover&position=top&format=webp&quality=80",
	"card":    "width=480&height=270&fit=cover&position=top&format=webp&quality=80",
	"preview": "width=1200&format=webp&quality=85",
	"og":      "width=1200&height=630&fit=pad&trim=10&pad=40&format=png",
}

// parsePresets reads "name:query;name:query".
func parsePresets(spec string) (map[string]url.Values, error) {
	out := map[string]url.Values{}
	add := func(name, q string) error {
		v, err := url.ParseQuery(q)
		if err != nil {
			return fmt.Errorf("preset %q: %v", name, err)
		}
		out[name] = v
		return nil
	}
	for name, q := range builtinPresets {
		if err := add(name, q); err != nil {
			return nil, err
		}
	}
	for _, item := range strings.Split(spec, ";") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		name, q, ok := strings.Cut(item, ":")
		if !ok {
			return nil, fmt.Errorf("preset %q: want name:query", item)
		}
		if err := add(strings.TrimSpace(name), strings.TrimSpace(q)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Server) parseOpts(r *http.Request, op string) (options, error) {
	q := r.URL.Query()
	if err := checkParams(q, op); err != nil {
		return options{Options: render.Options{Op: op}, fallback: s.cfg.fallback}, err
	}
	o := options{
		Options: render.Options{
			Options: imaging.Options{DPI: s.cfg.defaultDPI, Format: "png", Quality: 85, Fit: "contain", Position: "center", BG: imaging.White},
			Op:      op, Page: 1, Limit: defaultLimit, Labels: true,
		},
		fallback: s.cfg.fallback,
	}

	if s.cfg.onlyPresets {
		for _, p := range processingParams {
			if q.Has(p) {
				return o, apierr.New(http.StatusBadRequest, "only presets are allowed here (got %s=); use ?preset=", p)
			}
		}
	}
	// A preset supplies defaults; explicit parameters still win.
	if name := q.Get("preset"); name != "" {
		p, ok := s.cfg.presets[name]
		if !ok {
			return o, apierr.New(http.StatusBadRequest, "unknown preset %q", name)
		}
		merged := url.Values{}
		for k, v := range p {
			merged[k] = v
		}
		for k, v := range q {
			merged[k] = v
		}
		q = merged
	}

	intParam := func(name string, dst *int, lo, hi int) error {
		v := q.Get(name)
		if v == "" {
			return nil
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < lo || n > hi {
			return apierr.New(http.StatusBadRequest, "%s must be an integer in [%d, %d]", name, lo, hi)
		}
		*dst = n
		return nil
	}
	oneOf := func(name string, dst *string, allowed ...string) error {
		v := strings.ToLower(q.Get(name))
		if v == "" {
			return nil
		}
		for _, a := range allowed {
			if v == a {
				*dst = v
				return nil
			}
		}
		return apierr.New(http.StatusBadRequest, "%s must be one of %s", name, strings.Join(allowed, ", "))
	}
	for _, err := range []error{
		intParam("page", &o.Page, 1, 100000),
		intParam("dpi", &o.DPI, 30, 600),
		intParam("width", &o.Width, 16, s.cfg.maxSide),
		intParam("height", &o.Height, 16, s.cfg.maxSide),
		intParam("quality", &o.Quality, 1, 100),
		intParam("pad", &o.Pad, 0, 1000),
		oneOf("fit", &o.Fit, "contain", "cover", "fill", "pad"),
		oneOf("position", &o.Position, "top", "center", "bottom"),
	} {
		if err != nil {
			return o, err
		}
	}

	switch t := strings.ToLower(q.Get("trim")); t {
	case "", "0", "false":
	case "1", "true":
		o.Trim = 10 // survives JPEG noise and anti-aliasing
	default:
		if err := intParam("trim", &o.Trim, 2, 100); err != nil {
			return o, err
		}
	}

	if v := q.Get("bg"); v != "" {
		c, err := parseColor(v)
		if err != nil {
			return o, apierr.New(http.StatusBadRequest, "bg: %v", err)
		}
		o.BG, o.BGSet = c, true
	}

	switch f := strings.ToLower(q.Get("format")); f {
	case "":
	case "jpg", "jpeg":
		o.Format = "jpeg"
	case "png", "webp":
		o.Format = f
	case "auto":
		// Like imgproxy's AUTO_WEBP: WebP if accepted, else PNG (safe for text).
		o.autoFormat = true
		o.Format = "png"
		if strings.Contains(r.Header.Get("Accept"), "image/webp") {
			o.Format = "webp"
		}
	default:
		return o, apierr.New(http.StatusBadRequest, "format must be png, jpeg, webp or auto")
	}

	// dpr is folded into the sizes, so it stays out of the cache key.
	if v := q.Get("dpr"); v != "" {
		d, err := strconv.ParseFloat(v, 64)
		if err != nil || d < 1 || d > 4 {
			return o, apierr.New(http.StatusBadRequest, "dpr must be a number in [1, 4]")
		}
		mul := func(n int) int { return min(int(float64(n)*d+0.5), s.cfg.maxSide) }
		if o.Width > 0 || o.Height > 0 {
			o.Width, o.Height = mul(o.Width), mul(o.Height)
		} else {
			o.DPI = int(float64(o.DPI)*d + 0.5)
		}
		o.Pad = int(float64(o.Pad)*d + 0.5)
	}

	if g := strings.ToLower(q.Get("grid")); g != "" && g != "auto" {
		c, r, ok := strings.Cut(g, "x")
		cols, err1 := strconv.Atoi(c)
		rows, err2 := strconv.Atoi(r)
		if !ok || err1 != nil || err2 != nil || cols < 1 || rows < 1 || cols > 10 || rows > 10 {
			return o, apierr.New(http.StatusBadRequest, "grid must be COLSxROWS, each 1–10 (e.g. grid=3x3)")
		}
		o.GridCols, o.GridRows = cols, rows
	}
	if v := q.Get("time"); v != "" {
		t, ok := parseClock(v)
		if !ok {
			return o, apierr.New(http.StatusBadRequest, "time must be seconds (42.5) or a clock (1:23, 1:02:03)")
		}
		o.At, o.AtSet = t, true
	}
	if v := q.Get("labels"); v != "" {
		o.Labels = boolParam(v)
	}
	if err := intParam("limit", &o.Limit, 1, 200); err != nil {
		return o, err
	}
	if v := strings.ToLower(q.Get("store")); v != "" {
		target, strategy, _ := strings.Cut(v, ":")
		if target != "s3" {
			return o, apierr.New(http.StatusBadRequest, "store must be s3 or s3:append|replace|suffix|hash")
		}
		if strategy == "" {
			strategy = s.cfg.storeStrategy
		}
		if !storeStrategies[strategy] {
			return o, apierr.New(http.StatusBadRequest, "store strategy must be append, replace, suffix or hash")
		}
		o.store, o.storeStrategy = true, strategy
	}

	if int64(o.Width)*int64(o.Height) > imaging.MaxPixels {
		return o, apierr.New(http.StatusBadRequest, "width×height is over the %d pixel limit (DOCSHOT_MAX_PIXELS)", imaging.MaxPixels)
	}

	if v := q.Get("download"); v != "" && !strings.EqualFold(v, "0") && !strings.EqualFold(v, "false") {
		o.download = true
		if !boolParam(v) {
			o.downloadName = sanitizeFilename(v)
		}
	}
	if v := q.Get("fallback"); v != "" {
		o.fallback = boolParam(v)
	}
	return o, nil
}

func boolParam(v string) bool {
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// parseColor accepts fff, ffffff, ffffff80 (optional #) and "transparent".
func parseColor(v string) (color.NRGBA, error) {
	v = strings.TrimPrefix(strings.ToLower(v), "#")
	if v == "transparent" {
		return color.NRGBA{}, nil
	}
	if len(v) == 3 {
		v = string([]byte{v[0], v[0], v[1], v[1], v[2], v[2]})
	}
	if len(v) == 6 {
		v += "ff"
	}
	n, err := strconv.ParseUint(v, 16, 32)
	if len(v) != 8 || err != nil {
		return color.NRGBA{}, fmt.Errorf("want a hex colour (fff, ffffff, ffffff80) or transparent")
	}
	return color.NRGBA{uint8(n >> 24), uint8(n >> 16), uint8(n >> 8), uint8(n)}, nil
}

func sanitizeFilename(v string) string {
	var b strings.Builder
	for _, r := range v {
		switch {
		case r == '/' || r == '\\' || r == '"' || r < 0x20:
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}
