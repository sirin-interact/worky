package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/sirin-interact/worky/daemon/internal/auth"
	"github.com/sirin-interact/worky/daemon/internal/protocol"
	"github.com/sirin-interact/worky/daemon/internal/session"
)

const (
	writeTimeout   = 30 * time.Second
	pingInterval   = 30 * time.Second
	maxJSONMessage = 1 << 20
	// A client that lets this many control messages pile up is dropped.
	controlQueue = 1024
	// How many requests of one client run beside its read loop at a time,
	// and how many more may wait their turn before the client is told it is
	// asking too much.
	maxBackground = 16
	maxWaiting    = 256
)

// outgoing is a JSON message on its way to the client.
type outgoing struct {
	data []byte
	// then, when set, runs in the writer once data has been written.
	then func()
}

// conn is one client connection.
type conn struct {
	srv    *server
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc

	clientID string

	control chan outgoing // JSON messages waiting to be sent
	wake    chan struct{} // signals that a channel may have a frame to send

	mu          sync.Mutex
	channels    map[uint32]*channel
	order       []*channel // active channels, in round-robin order for the writer
	turn        int
	nextChannel uint32
	watching    map[*session.Session]int
	watches     map[uint32]*dirWatch // nil once the connection is over
	nextWatch   uint32

	// slots holds one entry for each request running beside the read loop,
	// and waiting counts those together with the ones that wait for a slot.
	slots   chan struct{}
	waiting atomic.Int32
}

func (s *server) serveWebSocket(w http.ResponseWriter, r *http.Request) {
	if !s.originAllowed(r) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// The origin was checked above, against this server's own list.
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	ws.SetReadLimit(maxJSONMessage)

	ctx, cancel := context.WithCancel(r.Context())
	c := &conn{
		srv:      s,
		ws:       ws,
		ctx:      ctx,
		cancel:   cancel,
		control:  make(chan outgoing, controlQueue),
		wake:     make(chan struct{}, 1),
		channels: make(map[uint32]*channel),
		watching: make(map[*session.Session]int),
		watches:  make(map[uint32]*dirWatch),
		slots:    make(chan struct{}, maxBackground),
	}
	c.serve()
}

func (c *conn) serve() {
	defer c.ws.CloseNow()
	defer c.cancel()

	if !c.handshake() {
		return
	}

	forward := func(event string, data any) {
		c.send(protocol.Event{Event: event, Data: mustMarshal(data)})
	}
	defer c.srv.Sessions.Subscribe(forward)()
	defer c.srv.Projects.Subscribe(forward)()
	defer c.detachAll()
	defer c.unwatchAll()

	go c.writeLoop()
	go c.pingLoop()
	c.readLoop()
}

// closeWith ends the connection with a protocol close code.
func (c *conn) closeWith(code int, reason string) {
	_ = c.ws.Close(websocket.StatusCode(code), reason)
}

// handshake reads the hello request and answers it. It reports whether the
// connection may go on.
func (c *conn) handshake() bool {
	// A read whose context expires drops the connection with no close code,
	// so the wait for hello is ended by closing instead.
	timer := time.AfterFunc(c.srv.HelloTimeout, func() {
		c.closeWith(protocol.CloseProtocolViolation, "no hello")
	})
	typ, data, err := c.ws.Read(c.ctx)
	if !timer.Stop() || err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
	defer cancel()

	var req protocol.Request
	if typ != websocket.MessageText || json.Unmarshal(data, &req) != nil || req.Method != protocol.MethodHello {
		c.closeWith(protocol.CloseProtocolViolation, "the first message must be hello")
		return false
	}

	reject := func(closeCode int, perr *protocol.Error) bool {
		_ = c.ws.Write(ctx, websocket.MessageText, mustMarshal(protocol.Response{ID: req.ID, Error: perr}))
		c.closeWith(closeCode, string(perr.Code))
		return false
	}

	var params protocol.HelloParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return reject(protocol.CloseProtocolViolation, &protocol.Error{
			Code: protocol.ErrInvalidRequest, Message: "malformed hello",
		})
	}
	if !auth.Equal(params.Token, c.srv.Token) {
		return reject(protocol.CloseUnauthorized, &protocol.Error{
			Code: protocol.ErrUnauthorized, Message: "wrong token",
		})
	}
	version, ok := protocol.Negotiate(params.Protocol, protocol.Supported)
	if !ok {
		return reject(protocol.CloseProtocolViolation, &protocol.Error{
			Code:    protocol.ErrUnsupportedProtocol,
			Message: "no protocol version in common",
			Data:    mustMarshal(protocol.Supported),
		})
	}

	c.clientID = session.NewClientID()
	hostname, _ := os.Hostname()
	result := protocol.HelloResult{
		Protocol: version,
		ClientID: c.clientID,
		Daemon:   protocol.Program{Name: "workyd", Version: c.srv.Version},
		Host:     protocol.Host{Hostname: hostname, OS: runtime.GOOS},
	}
	err = c.ws.Write(ctx, websocket.MessageText, mustMarshal(protocol.Response{ID: req.ID, Result: mustMarshal(result)}))
	return err == nil
}

