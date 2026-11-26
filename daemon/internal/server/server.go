// Package server is the daemon's network surface: a health endpoint and the
// WebSocket that carries the protocol.
package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/sirin-interact/worky/daemon/internal/auth"
	"github.com/sirin-interact/worky/daemon/internal/project"
	"github.com/sirin-interact/worky/daemon/internal/protocol"
	"github.com/sirin-interact/worky/daemon/internal/session"
	"github.com/sirin-interact/worky/daemon/internal/watch"
)

// The origins the Worky app's pages are served from.
var appOrigins = []string{
	"tauri://localhost",
	"http://tauri.localhost",
	"https://tauri.localhost",
}

// Options configures the server.
type Options struct {
	// Token is what a client must present.
	Token string
	// Version is the daemon's build version.
	Version string
	// Sessions holds the daemon's sessions.
	Sessions *session.Manager
	// Projects holds the daemon's projects.
	Projects *project.Manager
	// Watcher watches directories for the clients.
	Watcher *watch.Watcher
	// AllowedOrigins lists web origins that may connect besides the app's
	// own, for example a development server.
	AllowedOrigins []string
	// HelloTimeout is how long a new connection may take to send hello.
	// Zero means 5 seconds.
	HelloTimeout time.Duration
}

type server struct {
	Options
	origins map[string]bool
}

// New returns the daemon's handler.
func New(o Options) http.Handler {
	if o.HelloTimeout == 0 {
		o.HelloTimeout = 5 * time.Second
	}
	// A session belongs to the worktree it runs in, and is matched again
	// whenever the worktrees change.
	o.Sessions.SetWorktreeOf(o.Projects.WorktreeOf)
	o.Projects.OnWorktrees(o.Sessions.Relocate)

	s := &server{Options: o, origins: make(map[string]bool)}
	for _, origin := range append(appOrigins, o.AllowedOrigins...) {
		s.origins[strings.ToLower(origin)] = true
	}

	mux := http.NewServeMux()
	mux.Handle("GET /v1/health", auth.Require(o.Token, http.HandlerFunc(s.health)))
	// The token for this one travels in the first message, not in a header.
	mux.HandleFunc("GET /v1/ws", s.serveWebSocket)
	return mux
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(protocol.Health{
		Name:     "workyd",
		Version:  s.Version,
		Protocol: protocol.Supported,
	})
}

// originAllowed reports whether a request may open a WebSocket. A request
// without an Origin header does not come from a web page.
func (s *server) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	return origin == "" || s.origins[strings.ToLower(origin)]
}
