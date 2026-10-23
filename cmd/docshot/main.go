// Command docshot serves the docshot HTTP API; with arguments it also supervises Gotenberg.
package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dockette/docshot/internal/server"
	"github.com/dockette/docshot/internal/supervisor"
)

var version = "dev" // release from VERSION (YYYY.MM.DD[.N]), set with -ldflags "-X main.version=..."

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe /health on the local listener and exit (for Docker HEALTHCHECK; the image has no shell)")
	sign := flag.String("sign", "", "print the signed form of a URL path+query (needs DOCSHOT_SIGN_KEY), e.g. -sign '/render?url=https://example.com/a.docx&preset=thumb'")
	flag.Parse()

	if *sign != "" {
		key := os.Getenv("DOCSHOT_SIGN_KEY")
		if key == "" {
			fmt.Fprintln(os.Stderr, "DOCSHOT_SIGN_KEY is not set")
			os.Exit(2)
		}
		u, err := server.SignURL([]byte(key), *sign)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Println(u)
		return
	}

	cfg, err := server.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	cfg.Version = version
	if *healthcheck {
		os.Exit(probe(cfg.Listen))
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Arguments are a Gotenberg command line to supervise (all-in-one image).
	if args := flag.Args(); len(args) > 0 {
		port := cmp.Or(os.Getenv("GOTENBERG_INTERNAL_PORT"), "3001")
		cfg.GotenbergURL = "http://127.0.0.1:" + port
		go supervisor.New(args, port).Run(ctx)
	}

	s, err := server.New(cfg)
	if err != nil {
		slog.Error("init failed", "err", err)
		os.Exit(1)
	}
	defer s.Close()

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      cfg.Timeout + 30*time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	slog.Info("listening", "addr", cfg.Listen, "gotenberg", cfg.GotenbergURL, "version", version)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server failed", "err", err)
		os.Exit(1)
	}
}

func probe(listen string) int {
	addr := listen
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	c := http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get("http://" + addr + "/readyz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
