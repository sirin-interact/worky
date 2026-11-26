// Package protocol defines the wire protocol shared by the daemon and the UI.
// The specification is packages/protocol/README.md and the TypeScript side is
// packages/protocol/src. Change them together.
package protocol

import "encoding/json"

// Version is the wire protocol version.
const Version = 1

// Health is the response of GET /v1/health.
type Health struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
}

// Request is a JSON message from a client.
type Request struct {
	ID     uint64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response answers a Request. Exactly one of Result and Error is set.
type Response struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Event is a JSON message the daemon sends on its own.
type Event struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// ErrorCode identifies why a request failed.
type ErrorCode string

const (
	ErrUnauthorized        ErrorCode = "unauthorized"
	ErrUnsupportedProtocol ErrorCode = "unsupported_protocol"
	ErrInvalidRequest      ErrorCode = "invalid_request"
	ErrUnknownMethod       ErrorCode = "unknown_method"
	ErrNotFound            ErrorCode = "not_found"
	ErrNotSizeOwner        ErrorCode = "not_size_owner"
	ErrSpawnFailed         ErrorCode = "spawn_failed"
	ErrInternal            ErrorCode = "internal"
)

// Error is the error of a Response.
type Error struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

// WebSocket close codes.
const (
	CloseProtocolViolation = 4400
	CloseUnauthorized      = 4401
)

// Methods.
const (
	MethodHello             = "hello"
	MethodSessionList       = "session.list"
	MethodSessionCreate     = "session.create"
	MethodSessionRename     = "session.rename"
	MethodSessionClose      = "session.close"
	MethodTerminalAttach    = "terminal.attach"
	MethodTerminalDetach    = "terminal.detach"
	MethodTerminalClaimSize = "terminal.claimSize"
	MethodTerminalResize    = "terminal.resize"
)

// Events.
const (
	EventSessionCreated   = "session.created"
	EventSessionUpdated   = "session.updated"
	EventSessionClosed    = "session.closed"
	EventTerminalResized  = "terminal.resized"
	EventTerminalDetached = "terminal.detached"
)

// SessionStatus is the state of a session's process.
type SessionStatus string

const (
	StatusRunning SessionStatus = "running"
	StatusExited  SessionStatus = "exited"
)

// Session is one process running in a PTY.
type Session struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Cwd       string        `json:"cwd"`
	Command   []string      `json:"command"`
	Status    SessionStatus `json:"status"`
	ExitCode  *int          `json:"exitCode"`
	Cols      int           `json:"cols"`
	Rows      int           `json:"rows"`
	SizeOwner *string       `json:"sizeOwner"`
	CreatedAt string        `json:"createdAt"`
}

// Program names a client or a daemon build.
type Program struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Host describes the machine a daemon runs on. OS is a GOOS value.
type Host struct {
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
}

type HelloParams struct {
	Protocol int     `json:"protocol"`
	Token    string  `json:"token"`
	Client   Program `json:"client"`
}

type HelloResult struct {
	Protocol int     `json:"protocol"`
	ClientID string  `json:"clientId"`
	Daemon   Program `json:"daemon"`
	Host     Host    `json:"host"`
}

type SessionListResult struct {
	Sessions []Session `json:"sessions"`
}

type SessionCreateParams struct {
	Cwd     string            `json:"cwd"`
	Cols    int               `json:"cols"`
	Rows    int               `json:"rows"`
	Command []string          `json:"command,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Name    string            `json:"name,omitempty"`
}

type SessionResult struct {
	Session Session `json:"session"`
}

type SessionRenameParams struct {
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
}

// SessionIDParams is the params of session.close and the data of
// session.closed.
type SessionIDParams struct {
	SessionID string `json:"sessionId"`
}

type TerminalAttachParams struct {
	SessionID  string  `json:"sessionId"`
	FromOffset *uint64 `json:"fromOffset,omitempty"`
}

type TerminalAttachResult struct {
	Channel uint32 `json:"channel"`
	Offset  uint64 `json:"offset"`
	Head    uint64 `json:"head"`
	Cols    int    `json:"cols"`
	Rows    int    `json:"rows"`
	Reset   bool   `json:"reset"`
}

type TerminalDetachParams struct {
	Channel uint32 `json:"channel"`
}

// TerminalSizeParams is the params of terminal.claimSize and terminal.resize.
type TerminalSizeParams struct {
	SessionID string `json:"sessionId"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

type TerminalResized struct {
	Channel uint32 `json:"channel"`
	Offset  uint64 `json:"offset"`
	Cols    int    `json:"cols"`
	Rows    int    `json:"rows"`
}

// DetachReason says why the daemon ended an attachment.
type DetachReason string

const (
	DetachClosed  DetachReason = "closed"
	DetachLagging DetachReason = "lagging"
)

type TerminalDetached struct {
	Channel uint32       `json:"channel"`
	Reason  DetachReason `json:"reason"`
}
