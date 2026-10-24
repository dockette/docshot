// Command docshot serves the docshot HTTP API; with arguments it also supervises Gotenberg.
package main

import (
	"cmp"
	"context"
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
	healthcheck := flag.Bool("healthcheck", false, "probe /readyz on the local listener and exit (for Docker HEALTHCHECK; the image has no shell)")
	sign := flag.String("sign", "", "print the signed form of a URL path+query (needs DOCSHOT_SIGN_KEY), e.g. -sign '/render?url=https://example.com/a.docx&preset=thumb'")
	expires := flag.Duration("expires", 0, "with -sign: the URL stops working after this long (adds exp=), e.g. 24h")
	flag.Parse()

	if *sign != "" {
		key := os.Getenv("DOCSHOT_SIGN_KEY")
		if key == "" {
			fmt.Fprintln(os.Stderr, "DOCSHOT_SIGN_KEY is not set")
			os.Exit(2)
		}
		u, err := server.SignURL([]byte(key), *sign, *expires)
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

	slog.SetDefault(slog.New(cfg.LogHandler(os.Stdout)))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Arguments are a Gotenberg command line to supervise (all-in-one image). It has its own
	// context: on a signal it is stopped only after in-flight requests have drained.
	gotenberg, stopGotenberg := context.WithCancel(context.Background())
	supervised := make(chan struct{})
	if args := flag.Args(); len(args) > 0 {
		port := cmp.Or(os.Getenv("GOTENBERG_INTERNAL_PORT"), "3001")
		cfg.GotenbergURL = "http://127.0.0.1:" + port
		go func() { supervisor.New(args, port).Run(gotenberg); close(supervised) }()
	} else {
		close(supervised)
	}
	exit := func(code int) {
		stopGotenberg()
		<-supervised
		os.Exit(code)
	}

	s, err := server.New(cfg)
	if err != nil {
		slog.Error("init failed", "err", err)
		exit(1)
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       cfg.Timeout, // a whole upload, so a slow body cannot hold a connection forever
		WriteTimeout:      cfg.Timeout + 30*time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	served := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.Listen, "gotenberg", cfg.GotenbergURL, "version", version)
		served <- srv.ListenAndServe()
	}()

	// ListenAndServe returns as soon as Shutdown starts; Shutdown returns once requests have drained.
	code := 0
	select {
	case err := <-served:
		slog.Error("server failed", "err", err)
		code = 1
	case <-ctx.Done():
		slog.Info("shutting down", "drain", shutdownTimeout)
		drain, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		if err := srv.Shutdown(drain); err != nil {
			slog.Warn("shutdown: requests still running were cut off", "err", err)
		}
		cancel()
	}
	s.Close()
	exit(code)
}

// shutdownTimeout bounds the drain on SIGTERM; compose's stop_grace_period leaves room for it and Gotenberg's exit.
const shutdownTimeout = 30 * time.Second

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
