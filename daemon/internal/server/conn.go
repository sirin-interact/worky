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
	"time"

	"github.com/coder/websocket"

	"github.com/sirin-interact/worky/daemon/internal/auth"
	"github.com/sirin-interact/worky/daemon/internal/protocol"
	"github.com/sirin-interact/worky/daemon/internal/session"
)

const (
	helloTimeout   = 5 * time.Second
	writeTimeout   = 30 * time.Second
	pingInterval   = 30 * time.Second
	maxJSONMessage = 1 << 20
	// A client that lets this many control messages pile up is dropped.
	controlQueue = 1024
)

// conn is one client connection.
type conn struct {
	srv    *server
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc

	clientID string

	control chan []byte   // JSON messages waiting to be sent
	wake    chan struct{} // signals that a channel may have a frame to send

	mu          sync.Mutex
	channels    map[uint32]*channel
	order       []*channel // round-robin order for the writer
	turn        int
	nextChannel uint32
	watching    map[*session.Session]int
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
		control:  make(chan []byte, controlQueue),
		wake:     make(chan struct{}, 1),
		channels: make(map[uint32]*channel),
		watching: make(map[*session.Session]int),
	}
	c.serve()
}

func (c *conn) serve() {
	defer c.ws.CloseNow()
	defer c.cancel()

	if !c.handshake() {
		return
	}

	unsubscribe := c.srv.Sessions.Subscribe(func(event string, data any) {
		c.send(protocol.Event{Event: event, Data: mustMarshal(data)})
	})
	defer unsubscribe()
	defer c.detachAll()

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
	ctx, cancel := context.WithTimeout(c.ctx, helloTimeout)
	defer cancel()

	typ, data, err := c.ws.Read(ctx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			c.closeWith(protocol.CloseProtocolViolation, "no hello")
		}
		return false
	}
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
func (c *conn) send(v any) {
	select {
	case c.control <- mustMarshal(v):
	default:
		slog.Warn("dropping a client that is not reading", "client", c.clientID)
		c.cancel()
	}
}

func (c *conn) poke() {
	select {
	case c.wake <- struct{}{}:
	default:
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
		result, then, perr := c.dispatch(req)
		if perr != nil {
			c.send(protocol.Response{ID: req.ID, Error: perr})
			continue
		}
		c.send(protocol.Response{ID: req.ID, Result: mustMarshal(result)})
		if then != nil {
			then()
		}
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

// dispatch runs a request. then, when set, runs after the response has been
// queued.
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
		c.mu.Unlock()
		ch, head := newChannel(id, s, s.Log, params.FromOffset, params.Backlog)
		// The client learns the channel id from the response, so no frame
		// for the channel may be sent before it.
		return protocol.TerminalAttachResult{Channel: id, Head: head}, func() { c.attach(ch) }, nil

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
		c.mu.Lock()
		ch := c.channels[params.Channel]
		c.mu.Unlock()
		if ch == nil {
			return nil, nil, &protocol.Error{Code: protocol.ErrNotFound, Message: "no such channel"}
		}
		if perr := ch.sess.Resize(ch, c.clientID, params.Cols, params.Rows); perr != nil {
			return nil, nil, perr
		}
		return empty{}, nil, nil

	case protocol.MethodHello:
		return nil, nil, &protocol.Error{Code: protocol.ErrInvalidRequest, Message: "hello was already done"}
	}
	return nil, nil, &protocol.Error{Code: protocol.ErrUnknownMethod, Message: "unknown method " + req.Method}
}

func (c *conn) attach(ch *channel) {
	c.mu.Lock()
	c.channels[ch.id] = ch
	c.order = append(c.order, ch)
	if c.watching[ch.sess] == 0 {
		ch.log.Watch(c.wake)
	}
	c.watching[ch.sess]++
	c.mu.Unlock()
	c.poke()
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
	channels := append([]*channel(nil), c.order...)
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

// writeLoop is the only writer after the handshake. Control messages go
// before terminal frames, so a flood of output cannot delay them.
func (c *conn) writeLoop() {
	scratch := make([]byte, 0, protocol.MaxFramePayload)
	for {
		select {
		case msg := <-c.control:
			if !c.write(websocket.MessageText, msg) {
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
			if !c.write(websocket.MessageText, msg) {
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
