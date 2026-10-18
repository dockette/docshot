package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"regexp"

	"github.com/dockette/docshot/internal/apierr"
)

// ?url= fetching is SSRF by design, hence the mandatory
// DOCSHOT_FETCH_ALLOW list, re-checked on every redirect.

type source struct {
	body []byte
	name string
}

func (s *Server) fromURL(ctx context.Context, raw string) (source, error) {
	if s.cfg.fetchAllow == nil {
		return source{}, apierr.New(http.StatusForbidden, "url source disabled: set DOCSHOT_FETCH_ALLOW to a regexp of allowed URLs")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return source{}, apierr.New(http.StatusBadRequest, "url must be an absolute http(s) URL")
	}
	if !s.cfg.fetchAllow.MatchString(u.String()) {
		return source{}, apierr.New(http.StatusForbidden, "url not allowed by DOCSHOT_FETCH_ALLOW")
	}

	client := &http.Client{
		Timeout: s.cfg.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if !s.cfg.fetchAllow.MatchString(req.URL.String()) {
				return fmt.Errorf("redirect to %s not allowed by DOCSHOT_FETCH_ALLOW", req.URL.Host)
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return source{}, err
	}
	req.Header.Set("User-Agent", "docshot/"+s.cfg.Version)
	resp, err := client.Do(req)
	if err != nil {
		return source{}, apierr.New(http.StatusBadGateway, "fetching url: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return source{}, apierr.New(http.StatusBadGateway, "fetching url: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, s.cfg.maxBytes+1))
	if err != nil {
		return source{}, apierr.New(http.StatusBadGateway, "fetching url: %v", err)
	}
	if int64(len(body)) > s.cfg.maxBytes {
		return source{}, apierr.New(http.StatusRequestEntityTooLarge, "file larger than %d bytes", s.cfg.maxBytes)
	}

	// Name: the server's Content-Disposition, else the last path segment.
	name := path.Base(resp.Request.URL.Path)
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil && params["filename"] != "" {
		name = filepath.Base(params["filename"])
	}
	if name == "/" || name == "." {
		name = ""
	}
	return source{body: body, name: name}, nil
}

func compileAllow(expr string) *regexp.Regexp {
	if expr == "" {
		return nil
	}
	return regexp.MustCompile(expr)
}
