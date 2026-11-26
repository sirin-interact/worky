package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sirin-interact/worky/daemon/internal/protocol"
)

func TestHealth(t *testing.T) {
	h := New("secret", "test")

	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got protocol.Health
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := protocol.Health{Name: "workyd", Version: "test", Protocol: protocol.Version}
	if got != want {
		t.Fatalf("health = %+v, want %+v", got, want)
	}
}

func TestHealthRequiresToken(t *testing.T) {
	h := New("secret", "test")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/health", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
