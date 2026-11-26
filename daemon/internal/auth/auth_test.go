package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLoadOrCreateToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "token")

	first, err := LoadOrCreateToken(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(first) != 64 {
		t.Fatalf("token length = %d, want 64", len(first))
	}

	second, err := LoadOrCreateToken(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if second != first {
		t.Fatalf("reloaded token differs from the created one")
	}
}

func TestRequire(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	h := Require("secret", ok)

	tests := []struct {
		name   string
		header string
		want   int
	}{
		{"missing", "", http.StatusUnauthorized},
		{"wrong token", "Bearer nope", http.StatusUnauthorized},
		{"wrong scheme", "Basic secret", http.StatusUnauthorized},
		{"correct", "Bearer secret", http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

// Daemons that start at the same moment must all end up with the token that
// is in the file.
func TestLoadOrCreateTokenTogether(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")

	const daemons = 16
	tokens := make([]string, daemons)
	errs := make([]error, daemons)
	var wg sync.WaitGroup
	for i := range daemons {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tokens[i], errs[i] = LoadOrCreateToken(path)
		}()
	}
	wg.Wait()

	onDisk, err := LoadOrCreateToken(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	for i := range daemons {
		if errs[i] != nil || tokens[i] != onDisk {
			t.Fatalf("daemon %d got %q (%v), the file holds %q", i, tokens[i], errs[i], onDisk)
		}
	}
}

// A run that created the file and got no further leaves it empty. The next
// run does not give up over that.
func TestLoadOrCreateTokenReplacesAnEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	token, err := LoadOrCreateToken(path)
	if err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || strings.TrimSpace(string(onDisk)) != token {
		t.Errorf("token %q, file %q", token, onDisk)
	}
	if again, err := LoadOrCreateToken(path); err != nil || again != token {
		t.Errorf("the next run got %q, %v", again, err)
	}
}
