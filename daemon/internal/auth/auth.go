// Package auth handles the token every client must present.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var errEmptyToken = errors.New("token file is empty")

func readToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("%w: %s", errEmptyToken, path)
	}
	return token, nil
}

// LoadOrCreateToken reads the token at path, creating it on first run.
func LoadOrCreateToken(path string) (string, error) {
	token, err := readToken(path)
	if err == nil {
		return token, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("read token file: %w", err)
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	token = hex.EncodeToString(buf)

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create token directory: %w", err)
	}
	// Created exclusively: of two daemons starting at once, one writes the
	// file and the other reads it, so both hold the token that is on disk.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return awaitToken(path)
	}
	if err != nil {
		return "", fmt.Errorf("create token file: %w", err)
	}
	_, err = f.WriteString(token + "\n")
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", fmt.Errorf("write token file: %w", err)
	}
	return token, nil
}

// awaitToken reads a token file another process has just created and may not
// have filled in yet.
func awaitToken(path string) (string, error) {
	for attempt := 0; ; attempt++ {
		token, err := readToken(path)
		if err == nil {
			return token, nil
		}
		if !errors.Is(err, errEmptyToken) || attempt == 50 {
			return "", fmt.Errorf("read token file: %w", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Equal compares a presented token with the real one in constant time.
func Equal(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// Require rejects any request that does not carry the token as a bearer
// credential.
func Require(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || !Equal(got, token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
