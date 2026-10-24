package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/dockette/docshot/internal/apierr"
	"github.com/dockette/docshot/internal/metrics"
)

// ?url= fetching is SSRF by design, hence the mandatory
// DOCSHOT_FETCH_ALLOW list, re-checked on every redirect, and a dial guard
// that refuses loopback (Gotenberg, docshot itself), link-local (cloud metadata)
// and unspecified addresses whatever a name resolves to.

type source struct {
	body []byte
	name string
}

func (s *Server) fromURL(ctx context.Context, raw string) (src source, err error) {
	defer func() {
		result := "ok"
		switch {
		case err == nil:
		case statusOf(err) == http.StatusForbidden:
			result = "denied"
		default:
			result = "error"
		}
		metrics.URLFetches.WithLabelValues(result).Inc()
	}()
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

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return source{}, err
	}
	req.Header.Set("User-Agent", "docshot/"+s.cfg.Version)
	resp, err := s.fetch.Do(req)
	if err != nil {
		if denied := (*apierr.Error)(nil); errors.As(err, &denied) {
			return source{}, denied
		}
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

// compileAllow anchors the expression at the start, so "https://a.example/" cannot match "https://evil/?https://a.example/".
func compileAllow(expr string) (*regexp.Regexp, error) {
	if expr == "" {
		return nil, nil
	}
	return regexp.Compile("^(?:" + expr + ")")
}

// newFetchClient is the ?url= client: allow-listed redirects, guarded dials.
func newFetchClient(cfg Config) *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	if !cfg.fetchLoopback {
		dialer.Control = dialGuard
	}
	t.DialContext = dialer.DialContext
	return &http.Client{
		Transport: t,
		Timeout:   cfg.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if cfg.fetchAllow == nil || !cfg.fetchAllow.MatchString(req.URL.String()) {
				return fmt.Errorf("redirect to %s not allowed by DOCSHOT_FETCH_ALLOW", req.URL.Host)
			}
			return nil
		},
	}
}

// dialGuard runs on the resolved address of every connection.
func dialGuard(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return apierr.New(http.StatusForbidden, "url resolves to %s, a loopback, link-local or unspecified address", ip)
	}
	return nil
}
