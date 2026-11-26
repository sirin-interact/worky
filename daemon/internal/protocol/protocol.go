// Package protocol defines the wire protocol shared by the daemon and the UI.
// The specification is packages/protocol/README.md and the TypeScript side is
// packages/protocol/src. Change them together.
package protocol

import "encoding/json"

// The wire protocol versions this daemon speaks, oldest and newest.
const (
	MinVersion = 1
	MaxVersion = 1
)

// VersionRange is an inclusive range of protocol versions.
type VersionRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// Supported is the range this daemon speaks.
var Supported = VersionRange{Min: MinVersion, Max: MaxVersion}

// Negotiate returns the highest version in both ranges.
func Negotiate(client, daemon VersionRange) (version int, ok bool) {
	version = min(client.Max, daemon.Max)
	if version < client.Min || version < daemon.Min {
		return 0, false
	}
	return version, true
}

// Health is the response of GET /v1/health.
type Health struct {
	Name     string       `json:"name"`
	Version  string       `json:"version"`
	Protocol VersionRange `json:"protocol"`
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
	ErrSpawnFailed         ErrorCode = "spawn_failed"
	ErrInternal            ErrorCode = "internal"
)

// Error is the error of a Response. Data is set only for
// ErrUnsupportedProtocol, where it is the daemon's VersionRange.
type Error struct {
	Code    ErrorCode       `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

// WebSocket close codes.
const (
	CloseProtocolViolation = 4400
	CloseUnauthorized      = 4401
)

// Methods.
const (
	MethodHello          = "hello"
	MethodSessionList    = "session.list"
	MethodSessionCreate  = "session.create"
	MethodSessionRename  = "session.rename"
	MethodSessionClose   = "session.close"
	MethodTerminalAttach = "terminal.attach"
	MethodTerminalDetach = "terminal.detach"
	MethodTerminalResize = "terminal.resize"
)

// Events.
const (
	EventSessionCreated = "session.created"
	EventSessionUpdated = "session.updated"
	EventSessionClosed  = "session.closed"
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
	Protocol VersionRange `json:"protocol"`
	Token    string       `json:"token"`
	Client   Program      `json:"client"`
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
	Backlog    *uint64 `json:"backlog,omitempty"`
}

type TerminalAttachResult struct {
	Channel uint32 `json:"channel"`
	Head    uint64 `json:"head"`
}

type TerminalDetachParams struct {
	Channel uint32 `json:"channel"`
}

type TerminalResizeParams struct {
	Channel uint32 `json:"channel"`
	Cols    int    `json:"cols"`
	Rows    int    `json:"rows"`
}
