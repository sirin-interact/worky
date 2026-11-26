// Package session runs processes in PTYs and keeps them alive between client
// connections.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/x/xpty"

	"github.com/sirin-interact/worky/daemon/internal/protocol"
	"github.com/sirin-interact/worky/daemon/internal/termlog"
)

const (
	maxDimension = 4096

	// What a terminal built on xterm.js answers to a primary device
	// attributes query. The daemon sends it when no client can.
	primaryDAReply = "\x1b[?1;2c"

	// After the process exits, the PTY is closed once it has been quiet for
	// drainQuiet, or after drainLimit at the latest.
	drainQuiet = 50 * time.Millisecond
	drainLimit = 2 * time.Second

	// How long closing a session waits for its process to be gone.
	closeLimit = 3 * time.Second
)

// Manager owns every session of a daemon.
type Manager struct {
	logOptions termlog.Options

	mu       sync.Mutex
	sessions map[string]*Session
	order    []*Session
	subs     map[uint64]func(event string, data any)
	nextSub  uint64
}

// NewManager returns a Manager with no sessions. logOptions sets how much
// output each session keeps; its size fields are ignored.
func NewManager(logOptions termlog.Options) *Manager {
	return &Manager{
		logOptions: logOptions,
		sessions:   make(map[string]*Session),
		subs:       make(map[uint64]func(string, any)),
	}
}

// Subscribe calls fn for every session event until the returned function is
// called. fn must not block.
func (m *Manager) Subscribe(fn func(event string, data any)) (unsubscribe func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.nextSub
	m.nextSub++
	m.subs[id] = fn
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.subs, id)
	}
}

func (m *Manager) emit(event string, data any) {
	m.mu.Lock()
	subs := make([]func(string, any), 0, len(m.subs))
	for _, fn := range m.subs {
		subs = append(subs, fn)
	}
	m.mu.Unlock()
	for _, fn := range subs {
		fn(event, data)
	}
}

// List returns every session, oldest first.
func (m *Manager) List() []protocol.Session {
	m.mu.Lock()
	sessions := append([]*Session(nil), m.order...)
	m.mu.Unlock()

	out := make([]protocol.Session, len(sessions))
	for i, s := range sessions {
		out[i] = s.Info()
	}
	return out
}

// Get returns the session with the given id.
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	return s, ok
}

func validSize(cols, rows int) bool {
	return cols >= 1 && cols <= maxDimension && rows >= 1 && rows <= maxDimension
}

func protocolError(code protocol.ErrorCode, format string, args ...any) *protocol.Error {
	return &protocol.Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Create starts a process in a new PTY.
func (m *Manager) Create(p protocol.SessionCreateParams) (*Session, *protocol.Error) {
	if !validSize(p.Cols, p.Rows) {
		return nil, protocolError(protocol.ErrInvalidRequest, "size %dx%d is out of range", p.Cols, p.Rows)
	}
	if info, err := os.Stat(p.Cwd); err != nil || !info.IsDir() {
		return nil, protocolError(protocol.ErrInvalidRequest, "cwd %q is not a directory", p.Cwd)
	}

	argv := p.Command
	if len(argv) == 0 {
		argv = defaultShell()
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return nil, protocolError(protocol.ErrSpawnFailed, "%v", err)
	}

	pty, err := xpty.NewPty(p.Cols, p.Rows)
	if err != nil {
		return nil, protocolError(protocol.ErrInternal, "open pty: %v", err)
	}

	cmd := exec.Command(path, argv[1:]...)
	cmd.Dir = p.Cwd
	cmd.Env = buildEnv(os.Environ(), p.Env)
	cmd.SysProcAttr = sysProcAttr()
	if err := pty.Start(cmd); err != nil {
		_ = pty.Close()
		return nil, protocolError(protocol.ErrSpawnFailed, "%v", err)
	}
	// The child has its own copy of the slave end. Dropping ours lets reads
	// from the master end when the last process using the terminal is gone.
	if unix, ok := pty.(*xpty.UnixPty); ok {
		_ = unix.Slave().Close()
	}

	name := p.Name
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(argv[0]), filepath.Ext(argv[0]))
	}
	logOptions := m.logOptions
	logOptions.Cols, logOptions.Rows = uint16(p.Cols), uint16(p.Rows)

	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{
		mgr:    m,
		Log:    termlog.New(logOptions),
		pty:    pty,
		cmd:    cmd,
		cancel: cancel,
		input:  make(chan []byte, 256),
		done:   make(chan struct{}),
		info: protocol.Session{
			ID:        newID("s_"),
			Name:      name,
			Cwd:       p.Cwd,
			Command:   argv,
			Status:    protocol.StatusRunning,
			Cols:      p.Cols,
			Rows:      p.Rows,
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		},
	}
	s.lastRead.Store(time.Now().UnixNano())

	m.mu.Lock()
	m.sessions[s.info.ID] = s
	m.order = append(m.order, s)
	m.mu.Unlock()

	readDone := make(chan struct{})
	go s.readLoop(readDone)
	go s.writeLoop(ctx)
	go s.waitLoop(ctx, readDone)

	m.emit(protocol.EventSessionCreated, protocol.SessionResult{Session: s.Info()})
	return s, nil
}

