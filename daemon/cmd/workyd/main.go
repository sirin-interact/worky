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
	"strings"
	"syscall"
	"time"

	"github.com/sirin-interact/worky/daemon/internal/auth"
	"github.com/sirin-interact/worky/daemon/internal/project"
	"github.com/sirin-interact/worky/daemon/internal/server"
	"github.com/sirin-interact/worky/daemon/internal/session"
	"github.com/sirin-interact/worky/daemon/internal/termlog"
	"github.com/sirin-interact/worky/daemon/internal/watch"
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
	configDir := flag.String("config-dir", "", "directory for the daemon's own files (default: <user config dir>/worky)")
	tokenFile := flag.String("token-file", "", "path to the token file (default: <config dir>/token)")
	allowOrigins := flag.String("allow-origin", "", "comma-separated web origins that may connect besides the app, such as a dev server")
	flag.Parse()

	dir := *configDir
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return fmt.Errorf("locate config directory: %w", err)
		}
		dir = filepath.Join(base, "worky")
	}
	path := *tokenFile
	if path == "" {
		path = filepath.Join(dir, "token")
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
	watcher, err := watch.New()
	if err != nil {
		return fmt.Errorf("watch files: %w", err)
	}
	defer watcher.Close()
	projects, err := project.Open(filepath.Join(dir, "projects.json"), watcher)
	if err != nil {
		return fmt.Errorf("load projects: %w", err)
	}
	defer projects.Close()
	sessions := session.NewManager(termlog.Options{})
	defer sessions.Shutdown()

	var origins []string
	for origin := range strings.SplitSeq(*allowOrigins, ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			origins = append(origins, origin)
		}
	}
	srv := &http.Server{
		Handler: server.New(server.Options{
			Token:          token,
			Version:        version,
			Sessions:       sessions,
			Projects:       projects,
			Watcher:        watcher,
			AllowedOrigins: origins,
		}),
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
