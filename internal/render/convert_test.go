package render

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dockette/docshot/internal/apierr"
)

func TestHashBytes(t *testing.T) {
	b := []byte("document\x00bytes")
	if HashBytes(b) != Hash(string(b)) {
		t.Fatal("HashBytes differs from Hash: cache keys and ETags would change")
	}
}

// The form reaches Gotenberg whole, streamed without a declared length.
func TestGotenbergStreamsForm(t *testing.T) {
	doc := bytes.Repeat([]byte("docx"), 1<<18)
	got := make(chan error, 1)
	gotenberg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- func() error {
			if r.URL.Path != "/forms/libreoffice/convert" || r.ContentLength != -1 {
				return apierr.New(400, "path %s, length %d", r.URL.Path, r.ContentLength)
			}
			if err := r.ParseMultipartForm(32 << 20); err != nil {
				return err
			}
			f, _, err := r.FormFile("files")
			if err != nil {
				return err
			}
			b, _ := io.ReadAll(f)
			if !bytes.Equal(b, doc) || r.FormValue("singlePageSheets") != "true" {
				return apierr.New(400, "file of %d bytes, fields %v", len(b), r.MultipartForm.Value)
			}
			return nil
		}()
		_, _ = w.Write([]byte("%PDF-1.7"))
	}))
	defer gotenberg.Close()

	r := New(Config{GotenbergURL: gotenberg.URL, Timeout: 10 * time.Second}, nil)
	pdf, err := r.gotenberg(context.Background(), "/forms/libreoffice/convert", map[string][]byte{"document.xlsx": doc},
		map[string]string{"singlePageSheets": "true"})
	if err != nil || string(pdf) != "%PDF-1.7" {
		t.Fatalf("%q %v", pdf, err)
	}
	if err := <-got; err != nil {
		t.Fatal(err)
	}
}

func TestGotenbergErrors(t *testing.T) {
	status := http.StatusBadRequest
	gotenberg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		http.Error(w, "corrupt", status)
	}))
	defer gotenberg.Close()
	r := New(Config{GotenbergURL: gotenberg.URL, Timeout: 10 * time.Second}, nil)
	for give, want := range map[int]int{http.StatusBadRequest: http.StatusUnprocessableEntity, http.StatusInternalServerError: http.StatusBadGateway} {
		status = give
		_, err := r.gotenberg(context.Background(), "/forms/chromium/convert/html", map[string][]byte{"index.html": []byte("<p>")}, nil)
		var he *apierr.Error
		if !asHTTP(err, &he) || he.Status != want {
			t.Errorf("gotenberg %d: %v, want %d", give, err, want)
		}
	}
	// Unreachable: the request fails and the form writer must not leak.
	gotenberg.Close()
	if _, err := r.gotenberg(context.Background(), "/x", map[string][]byte{"a": bytes.Repeat([]byte("a"), 1<<20)}, nil); err == nil {
		t.Fatal("no error from a closed Gotenberg")
	}
}
