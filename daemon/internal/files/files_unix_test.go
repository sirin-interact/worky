//go:build !windows

package files

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/sirin-interact/worky/daemon/internal/protocol"
)

// Opening a named pipe waits for whoever is at its other end. Nothing here
// is, so every call has to answer without opening it.
func TestNamedPipe(t *testing.T) {
	dir := t.TempDir()
	pipe := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(pipe, 0o644); err != nil {
		t.Skipf("cannot make a named pipe: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, perr := Read(protocol.FsReadParams{Path: pipe})
		wantCode(t, "reading a pipe", perr, protocol.ErrInvalidRequest)
		_, perr = List(pipe)
		wantCode(t, "listing a pipe", perr, protocol.ErrInvalidRequest)
		_, perr = Write(protocol.FsWriteParams{Path: pipe, Data: []byte("x")})
		wantCode(t, "writing to a pipe", perr, protocol.ErrInvalidRequest)
		_, perr = Write(protocol.FsWriteParams{Path: pipe, Exclusive: true})
		wantCode(t, "creating a file where a pipe is", perr, protocol.ErrAlreadyExists)

		st, perr := Stat(pipe)
		if perr != nil || st.Type != protocol.TypeOther {
			t.Errorf("stat of a pipe: %+v, %v", st, perr)
		}
		list, perr := List(dir)
		if perr != nil || describe(list.Entries) != "pipe:other" {
			t.Errorf("the directory with the pipe: %+v, %v", list, perr)
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a call on a named pipe did not return")
	}
}