func mustMarshal(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err) // every value marshalled here is a plain protocol struct
	}
	return data
}

// send queues a JSON message. A client too slow to take them is dropped.
func (c *conn) send(v any) { c.sendThen(v, nil) }

// sendThen queues a JSON message, and has the writer run then right after it
// has written the message.
func (c *conn) sendThen(v any, then func()) {
	select {
	case c.control <- outgoing{data: mustMarshal(v), then: then}:
	default:
		slog.Warn("dropping a client that is not reading", "client", c.clientID)
		c.cancel()
	}
}

func (c *conn) readLoop() {
	for {
		typ, data, err := c.ws.Read(c.ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageBinary {
			if !c.handleFrame(data) {
				return
			}
			continue
		}

		var req protocol.Request
		if err := json.Unmarshal(data, &req); err != nil || req.Method == "" {
			c.closeWith(protocol.CloseProtocolViolation, "malformed request")
			return
		}
		if work := c.background(req); work != nil {
			c.start(req.ID, work)
			continue
		}
		result, then, perr := c.dispatch(req)
		if perr != nil {
			c.send(protocol.Response{ID: req.ID, Error: perr})
			continue
		}
		c.sendThen(protocol.Response{ID: req.ID, Result: mustMarshal(result)}, then)
	}
}

// handleFrame acts on a binary frame from the client. It reports false when
// the connection must end.
func (c *conn) handleFrame(data []byte) bool {
	frame, err := protocol.ParseFrame(data)
	if errors.Is(err, protocol.ErrUnknownFrame) {
		return true
	}
	if err != nil {
		c.closeWith(protocol.CloseProtocolViolation, "malformed frame")
		return false
	}

	c.mu.Lock()
	ch := c.channels[frame.Channel]
	accept := ch != nil
	if accept && frame.Type == protocol.FrameReply {
		accept = ch.acceptsReply(frame.Offset) && ch.sess.IsOwner(ch)
	}
	c.mu.Unlock()

	if accept && (frame.Type == protocol.FrameInput || frame.Type == protocol.FrameReply) {
		ch.sess.Input(frame.Payload)
	}
	return true
}

func decode[T any](req protocol.Request) (T, *protocol.Error) {
	var params T
	if len(req.Params) == 0 {
		return params, nil
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return params, &protocol.Error{Code: protocol.ErrInvalidRequest, Message: "malformed params: " + err.Error()}
	}
	return params, nil
}

type empty struct{}

// dispatch runs a request that is answered in order. then, when set, runs in
// the writer after the response has been written.
func (c *conn) dispatch(req protocol.Request) (result any, then func(), perr *protocol.Error) {
	sessions := c.srv.Sessions
	find := func(id string) (*session.Session, *protocol.Error) {
		if s, ok := sessions.Get(id); ok {
			return s, nil
		}
		return nil, &protocol.Error{Code: protocol.ErrNotFound, Message: "no session " + id}
	}

	switch req.Method {
	case protocol.MethodSessionList:
		return protocol.SessionListResult{Sessions: sessions.List()}, nil, nil

	case protocol.MethodSessionCreate:
		params, perr := decode[protocol.SessionCreateParams](req)
		if perr != nil {
			return nil, nil, perr
		}
		s, perr := sessions.Create(params)
		if perr != nil {
			return nil, nil, perr
		}
		return protocol.SessionResult{Session: s.Info()}, nil, nil

	case protocol.MethodSessionRename:
		params, perr := decode[protocol.SessionRenameParams](req)
		if perr != nil {
			return nil, nil, perr
		}
		s, perr := find(params.SessionID)
		if perr != nil {
			return nil, nil, perr
		}
		s.Rename(params.Name)
		return empty{}, nil, nil

	case protocol.MethodSessionClose:
		params, perr := decode[protocol.SessionIDParams](req)
		if perr != nil {
			return nil, nil, perr
		}
		if perr := sessions.Close(params.SessionID); perr != nil {
			return nil, nil, perr
		}
		return empty{}, nil, nil

	case protocol.MethodTerminalAttach:
		params, perr := decode[protocol.TerminalAttachParams](req)
		if perr != nil {
			return nil, nil, perr
		}
		s, perr := find(params.SessionID)
		if perr != nil {
			return nil, nil, perr
		}
		c.mu.Lock()
		id := c.nextChannel
		c.nextChannel++
		ch, head := newChannel(id, s, s.Log, params.FromOffset, params.Backlog)
		c.channels[id] = ch
		c.mu.Unlock()
		// The client learns the channel id from the response, so no frame
		// for the channel may be sent before it.
		return protocol.TerminalAttachResult{Channel: id, Head: head}, func() { c.activate(ch) }, nil

	case protocol.MethodTerminalDetach:
		params, perr := decode[protocol.TerminalDetachParams](req)
		if perr != nil {
			return nil, nil, perr
		}
		if !c.detach(params.Channel) {
			return nil, nil, &protocol.Error{Code: protocol.ErrNotFound, Message: "no such channel"}
		}
		return empty{}, nil, nil

	case protocol.MethodTerminalResize:
		params, perr := decode[protocol.TerminalResizeParams](req)
		if perr != nil {
			return nil, nil, perr
		}
		// The lock is kept, so the writer cuts no frame while the place
		// this client may answer from is moving.
		c.mu.Lock()
		defer c.mu.Unlock()
		ch := c.channels[params.Channel]
		if ch == nil {
			return nil, nil, &protocol.Error{Code: protocol.ErrNotFound, Message: "no such channel"}
		}
		since, perr := ch.sess.Resize(ch, c.clientID, params.Cols, params.Rows)
		if perr != nil {
			return nil, nil, perr
		}
		ch.replyFrom = max(ch.replyFrom, since)
		return empty{}, nil, nil

	case protocol.MethodHello:
		return nil, nil, &protocol.Error{Code: protocol.ErrInvalidRequest, Message: "hello was already done"}
	}
	return nil, nil, &protocol.Error{Code: protocol.ErrUnknownMethod, Message: "unknown method " + req.Method}
}

// activate lets the writer send a channel's frames. The writer calls it once
// the client has the channel's id.
func (c *conn) activate(ch *channel) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.channels[ch.id] != ch {
		return // detached in the meantime
	}
	ch.active = true
	c.order = append(c.order, ch)
	if c.watching[ch.sess] == 0 {
		ch.log.Watch(c.wake)
	}
	c.watching[ch.sess]++
}

