package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/sirin-interact/worky/daemon/internal/protocol"
	"github.com/sirin-interact/worky/daemon/internal/ptytest"
	"github.com/sirin-interact/worky/daemon/internal/session"
	"github.com/sirin-interact/worky/daemon/internal/termlog"
)

func TestMain(m *testing.M) {
	ptytest.RunHelper()
	os.Exit(m.Run())
}

const (
	testToken = "secret"
	waitLimit = 15 * time.Second
)

type testDaemon struct {
	url     string
	workDir string
}

func startDaemon(t *testing.T) *testDaemon {
	t.Helper()
	// Made first, so it is removed after the sessions running in it are gone.
	workDir := t.TempDir()
	sessions := session.NewManager(termlog.Options{})
	t.Cleanup(sessions.Shutdown)
	srv := httptest.NewServer(New(Options{Token: testToken, Version: "test", Sessions: sessions}))
	t.Cleanup(srv.Close)
	return &testDaemon{url: "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/ws", workDir: workDir}
}

func dial(t *testing.T, d *testDaemon, origin string) (*websocket.Conn, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	opts := &websocket.DialOptions{}
	if origin != "" {
		opts.HTTPHeader = http.Header{"Origin": {origin}}
	}
	ws, _, err := websocket.Dial(ctx, d.url, opts)
	if err == nil {
		ws.SetReadLimit(1 << 20)
		t.Cleanup(func() { ws.CloseNow() })
	}
	return ws, err
}

// terminal applies a channel's frames the way a client would, and notes
// anything the protocol does not allow.
type terminal struct {
	mu         sync.Mutex
	kinds      []string // every frame received, in order
	resets     int
	next       uint64 // offset the next output must have
	started    bool
	cols, rows uint16
	state      []byte // state frames since the last reset
	text       []byte // output since the last reset
	violations []string
}

func (tm *terminal) apply(f protocol.Frame) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	last := ""
	if n := len(tm.kinds); n > 0 {
		last = tm.kinds[n-1]
	}
	switch f.Type {
	case protocol.FrameReset:
		tm.kinds = append(tm.kinds, "reset")
		tm.resets++
		tm.started, tm.next = true, f.Offset
		tm.cols, tm.rows = f.Cols, f.Rows
		tm.state, tm.text = nil, nil
	case protocol.FrameState:
		tm.kinds = append(tm.kinds, "state")
		if last != "reset" && last != "state" {
			tm.violations = append(tm.violations, "state frame after "+last)
		}
		tm.state = append(tm.state, f.Payload...)
	case protocol.FrameOutput:
		tm.kinds = append(tm.kinds, "output")
		if !tm.started {
			tm.started, tm.next = true, f.Offset
		}
		if f.Offset != tm.next {
			tm.violations = append(tm.violations, fmt.Sprintf("output at offset %d, expected %d", f.Offset, tm.next))
		}
		tm.next = f.Offset + uint64(len(f.Payload))
		tm.text = append(tm.text, f.Payload...)
	case protocol.FrameResize:
		tm.kinds = append(tm.kinds, "resize")
		tm.cols, tm.rows = f.Cols, f.Rows
	default:
		tm.violations = append(tm.violations, fmt.Sprintf("frame type 0x%02x from the daemon", f.Type))
	}
}

type client struct {
	t  *testing.T
	ws *websocket.Conn
	id string

	mu      sync.Mutex
	nextID  uint64
	waiting map[uint64]chan protocol.Response
	terms   map[uint32]*terminal
	events  []protocol.Event
	done    chan struct{}
}

func connect(t *testing.T, d *testDaemon) *client {
	t.Helper()
	ws, err := dial(t, d, "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c := &client{
		t:       t,
		ws:      ws,
		waiting: make(map[uint64]chan protocol.Response),
		terms:   make(map[uint32]*terminal),
		done:    make(chan struct{}),
	}
	go c.readLoop()

	var hello protocol.HelloResult
	c.mustCall(protocol.MethodHello, protocol.HelloParams{
		Protocol: protocol.Supported,
		Token:    testToken,
		Client:   protocol.Program{Name: "test", Version: "0"},
	}, &hello)
	if hello.Protocol != protocol.MaxVersion || hello.ClientID == "" || hello.Daemon.Name != "workyd" || hello.Host.OS == "" {
		t.Fatalf("hello result = %+v", hello)
	}
	c.id = hello.ClientID
	return c
}

