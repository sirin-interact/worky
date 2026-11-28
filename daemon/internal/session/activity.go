package session

import (
	"strings"
	"time"
	"unicode"

	"github.com/sirin-interact/worky/daemon/internal/protocol"
)

// A session is busy while its process produces output and idle once it has
// been quiet for idleAfter. An agent that stops printing is, as a rule,
// waiting for its user, and that holds for any program, not for one agent.
var idleAfter = 2 * time.Second

const (
	// A program that animates its title is reported at most this often.
	titleEvery = time.Second
	maxTitle   = 256
)

type scanState uint8

const (
	scanGround scanState = iota
	scanEsc              // after ESC
	scanOsc              // after ESC ], up to BEL or ST
	scanOscEsc           // ESC seen inside the string, maybe the start of ST
)

// titleScanner picks the window titles a program sets out of its output: the
// operating system commands 0 and 2. It reads the output and changes nothing.
type titleScanner struct {
	st   scanState
	buf  []byte
	over bool // the string is too long to be a title
}

// write feeds output to the scanner. It returns the last title set in p.
func (t *titleScanner) write(p []byte) (title string, ok bool) {
	for _, b := range p {
		switch t.st {
		case scanGround:
			if b == 0x1b {
				t.st = scanEsc
			}
		case scanEsc:
			switch b {
			case ']':
				t.st = scanOsc
				t.buf = t.buf[:0]
				t.over = false
			case 0x1b:
			default:
				t.st = scanGround
			}
		case scanOsc:
			switch b {
			case 0x07:
				if s, found := t.finish(); found {
					title, ok = s, true
				}
				t.st = scanGround
			case 0x1b:
				t.st = scanOscEsc
			default:
				if len(t.buf) < maxTitle+8 {
					t.buf = append(t.buf, b)
				} else {
					t.over = true
				}
			}
		case scanOscEsc:
			if b == '\\' {
				if s, found := t.finish(); found {
					title, ok = s, true
				}
				t.st = scanGround
			} else if b == ']' {
				// The string was cut off and another one starts.
				t.st = scanOsc
				t.buf = t.buf[:0]
				t.over = false
			} else {
				t.st = scanGround
			}
		}
	}
	return title, ok
}

func (t *titleScanner) finish() (string, bool) {
	if t.over {
		return "", false
	}
	command, text, found := strings.Cut(string(t.buf), ";")
	if !found || (command != "0" && command != "2") {
		return "", false
	}
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(text, ""))
	return strings.TrimSpace(text), true
}

// observe notes output the process just wrote. It reports whether the
// session's activity changed. The caller holds s.mu.
func (s *Session) observe(p []byte) (changed bool) {
	if s.closed {
		return false
	}
	if title, ok := s.scan.write(p); ok && title != s.info.Title {
		s.info.Title = title
		if s.titleTimer == nil {
			s.titleTimer = time.AfterFunc(titleEvery, s.flushTitle)
		}
	}
	s.quiet.Reset(idleAfter)
	if s.info.Activity == protocol.ActivityBusy || s.info.Status != protocol.StatusRunning {
		return false
	}
	s.setActivity(protocol.ActivityBusy)
	return true
}

// setActivity records a change of activity. The caller holds s.mu.
func (s *Session) setActivity(a protocol.Activity) {
	s.info.Activity = a
	s.info.ActivityAt = time.Now().UTC().Format(time.RFC3339Nano)
}

// goIdle runs when the quiet timer fires.
func (s *Session) goIdle() {
	s.mu.Lock()
	if s.closed || s.info.Activity != protocol.ActivityBusy {
		s.mu.Unlock()
		return
	}
	// The timer may have fired just as more output arrived.
	if quiet := time.Since(time.Unix(0, s.lastRead.Load())); quiet < idleAfter {
		s.quiet.Reset(idleAfter - quiet)
		s.mu.Unlock()
		return
	}
	s.setActivity(protocol.ActivityIdle)
	s.mu.Unlock()

	s.mgr.emitUpdated(s)
}

// flushTitle reports the title once the program has had time to settle on it.
func (s *Session) flushTitle() {
	s.mu.Lock()
	s.titleTimer = nil
	closed := s.closed
	s.mu.Unlock()

	if !closed {
		s.mgr.emitUpdated(s)
	}
}

// stopTimers ends the session's timers. The caller holds s.mu.
func (s *Session) stopTimers() {
	s.quiet.Stop()
	if s.titleTimer != nil {
		s.titleTimer.Stop()
		s.titleTimer = nil
	}
}