// Close ends a session and forgets it.
func (m *Manager) Close(id string) *protocol.Error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
		for i, other := range m.order {
			if other == s {
				m.order = append(m.order[:i], m.order[i+1:]...)
				break
			}
		}
	}
	m.mu.Unlock()
	if !ok {
		return protocolError(protocol.ErrNotFound, "no session %q", id)
	}

	s.close()
	m.emit(protocol.EventSessionClosed, protocol.SessionIDParams{SessionID: id})
	return nil
}

// Shutdown ends every session.
func (m *Manager) Shutdown() {
	for _, info := range m.List() {
		m.Close(info.ID)
	}
}

// Session is one process running in a PTY.
type Session struct {
	// Log holds the session's output.
	Log *termlog.Log

	mgr    *Manager
	pty    xpty.Pty
	cmd    *exec.Cmd
	cancel context.CancelFunc
	input  chan []byte
	done   chan struct{} // closed when the process is gone and its output is logged

	lastRead atomic.Int64 // unix nanoseconds

	mu     sync.Mutex
	info   protocol.Session
	owner  any // key of the size owner, nil when there is none
	closed bool
}

// Info returns a snapshot of the session.
func (s *Session) Info() protocol.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}

// Closed reports whether the session has been closed.
func (s *Session) Closed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Session) readLoop(done chan<- struct{}) {
	defer close(done)
	buf := make([]byte, 32<<10)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			s.lastRead.Store(time.Now().UnixNano())
			queries := s.Log.Write(buf[:n])
			// Some programs wait for this answer before they start, and
			// without a size owner no client will send it.
			if queries > 0 && !s.hasOwner() {
				s.Input([]byte(strings.Repeat(primaryDAReply, queries)))
			}
		}
		if err != nil {
			return
		}
	}
}

// writeLoop writes queued input to the PTY. A process that stops reading
// blocks only this goroutine.
func (s *Session) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case p := <-s.input:
			if _, err := s.pty.Write(p); err != nil {
				return
			}
		}
	}
}

func (s *Session) waitLoop(ctx context.Context, readDone <-chan struct{}) {
	_ = xpty.WaitProcess(ctx, s.cmd)

	// Let the last output arrive before closing the PTY under the reader.
	deadline := time.Now().Add(drainLimit)
drain:
	for time.Now().Before(deadline) {
		quiet := time.Since(time.Unix(0, s.lastRead.Load()))
		if quiet >= drainQuiet {
			break
		}
		select {
		case <-readDone:
			break drain
		case <-time.After(drainQuiet - quiet):
		}
	}
	_ = s.pty.Close()
	<-readDone
	s.cancel()
	close(s.done)

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.info.Status = protocol.StatusExited
	if state := s.cmd.ProcessState; state != nil {
		code := state.ExitCode()
		s.info.ExitCode = &code
	}
	info := s.info
	s.mu.Unlock()

	s.Log.Wake()
	s.mgr.emit(protocol.EventSessionUpdated, protocol.SessionResult{Session: info})
}

func (s *Session) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()

	s.cancel()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	_ = s.pty.Close()
	// Callers go on to delete the directory the process ran in, which fails
	// on Windows while the process still exists.
	select {
	case <-s.done:
	case <-time.After(closeLimit):
		slog.Warn("session process did not exit in time", "session", s.info.ID)
	}
	s.Log.Wake()
}

