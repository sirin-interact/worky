package session

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirin-interact/worky/daemon/internal/protocol"
	"github.com/sirin-interact/worky/daemon/internal/ptytest"
	"github.com/sirin-interact/worky/daemon/internal/termlog"
)

func TestMain(m *testing.M) {
	ptytest.RunHelper()
	os.Exit(m.Run())
}

const waitLimit = 15 * time.Second

// workDir is where the current test's sessions run.
var workDir string

// output collects everything a session printed.
type output struct {
	t      *testing.T
	s      *Session
	cursor termlog.Cursor
	text   strings.Builder
	// deadline, when set, replaces the usual limit on a wait.
	deadline time.Time
}

func follow(t *testing.T, s *Session) *output {
	return &output{t: t, s: s, cursor: s.Log.Open(nil, nil).Cursor}
}

// waitFor reads output until it contains want.
func (o *output) waitFor(want string) {
	o.t.Helper()
	deadline := time.Now().Add(waitLimit)
	if !o.deadline.IsZero() {
		deadline = o.deadline
	}
	for {
		for {
			item, ok := o.s.Log.Next(&o.cursor, nil, 4096)
			if !ok {
				break
			}
			if item.Kind == termlog.Output {
				o.text.Write(item.Data)
			}
		}
		if strings.Contains(o.text.String(), want) {
			return
		}
		if time.Now().After(deadline) {
			o.t.Fatalf("output never contained %q; got %q", want, o.text.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// events records what a Manager emits.
type events struct {
	mu   sync.Mutex
	seen []string
}

func (e *events) record(event string, data any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch d := data.(type) {
	case protocol.SessionResult:
		owner := "-"
		if d.Session.SizeOwner != nil {
			owner = *d.Session.SizeOwner
		}
		e.seen = append(e.seen, event+" "+string(d.Session.Status)+" "+owner)
	default:
		e.seen = append(e.seen, event)
	}
}

func (e *events) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	for {
		e.mu.Lock()
		found := slices.Contains(e.seen, want)
		seen := slices.Clone(e.seen)
		e.mu.Unlock()
		if found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("never saw event %q; got %q", want, seen)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func newManager(t *testing.T) (*Manager, *events) {
	// Made before Shutdown is registered, so it is removed after the sessions
	// running in it are gone.
	workDir = t.TempDir()
	m := NewManager(termlog.Options{})
	t.Cleanup(m.Shutdown)
	ev := &events{}
	m.Subscribe(ev.record)
	return m, ev
}

func startHelper(t *testing.T, m *Manager, mode string) *Session {
	t.Helper()
	argv, env := ptytest.Command(mode)
	s, perr := m.Create(protocol.SessionCreateParams{
		Cwd: workDir, Cols: 80, Rows: 24, Command: argv, Env: env,
	})
	if perr != nil {
		t.Fatalf("create: %v", perr)
	}
	return s
}

func TestSessionRunsAndEchoes(t *testing.T) {
	m, ev := newManager(t)
	s := startHelper(t, m, ptytest.Echo)
	out := follow(t, s)

	ev.waitFor(t, "session.created running -")
	out.waitFor("ready")

	s.Input([]byte("hello\r"))
	out.waitFor(`got:"hello"`)

	// Multi-byte text survives the trip in both directions.
	s.Input([]byte("한글 입력\r"))
	out.waitFor(`got:"한글 입력"`)

	if got := m.List(); len(got) != 1 || got[0].ID != s.Info().ID {
		t.Fatalf("List() = %+v", got)
	}
}

func TestSessionExit(t *testing.T) {
	m, ev := newManager(t)
	s := startHelper(t, m, ptytest.Echo)
	out := follow(t, s)
	out.waitFor("ready")

	s.Input([]byte("exit\r"))
	ev.waitFor(t, "session.updated exited -")

	info := s.Info()
	if info.ExitCode == nil || *info.ExitCode != 3 {
		t.Fatalf("exit code = %v, want 3", info.ExitCode)
	}
	// An exited session stays listed, and input for it is dropped quietly.
	if len(m.List()) != 1 {
		t.Fatalf("exited session is no longer listed")
	}
	s.Input([]byte("ignored\r"))
}

// What a program prints just before it exits is kept, even after a silence.
func TestLastOutputBeforeExit(t *testing.T) {
	m, ev := newManager(t)
	s := startHelper(t, m, ptytest.Echo)
	out := follow(t, s)
	out.waitFor("ready")

	s.Input([]byte(ptytest.LastWordsLine + "\r"))
	ev.waitFor(t, "session.updated exited -")
	// Everything is in the log by the time the session counts as exited.
	out.deadline = time.Now()
	out.waitFor(ptytest.LastWords)
}

// A relative command is looked for in the session's directory.
func TestRelativeCommand(t *testing.T) {
	m, _ := newManager(t)
	argv, env := ptytest.Command(ptytest.Echo)
	dir, file := filepath.Split(argv[0])

	s, perr := m.Create(protocol.SessionCreateParams{
		Cwd: dir, Cols: 80, Rows: 24, Command: []string{"./" + file}, Env: env,
	})
	if perr != nil {
		t.Fatalf("create: %v", perr)
	}
	follow(t, s).waitFor("ready")
}

// Events about one session arrive in the order created, updated, closed, also
// when its process is gone at once.
func TestEventOrder(t *testing.T) {
	m, _ := newManager(t)
	var mu sync.Mutex
	seen := map[string][]string{}
	m.Subscribe(func(event string, data any) {
		mu.Lock()
		defer mu.Unlock()
		switch d := data.(type) {
		case protocol.SessionResult:
			seen[d.Session.ID] = append(seen[d.Session.ID], event)
		case protocol.SessionIDParams:
			seen[d.SessionID] = append(seen[d.SessionID], event)
		}
	})

	argv, env := ptytest.Command(ptytest.Echo)
	var wg sync.WaitGroup
	for range 12 {
		s, perr := m.Create(protocol.SessionCreateParams{Cwd: workDir, Cols: 80, Rows: 24, Command: argv, Env: env})
		if perr != nil {
			t.Fatalf("create: %v", perr)
		}
		wg.Add(2)
		go func() {
			defer wg.Done()
			s.Input([]byte("exit\r"))
			s.Rename("renamed")
			_, _ = s.Resize(s, "c_1", 90, 20)
		}()
		go func() {
			defer wg.Done()
			time.Sleep(time.Duration(len(s.Info().ID)%7) * 10 * time.Millisecond)
			_ = m.Close(s.Info().ID)
		}()
	}
	wg.Wait()
	// Let updates that lost the race against a close show up.
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 12 {
		t.Fatalf("saw events for %d sessions, want 12", len(seen))
	}
	for id, events := range seen {
		last := len(events) - 1
		for i, event := range events {
			ok := event == protocol.EventSessionUpdated
			if i == 0 {
				ok = event == protocol.EventSessionCreated
			} else if i == last {
				ok = event == protocol.EventSessionClosed
			}
			if !ok {
				t.Fatalf("session %s: events %q", id, events)
			}
		}
	}
}

func TestResizeTakesOwnership(t *testing.T) {
	m, ev := newManager(t)
	s := startHelper(t, m, ptytest.Echo)
	out := follow(t, s)
	out.waitFor("ready")

	first, second := new(int), new(int)
	since, perr := s.Resize(first, "c_1", 100, 30)
	if perr != nil {
		t.Fatalf("resize: %v", perr)
	}
	ev.waitFor(t, "session.updated running c_1")
	if info := s.Info(); info.Cols != 100 || info.Rows != 30 {
		t.Fatalf("size = %dx%d", info.Cols, info.Rows)
	}
	if !s.IsOwner(first) || s.IsOwner(second) {
		t.Fatalf("ownership is wrong after the first resize")
	}
	// The owner may answer queries in output from here on. Resizing again
	// does not move that point.
	if since == 0 || since > s.Log.Head() {
		t.Fatalf("owner since offset %d, log ends at %d", since, s.Log.Head())
	}
	s.Input([]byte("more\r"))
	out.waitFor(`got:"more"`)
	if again, _ := s.Resize(first, "c_1", 101, 30); again != since {
		t.Fatalf("owner since offset %d after a second resize, want %d", again, since)
	}

	// Another client resizing takes over, from where the log ends now.
	taken, perr := s.Resize(second, "c_2", 100, 30)
	if perr != nil {
		t.Fatalf("resize: %v", perr)
	}
	if taken <= since {
		t.Fatalf("second owner since offset %d, first since %d", taken, since)
	}
	ev.waitFor(t, "session.updated running c_2")
	if s.IsOwner(first) || !s.IsOwner(second) {
		t.Fatalf("ownership did not move")
	}

	// Only the owner can release.
	s.Release(first)
	if !s.IsOwner(second) {
		t.Fatalf("a non-owner released the size")
	}
	s.Release(second)
	ev.waitFor(t, "session.updated running -")
	if info := s.Info(); info.SizeOwner != nil || info.Cols != 100 {
		t.Fatalf("after release: %+v", info)
	}

	if _, perr := s.Resize(first, "c_1", 0, 30); perr == nil || perr.Code != protocol.ErrInvalidRequest {
		t.Fatalf("resize to zero columns: %v", perr)
	}
}

func TestCloseSession(t *testing.T) {
	m, ev := newManager(t)
	s := startHelper(t, m, ptytest.Echo)
	follow(t, s).waitFor("ready")
	id := s.Info().ID

	if perr := m.Close(id); perr != nil {
		t.Fatalf("close: %v", perr)
	}
	ev.waitFor(t, "session.closed")
	if _, ok := m.Get(id); ok || len(m.List()) != 0 {
		t.Fatalf("closed session is still known")
	}
	if !s.Closed() {
		t.Fatalf("Closed() = false")
	}
	if perr := m.Close(id); perr == nil || perr.Code != protocol.ErrNotFound {
		t.Fatalf("second close: %v", perr)
	}
}

func TestCreateErrors(t *testing.T) {
	m, _ := newManager(t)
	argv, env := ptytest.Command(ptytest.Echo)

	tests := []struct {
		name   string
		params protocol.SessionCreateParams
		want   protocol.ErrorCode
	}{
		{"missing cwd", protocol.SessionCreateParams{Cwd: "/no/such/dir/anywhere", Cols: 80, Rows: 24, Command: argv, Env: env}, protocol.ErrInvalidRequest},
		{"zero size", protocol.SessionCreateParams{Cwd: workDir, Cols: 0, Rows: 24, Command: argv, Env: env}, protocol.ErrInvalidRequest},
		{"unknown command", protocol.SessionCreateParams{Cwd: workDir, Cols: 80, Rows: 24, Command: []string{"worky-no-such-command"}}, protocol.ErrSpawnFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, perr := m.Create(tt.params)
			if perr == nil || perr.Code != tt.want {
				t.Fatalf("error = %v, want code %s", perr, tt.want)
			}
		})
	}
	if len(m.List()) != 0 {
		t.Fatalf("a failed create left a session behind")
	}
}

func TestDaemonAnswersDeviceAttributesWithoutAnOwner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the console host handles the reply itself, so the program never sees it")
	}
	m, _ := newManager(t)
	s := startHelper(t, m, ptytest.AskDeviceAttributes)
	out := follow(t, s)
	out.waitFor("ready")

	// The answer has no line ending. Finish the line to make the helper
	// print what it received.
	s.Input([]byte("\r"))
	out.waitFor(`got:"\x1b[?1;2c"`)
}

func TestBuildEnv(t *testing.T) {
	env := buildEnv([]string{"A=1", "B=2", "=broken", "noequals"}, map[string]string{"B": "3", "C": "4"})
	got := map[string]string{}
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if _, dup := got[key]; dup {
			t.Fatalf("variable %s appears twice in %q", key, env)
		}
		got[key] = value
	}
	if got["A"] != "1" || got["B"] != "3" || got["C"] != "4" {
		t.Fatalf("env = %q", env)
	}
	if runtime.GOOS != "windows" && got["TERM"] != "xterm-256color" {
		t.Fatalf("TERM = %q", got["TERM"])
	}
}
