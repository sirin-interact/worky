package session

import (
	"testing"
	"time"

	"github.com/sirin-interact/worky/daemon/internal/protocol"
	"github.com/sirin-interact/worky/daemon/internal/ptytest"
)

func TestTitleScanner(t *testing.T) {
	tests := []struct {
		name   string
		chunks []string
		want   string
		ok     bool
	}{
		{"bel", []string{"\x1b]0;fix login\x07"}, "fix login", true},
		{"string terminator", []string{"\x1b]2;fix login\x1b\\"}, "fix login", true},
		{"split across writes", []string{"text\x1b", "]0;한글 ", "제목\x07more"}, "한글 제목", true},
		{"the last one wins", []string{"\x1b]0;one\x07\x1b]0;two\x07"}, "two", true},
		{"an empty title clears", []string{"\x1b]0;\x07"}, "", true},
		{"other commands are not titles", []string{"\x1b]8;;https://example.com\x07", "\x1b]11;?\x07"}, "", false},
		{"icon name alone is not a title", []string{"\x1b]1;icon\x07"}, "", false},
		{"control characters are dropped", []string{"\x1b]0;a\tb\x07"}, "ab", true},
		{"too long to be a title", []string{"\x1b]0;" + string(make([]byte, 600)) + "\x07"}, "", false},
		{"a cut-off string does not swallow the next", []string{"\x1b]0;lost\x1b]0;kept\x07"}, "kept", true},
		{"plain output", []string{"hello ]0;not a title\x07"}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var scan titleScanner
			var got string
			var ok bool
			for _, chunk := range tt.chunks {
				if title, found := scan.write([]byte(chunk)); found {
					got, ok = title, true
				}
			}
			if got != tt.want || ok != tt.ok {
				t.Errorf("got %q, %v; want %q, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func waitActivity(t *testing.T, s *Session, want protocol.Activity) protocol.Session {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	for {
		info := s.Info()
		if info.Activity == want {
			return info
		}
		if time.Now().After(deadline) {
			t.Fatalf("activity never became %q", want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestActivityFollowsOutput(t *testing.T) {
	old := idleAfter
	idleAfter = 150 * time.Millisecond
	t.Cleanup(func() { idleAfter = old })

	m, _ := newManager(t)
	s := startHelper(t, m, ptytest.Echo)
	out := follow(t, s)
	out.waitFor("ready")

	if info := s.Info(); info.ActivityAt == "" {
		t.Fatal("a new session has no activity time")
	}
	idle := waitActivity(t, s, protocol.ActivityIdle)

	s.Input([]byte("hello\r"))
	out.waitFor(`got:"hello"`)
	// The answer may be read and gone quiet again before this looks, so
	// what proves the session was busy is that its idle time moved.
	again := waitActivity(t, s, protocol.ActivityIdle)
	deadline := time.Now().Add(waitLimit)
	for again.ActivityAt == idle.ActivityAt {
		if time.Now().After(deadline) {
			t.Fatal("output did not make the session busy")
		}
		time.Sleep(5 * time.Millisecond)
		again = waitActivity(t, s, protocol.ActivityIdle)
	}

	s.Input([]byte("exit\r"))
	deadline = time.Now().Add(waitLimit)
	for s.Info().Status != protocol.StatusExited {
		if time.Now().After(deadline) {
			t.Fatal("the session never exited")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := s.Info().Activity; got != protocol.ActivityIdle {
		t.Errorf("an exited session is %q, want idle", got)
	}
}