// Input queues bytes to be written to the PTY. Input for a session that has
// exited, or that has stopped reading for a long time, is dropped.
func (s *Session) Input(p []byte) {
	if len(p) == 0 {
		return
	}
	s.mu.Lock()
	live := !s.closed && s.info.Status == protocol.StatusRunning
	s.mu.Unlock()
	if !live {
		return
	}
	select {
	case s.input <- append([]byte(nil), p...):
	default:
		slog.Warn("dropping input for a session that is not reading", "session", s.info.ID)
	}
}

func (s *Session) hasOwner() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.owner != nil
}

// IsOwner reports whether key is the session's size owner.
func (s *Session) IsOwner(key any) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.owner != nil && s.owner == key
}

// Resize sets the session's size and makes key, which belongs to clientID,
// its size owner.
func (s *Session) Resize(key any, clientID string, cols, rows int) *protocol.Error {
	if !validSize(cols, rows) {
		return protocolError(protocol.ErrInvalidRequest, "size %dx%d is out of range", cols, rows)
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return protocolError(protocol.ErrNotFound, "session is closed")
	}
	sizeChanged := s.info.Cols != cols || s.info.Rows != rows
	ownerChanged := s.owner != key
	s.owner = key
	s.info.SizeOwner = &clientID
	s.info.Cols, s.info.Rows = cols, rows
	running := s.info.Status == protocol.StatusRunning
	info := s.info
	s.mu.Unlock()

	if sizeChanged {
		if running {
			if err := s.pty.Resize(cols, rows); err != nil {
				slog.Warn("resize pty", "session", info.ID, "err", err)
			}
		}
		s.Log.Resize(uint16(cols), uint16(rows))
	}
	if sizeChanged || ownerChanged {
		s.mgr.emit(protocol.EventSessionUpdated, protocol.SessionResult{Session: info})
	}
	return nil
}

// Release gives up size ownership if key holds it.
func (s *Session) Release(key any) {
	s.mu.Lock()
	if s.owner == nil || s.owner != key || s.closed {
		s.mu.Unlock()
		return
	}
	s.owner = nil
	s.info.SizeOwner = nil
	info := s.info
	s.mu.Unlock()

	s.mgr.emit(protocol.EventSessionUpdated, protocol.SessionResult{Session: info})
}

// Rename changes the session's name.
func (s *Session) Rename(name string) {
	s.mu.Lock()
	if s.closed || s.info.Name == name {
		s.mu.Unlock()
		return
	}
	s.info.Name = name
	info := s.info
	s.mu.Unlock()

	s.mgr.emit(protocol.EventSessionUpdated, protocol.SessionResult{Session: info})
}

func newID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // the system's random source is broken
	}
	return prefix + hex.EncodeToString(b[:])
}

// NewClientID returns an id for a client connection.
func NewClientID() string { return newID("c_") }

// defaultShell returns the command for the user's shell.
func defaultShell() []string {
	if runtime.GOOS == "windows" {
		for _, name := range []string{"pwsh.exe", "powershell.exe"} {
			if _, err := exec.LookPath(name); err == nil {
				return []string{name}
			}
		}
		if comspec := os.Getenv("COMSPEC"); comspec != "" {
			return []string{comspec}
		}
		return []string{"cmd.exe"}
	}
	if shell := os.Getenv("SHELL"); shell != "" {
		return []string{shell}
	}
	return []string{"/bin/sh"}
}

// buildEnv returns base with extra applied on top, one entry per variable.
// Off Windows it also describes the terminal the clients emulate.
func buildEnv(base []string, extra map[string]string) []string {
	fold := func(key string) string { return key }
	if runtime.GOOS == "windows" {
		fold = strings.ToUpper // names are case-insensitive there
	}

	index := make(map[string]int)
	var env []string
	set := func(key, value string) {
		entry := key + "=" + value
		if i, ok := index[fold(key)]; ok {
			env[i] = entry
			return
		}
		index[fold(key)] = len(env)
		env = append(env, entry)
	}

	for _, entry := range base {
		if key, value, ok := strings.Cut(entry, "="); ok && key != "" {
			set(key, value)
		}
	}
	if runtime.GOOS != "windows" {
		set("TERM", "xterm-256color")
		set("COLORTERM", "truecolor")
	}
	for key, value := range extra {
		set(key, value)
	}
	return env
}
