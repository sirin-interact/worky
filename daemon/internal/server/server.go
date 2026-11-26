// Package server is the daemon's HTTP surface.
package server

import (
	"encoding/json"
	"net/http"

	"github.com/sirin-interact/worky/daemon/internal/auth"
	"github.com/sirin-interact/worky/daemon/internal/protocol"
)

// New returns the daemon's handler. Every route requires the token.
func New(token, version string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(protocol.Health{
			Name:     "workyd",
			Version:  version,
			Protocol: protocol.Supported,
		})
	})

	return auth.Require(token, mux)
}
