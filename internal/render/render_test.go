package render

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/dockette/docshot/internal/apierr"
	"github.com/dockette/docshot/internal/imaging"
)

func TestLimiterQueue(t *testing.T) {
	l := newLimiter(1, 1)
	release, err := l.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	waiter := make(chan error, 1)
	go func() {
		r, err := l.acquire(context.Background())
		if err == nil {
			r()
		}
		waiter <- err
	}()
	time.Sleep(50 * time.Millisecond)
	_, err = l.acquire(context.Background())
	var he *apierr.Error
	if err == nil || !asHTTP(err, &he) || he.Status != http.StatusTooManyRequests {
		t.Errorf("third request: %v, want 429", err)
	}
	release()
	if err := <-waiter; err != nil {
		t.Errorf("queued request failed: %v", err)
	}
}

func asHTTP(err error, he **apierr.Error) bool {
	e, ok := err.(*apierr.Error)
	*he = e
	return ok
}

func TestAutoLayout(t *testing.T) {
	for n, want := range map[int][2]int{1: {1, 1}, 2: {2, 1}, 3: {3, 1}, 4: {2, 2}, 6: {3, 2}, 9: {3, 3}, 12: {4, 3}} {
		c, r := autoLayout(n)
		if c != want[0] || r != want[1] {
			t.Errorf("autoLayout(%d) = %dx%d, want %dx%d", n, c, r, want[0], want[1])
		}
	}
}

func TestPick(t *testing.T) {
	seq := &imaging.Sequence{N: 10}
	if got := pick(seq, 3, 4); len(got) != 4 || got[0] != 2 || got[3] != 5 {
		t.Errorf("sequential pick %v", got)
	}
	if got := pick(seq, 9, 4); len(got) != 2 {
		t.Errorf("pick past the end %v", got)
	}
	anim := &imaging.Sequence{N: 100, Spread: true}
	if got := pick(anim, 1, 4); len(got) != 4 || got[0] != 12 || got[3] != 87 {
		t.Errorf("spread pick %v", got)
	}
}

func TestReadArchive(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range []string{"b.txt", "a.md", "__MACOSX/._a.md", ".hidden", "dir/"} {
		w, _ := zw.Create(n)
		w.Write([]byte("hello " + n))
	}
	zw.Close()
	es, err := readArchive(context.Background(), buf.Bytes(), "x.zip", "zip", 1<<20)
	if err != nil || len(es) != 2 || es[0].name != "a.md" || es[1].name != "b.txt" {
		t.Errorf("entries %v %v", es, err)
	}
}

func TestArchiveBudget(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range []string{"a.txt", "b.txt", "c.txt", "clip.mp4", "inner.zip"} {
		w, _ := zw.Create(n)
		w.Write(bytes.Repeat([]byte("x"), 400))
	}
	zw.Close()
	es, err := readArchive(context.Background(), buf.Bytes(), "x.zip", "zip", 1000) // budget for two members of 400 B
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 2 {
		t.Errorf("budget 1000 B kept %d members of 400 B, want 2", len(es))
	}
	for _, e := range es {
		if e.name == "clip.mp4" || e.name == "inner.zip" {
			t.Errorf("%s should be skipped before reading", e.name)
		}
	}
}