// detach ends a channel. It reports whether the channel existed.
func (c *conn) detach(id uint32) bool {
	c.mu.Lock()
	ch := c.channels[id]
	if ch != nil {
		c.removeLocked(ch)
	}
	c.mu.Unlock()

	if ch == nil {
		return false
	}
	ch.sess.Release(ch)
	return true
}

// removeLocked forgets a channel. The caller holds c.mu.
func (c *conn) removeLocked(ch *channel) {
	delete(c.channels, ch.id)
	if !ch.active {
		return
	}
	ch.active = false
	for i, other := range c.order {
		if other == ch {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	c.watching[ch.sess]--
	if c.watching[ch.sess] == 0 {
		delete(c.watching, ch.sess)
		ch.log.Unwatch(c.wake)
	}
}

func (c *conn) detachAll() {
	c.mu.Lock()
	channels := make([]*channel, 0, len(c.channels))
	for _, ch := range c.channels {
		channels = append(channels, ch)
	}
	for _, ch := range channels {
		c.removeLocked(ch)
	}
	c.mu.Unlock()

	for _, ch := range channels {
		ch.sess.Release(ch)
	}
}

// nextFrame returns the next terminal frame to send, taking the channels in
// turn so one busy session cannot starve the others.
func (c *conn) nextFrame(scratch []byte) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for tried := 0; tried < len(c.order); {
		if c.turn >= len(c.order) {
			c.turn = 0
		}
		ch := c.order[c.turn]
		if ch.sess.Closed() {
			// Closing a session ends its channels.
			c.removeLocked(ch)
			continue
		}
		c.turn++
		tried++
		if frame, ok := ch.next(scratch); ok {
			return frame, true
		}
	}
	return nil, false
}

func (c *conn) write(typ websocket.MessageType, data []byte) bool {
	ctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
	defer cancel()
	if err := c.ws.Write(ctx, typ, data); err != nil {
		c.cancel()
		return false
	}
	return true
}

func (c *conn) writeControl(msg outgoing) bool {
	if !c.write(websocket.MessageText, msg.data) {
		return false
	}
	if msg.then != nil {
		msg.then()
	}
	return true
}

// writeLoop is the only writer after the handshake. Control messages go
// before terminal frames, so a flood of output cannot delay them.
func (c *conn) writeLoop() {
	scratch := make([]byte, 0, protocol.MaxFramePayload)
	for {
		select {
		case msg := <-c.control:
			if !c.writeControl(msg) {
				return
			}
			continue
		default:
		}

		if frame, ok := c.nextFrame(scratch); ok {
			if !c.write(websocket.MessageBinary, frame) {
				return
			}
			continue
		}

		select {
		case msg := <-c.control:
			if !c.writeControl(msg) {
				return
			}
		case <-c.wake:
		case <-c.ctx.Done():
			return
		}
	}
}

func (c *conn) pingLoop() {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
			err := c.ws.Ping(ctx)
			cancel()
			if err != nil {
				c.cancel()
				return
			}
		}
	}
}
