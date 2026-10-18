package server

import (
	"net/url"
	"strings"
	"testing"

	"github.com/dockette/docshot/internal/apierr"
	"github.com/dockette/docshot/internal/render"
)

func TestParseClock(t *testing.T) {
	for in, want := range map[string]float64{"42.5": 42.5, "1:23": 83, "1:02:03": 3723, "0": 0} {
		if got, ok := parseClock(in); !ok || got != want {
			t.Errorf("%q → %v %v, want %v", in, got, ok, want)
		}
	}
	for _, bad := range []string{"x", "-1", "1:-2"} {
		if _, ok := parseClock(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestCheckParams(t *testing.T) {
	q := func(s string) url.Values { v, _ := url.ParseQuery(s); return v }
	if err := checkParams(q("width=3&page=2&time=4&format=webp"), render.OpPage); err != nil {
		t.Errorf("valid page params rejected: %v", err)
	}
	if err := checkParams(q("grid=2x2&labels=0"), render.OpGrid); err != nil {
		t.Errorf("valid grid params rejected: %v", err)
	}
	cases := map[string]string{"widht=3": "unknown_parameter", "limit=3": "parameter_not_applicable", "t=4": "unknown_parameter"}
	for s, code := range cases {
		err := checkParams(q(s), render.OpPage)
		he, ok := err.(*apierr.Error)
		if !ok || he.Code != code {
			t.Errorf("%s: %v, want code %s", s, err, code)
		}
	}
	if err := checkParams(q("widht=3"), render.OpPage); err == nil || !strings.Contains(err.Error(), `"width"`) {
		t.Errorf("no suggestion: %v", err)
	}
	if err := checkParams(q("width=3"), "info"); err == nil {
		t.Error("/info accepted a processing parameter")
	}
}