func (c *client) readLoop() {
	defer close(c.done)
	for {
		typ, data, err := c.ws.Read(context.Background())
		if err != nil {
			return
		}
		if typ == websocket.MessageBinary {
			frame, err := protocol.ParseFrame(data)
			if err != nil {
				c.t.Errorf("bad frame from the daemon: %v", err)
				continue
			}
			c.term(frame.Channel).apply(frame)
			continue
		}

		var msg struct {
			ID    *uint64 `json:"id"`
			Event string  `json:"event"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			c.t.Errorf("bad JSON from the daemon: %v", err)
			continue
		}
		c.mu.Lock()
		if msg.ID == nil {
			var ev protocol.Event
			_ = json.Unmarshal(data, &ev)
			c.events = append(c.events, ev)
		} else if ch, ok := c.waiting[*msg.ID]; ok {
			var resp protocol.Response
			_ = json.Unmarshal(data, &resp)
			delete(c.waiting, *msg.ID)
			ch <- resp
		} else {
			c.t.Errorf("response to unknown request %d", *msg.ID)
		}
		c.mu.Unlock()
	}
}

// term returns the terminal for a channel, making it on first use.
func (c *client) term(channel uint32) *terminal {
	c.mu.Lock()
	defer c.mu.Unlock()
	tm := c.terms[channel]
	if tm == nil {
		tm = &terminal{}
		c.terms[channel] = tm
	}
	return tm
}

func (c *client) call(method string, params, result any) *protocol.Error {
	c.t.Helper()
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan protocol.Response, 1)
	c.waiting[id] = ch
	c.mu.Unlock()

	req := protocol.Request{ID: id, Method: method}
	if params != nil {
		req.Params = mustMarshal(params)
	}
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	if err := c.ws.Write(ctx, websocket.MessageText, mustMarshal(req)); err != nil {
		c.t.Fatalf("%s: write: %v", method, err)
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		if result != nil {
			if err := json.Unmarshal(resp.Result, result); err != nil {
				c.t.Fatalf("%s: bad result %s: %v", method, resp.Result, err)
			}
		}
		return nil
	case <-c.done:
		c.t.Fatalf("%s: connection closed before the response", method)
	case <-time.After(waitLimit):
		c.t.Fatalf("%s: no response", method)
	}
	return nil
}

func (c *client) mustCall(method string, params, result any) {
	c.t.Helper()
	if perr := c.call(method, params, result); perr != nil {
		c.t.Fatalf("%s: %v", method, perr)
	}
}

func (c *client) sendFrame(f protocol.Frame) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	if err := c.ws.Write(ctx, websocket.MessageBinary, protocol.AppendFrame(nil, f)); err != nil {
		c.t.Fatalf("write frame: %v", err)
	}
}

func (c *client) input(channel uint32, text string) {
	c.t.Helper()
	c.sendFrame(protocol.Frame{Type: protocol.FrameInput, Channel: channel, Payload: []byte(text)})
}

func (c *client) attach(sessionID string, from, backlog *uint64) (uint32, protocol.TerminalAttachResult) {
	c.t.Helper()
	var res protocol.TerminalAttachResult
	c.mustCall(protocol.MethodTerminalAttach, protocol.TerminalAttachParams{
		SessionID: sessionID, FromOffset: from, Backlog: backlog,
	}, &res)
	return res.Channel, res
}

func poll(t *testing.T, what string, cond func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	for {
		ok, state := cond()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; %s", what, state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitText waits until the channel's output since its last reset contains want.
func (c *client) waitText(channel uint32, want string) {
	c.t.Helper()
	tm := c.term(channel)
	poll(c.t, fmt.Sprintf("output containing %q", want), func() (bool, string) {
		tm.mu.Lock()
		defer tm.mu.Unlock()
		return strings.Contains(string(tm.text), want), fmt.Sprintf("output so far: %q", tm.text)
	})
}

// waitSession waits for an event carrying a session that satisfies cond.
func (c *client) waitSession(event string, cond func(protocol.Session) bool) protocol.Session {
	c.t.Helper()
	var found protocol.Session
	poll(c.t, event, func() (bool, string) {
		c.mu.Lock()
		defer c.mu.Unlock()
		var names []string
		for _, ev := range c.events {
			names = append(names, ev.Event)
			if ev.Event != event {
				continue
			}
			var data protocol.SessionResult
			if json.Unmarshal(ev.Data, &data) == nil && cond(data.Session) {
				found = data.Session
				return true, ""
			}
		}
		return false, fmt.Sprintf("events so far: %q", names)
	})
	return found
}

func (c *client) snapshot(channel uint32) (kinds []string, text string, next uint64, cols, rows uint16) {
	tm := c.term(channel)
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return append([]string(nil), tm.kinds...), string(tm.text), tm.next, tm.cols, tm.rows
}

// checkStreams fails the test if any channel saw something the protocol
// forbids.
func (c *client) checkStreams() {
	c.t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, tm := range c.terms {
		tm.mu.Lock()
		for _, v := range tm.violations {
			c.t.Errorf("channel %d: %s", id, v)
		}
		tm.mu.Unlock()
	}
}

func (c *client) createEcho(d *testDaemon) protocol.Session {
	c.t.Helper()
	argv, env := ptytest.Command(ptytest.Echo)
	var res protocol.SessionResult
	c.mustCall(protocol.MethodSessionCreate, protocol.SessionCreateParams{
		Cwd: d.workDir, Cols: 80, Rows: 24, Command: argv, Env: env,
	}, &res)
	return res.Session
}

// readRaw reads one message with a deadline, for tests that speak the
// protocol by hand.
func readRaw(t *testing.T, ws *websocket.Conn) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	_, data, err := ws.Read(ctx)
	return data, err
}

func writeRaw(t *testing.T, ws *websocket.Conn, v any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	if err := ws.Write(ctx, websocket.MessageText, mustMarshal(v)); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func hello(token string, versions protocol.VersionRange) protocol.Request {
	return protocol.Request{ID: 1, Method: protocol.MethodHello, Params: mustMarshal(protocol.HelloParams{
		Protocol: versions, Token: token, Client: protocol.Program{Name: "test", Version: "0"},
	})}
}

func TestHandshakeRejections(t *testing.T) {
	d := startDaemon(t)

	tests := []struct {
		name      string
		first     protocol.Request
		wantError protocol.ErrorCode // empty when the daemon closes without answering
		wantClose int
	}{
		{"wrong token", hello("nope", protocol.Supported), protocol.ErrUnauthorized, protocol.CloseUnauthorized},
		{"no version in common", hello(testToken, protocol.VersionRange{Min: 98, Max: 99}), protocol.ErrUnsupportedProtocol, protocol.CloseProtocolViolation},
		{"first message is not hello", protocol.Request{ID: 1, Method: protocol.MethodSessionList}, "", protocol.CloseProtocolViolation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws, err := dial(t, d, "")
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			writeRaw(t, ws, tt.first)

			if tt.wantError != "" {
				data, err := readRaw(t, ws)
				if err != nil {
					t.Fatalf("read the error response: %v", err)
				}
				var resp protocol.Response
				if err := json.Unmarshal(data, &resp); err != nil || resp.Error == nil || resp.Error.Code != tt.wantError {
					t.Fatalf("response = %s, want error %s", data, tt.wantError)
				}
				if tt.wantError == protocol.ErrUnsupportedProtocol {
					var theirs protocol.VersionRange
					if json.Unmarshal(resp.Error.Data, &theirs) != nil || theirs != protocol.Supported {
						t.Fatalf("error data = %s, want the daemon's range", resp.Error.Data)
					}
				}
			}

			_, err = readRaw(t, ws)
			if got := int(websocket.CloseStatus(err)); got != tt.wantClose {
				t.Fatalf("close status = %d (%v), want %d", got, err, tt.wantClose)
			}
		})
	}
}

func TestOriginCheck(t *testing.T) {
	d := startDaemon(t)

	tests := []struct {
		origin string
		ok     bool
	}{
		{"", true},
		{"tauri://localhost", true},
		{"http://tauri.localhost", true},
		{"https://evil.example", false},
		{"http://localhost:1420", false}, // a dev server has to be allowed explicitly
	}
	for _, tt := range tests {
		_, err := dial(t, d, tt.origin)
		if (err == nil) != tt.ok {
			t.Errorf("origin %q: dial error = %v, want ok = %v", tt.origin, err, tt.ok)
		}
	}
}

func TestTerminalOverWebSocket(t *testing.T) {
	d := startDaemon(t)
	alice := connect(t, d)
	bob := connect(t, d)
	defer alice.checkStreams()
	defer bob.checkStreams()

	sess := alice.createEcho(d)
	if sess.Status != protocol.StatusRunning || sess.SizeOwner != nil || sess.Cols != 80 {
		t.Fatalf("created session = %+v", sess)
	}
	// Everyone hears about a new session, not only its creator.
	bob.waitSession(protocol.EventSessionCreated, func(s protocol.Session) bool { return s.ID == sess.ID })

	// A first attach starts with a reset at the beginning of the session.
	a, _ := alice.attach(sess.ID, nil, nil)
	alice.waitText(a, "ready")
	kinds, _, _, cols, rows := alice.snapshot(a)
	if kinds[0] != "reset" || cols != 80 || rows != 24 {
		t.Fatalf("stream began with %v at %dx%d", kinds, cols, rows)
	}

	alice.input(a, "hello\r")
	alice.waitText(a, `got:"hello"`)

	// Resizing makes alice the size owner, and the new size reaches her
	// terminal through the stream.
	alice.mustCall(protocol.MethodTerminalResize, protocol.TerminalResizeParams{Channel: a, Cols: 100, Rows: 30}, nil)
	bob.waitSession(protocol.EventSessionUpdated, func(s protocol.Session) bool {
		return s.SizeOwner != nil && *s.SizeOwner == alice.id && s.Cols == 100 && s.Rows == 30
	})
	poll(t, "the resize frame", func() (bool, string) {
		_, _, _, cols, rows := alice.snapshot(a)
		return cols == 100 && rows == 30, fmt.Sprintf("size is %dx%d", cols, rows)
	})

	// A second client replays the same session from the start and ends up
	// with the same output and size.
	b, res := bob.attach(sess.ID, nil, nil)
	if res.Head == 0 {
		t.Fatalf("attach head = 0 after output was produced")
	}
	bob.waitText(b, `got:"hello"`)
	poll(t, "bob's size", func() (bool, string) {
		_, _, _, cols, rows := bob.snapshot(b)
		return cols == 100 && rows == 30, fmt.Sprintf("size is %dx%d", cols, rows)
	})

	// Anyone attached may type.
	bob.input(b, "from-bob\r")
	alice.waitText(a, `got:"from-bob"`)

	// Replies: only the size owner's are written, and only for live output.
	_, _, bobNext, _, _ := bob.snapshot(b)
	bob.sendFrame(protocol.Frame{Type: protocol.FrameReply, Channel: b, Offset: bobNext, Payload: []byte("reply-bob\r")})
	bob.input(b, "after-bob\r")
	alice.waitText(a, `got:"after-bob"`)

	alice.sendFrame(protocol.Frame{Type: protocol.FrameReply, Channel: a, Offset: 0, Payload: []byte("reply-stale\r")})
	_, _, aliceNext, _, _ := alice.snapshot(a)
	alice.sendFrame(protocol.Frame{Type: protocol.FrameReply, Channel: a, Offset: aliceNext, Payload: []byte("reply-alice\r")})
	alice.waitText(a, `got:"reply-alice"`)

	_, text, _, _, _ := alice.snapshot(a)
	for _, dropped := range []string{"reply-bob", "reply-stale"} {
		if strings.Contains(text, dropped) {
			t.Errorf("%s reached the program; output: %q", dropped, text)
		}
	}

	// Detach and resume: the stream continues at the given offset, with no
	// reset and nothing missing.
	_, _, resumeAt, _, _ := bob.snapshot(b)
	bob.mustCall(protocol.MethodTerminalDetach, protocol.TerminalDetachParams{Channel: b}, nil)
	alice.input(a, "while-away\r")
	alice.waitText(a, `got:"while-away"`)

	b2, _ := bob.attach(sess.ID, &resumeAt, nil)
	if b2 == b {
		t.Fatalf("channel id %d was reused", b)
	}
	bob.waitText(b2, `got:"while-away"`)
	kinds, _, _, _, _ = bob.snapshot(b2)
	for _, kind := range kinds {
		if kind == "reset" || kind == "state" {
			t.Fatalf("resumed stream contained a %s frame: %v", kind, kinds)
		}
	}

	// A zero backlog starts at the end: a reset and then only new output.
	b3, _ := bob.attach(sess.ID, nil, new(uint64))
	alice.input(a, "newest\r")
	bob.waitText(b3, `got:"newest"`)
	kinds, text, _, cols, rows = bob.snapshot(b3)
	if kinds[0] != "reset" || cols != 100 || rows != 30 || strings.Contains(text, "while-away") {
		t.Fatalf("tail stream: frames %v, size %dx%d, output %q", kinds, cols, rows, text)
	}

	// Detaching a channel that does not exist is an error.
	if perr := bob.call(protocol.MethodTerminalDetach, protocol.TerminalDetachParams{Channel: b}, nil); perr == nil || perr.Code != protocol.ErrNotFound {
		t.Fatalf("detach of a gone channel: %v", perr)
	}

	// Closing the session reaches everyone and empties the list.
	bob.mustCall(protocol.MethodSessionClose, protocol.SessionIDParams{SessionID: sess.ID}, nil)
	poll(t, "session.closed at alice", func() (bool, string) {
		alice.mu.Lock()
		defer alice.mu.Unlock()
		for _, ev := range alice.events {
			if ev.Event == protocol.EventSessionClosed {
				return true, ""
			}
		}
		return false, ""
	})
	var list protocol.SessionListResult
	alice.mustCall(protocol.MethodSessionList, nil, &list)
	if list.Sessions == nil || len(list.Sessions) != 0 {
		t.Fatalf("sessions after close = %+v", list.Sessions)
	}
}

func TestOwnershipEndsWithTheConnection(t *testing.T) {
	d := startDaemon(t)
	alice := connect(t, d)
	bob := connect(t, d)

	sess := alice.createEcho(d)
	a, _ := alice.attach(sess.ID, nil, nil)
	alice.waitText(a, "ready")
	alice.mustCall(protocol.MethodTerminalResize, protocol.TerminalResizeParams{Channel: a, Cols: 90, Rows: 20}, nil)
	bob.waitSession(protocol.EventSessionUpdated, func(s protocol.Session) bool { return s.SizeOwner != nil })

	alice.ws.CloseNow()
	released := bob.waitSession(protocol.EventSessionUpdated, func(s protocol.Session) bool { return s.SizeOwner == nil })
	if released.Cols != 90 || released.Rows != 20 || released.Status != protocol.StatusRunning {
		t.Fatalf("after the owner left: %+v", released)
	}
}

func TestRequestErrors(t *testing.T) {
	d := startDaemon(t)
	c := connect(t, d)

	tests := []struct {
		name   string
		method string
		params any
		want   protocol.ErrorCode
	}{
		{"unknown method", "session.fly", nil, protocol.ErrUnknownMethod},
		{"second hello", protocol.MethodHello, nil, protocol.ErrInvalidRequest},
		{"attach to a missing session", protocol.MethodTerminalAttach, protocol.TerminalAttachParams{SessionID: "s_none"}, protocol.ErrNotFound},
		{"resize a missing channel", protocol.MethodTerminalResize, protocol.TerminalResizeParams{Channel: 9, Cols: 80, Rows: 24}, protocol.ErrNotFound},
		{"close a missing session", protocol.MethodSessionClose, protocol.SessionIDParams{SessionID: "s_none"}, protocol.ErrNotFound},
		{"malformed params", protocol.MethodSessionCreate, "not an object", protocol.ErrInvalidRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			perr := c.call(tt.method, tt.params, nil)
			if perr == nil || perr.Code != tt.want {
				t.Fatalf("error = %v, want code %s", perr, tt.want)
			}
		})
	}

	// The connection survives request errors.
	var list protocol.SessionListResult
	c.mustCall(protocol.MethodSessionList, nil, &list)
}
