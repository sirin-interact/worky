// Command workyd is the Worky daemon. One runs per machine and owns the
// sessions on it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sirin-interact/worky/daemon/internal/auth"
	"github.com/sirin-interact/worky/daemon/internal/server"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("workyd stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", "127.0.0.1:7433", "address to listen on")
	tokenFile := flag.String("token-file", "", "path to the token file (default: <user config dir>/worky/token)")
	flag.Parse()

	path := *tokenFile
	if path == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return fmt.Errorf("locate config directory: %w", err)
		}
		path = filepath.Join(dir, "worky", "token")
	}
	token, err := auth.LoadOrCreateToken(path)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	srv := &http.Server{
		Handler:           server.New(token, version),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	slog.Info("workyd listening", "addr", ln.Addr().String(), "version", version, "token_file", path)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
