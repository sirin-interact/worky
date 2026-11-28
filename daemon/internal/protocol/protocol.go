// Package protocol defines the wire protocol shared by the daemon and the UI.
// The specification is packages/protocol/README.md and the TypeScript side is
// packages/protocol/src. Change them together.
package protocol

import (
	"encoding/json"
	"fmt"
)

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
	ErrAlreadyExists       ErrorCode = "already_exists"
	ErrPermissionDenied    ErrorCode = "permission_denied"
	ErrConflict            ErrorCode = "conflict"
	ErrIO                  ErrorCode = "io_error"
	ErrNotARepository      ErrorCode = "not_a_repository"
	ErrGitFailed           ErrorCode = "git_failed"
	ErrInUse               ErrorCode = "in_use"
	ErrBusy                ErrorCode = "busy"
	ErrInternal            ErrorCode = "internal"
)

// Errorf returns an Error with a formatted message.
func Errorf(code ErrorCode, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

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
	MethodProjectList    = "project.list"
	MethodProjectAdd     = "project.add"
	MethodProjectRename  = "project.rename"
	MethodProjectRemove  = "project.remove"
	MethodWorktreeCreate = "worktree.create"
	MethodWorktreeRemove = "worktree.remove"
	MethodFsRoots        = "fs.roots"
	MethodFsList         = "fs.list"
	MethodFsStat         = "fs.stat"
	MethodFsRead         = "fs.read"
	MethodFsWrite        = "fs.write"
	MethodFsMkdir        = "fs.mkdir"
	MethodFsRename       = "fs.rename"
	MethodFsRemove       = "fs.remove"
	MethodFsWatch        = "fs.watch"
	MethodFsUnwatch      = "fs.unwatch"
	MethodFsSearch       = "fs.search"
	MethodGitStatus      = "git.status"
	MethodGitStage       = "git.stage"
	MethodGitUnstage     = "git.unstage"
	MethodGitCommit      = "git.commit"
)

// Events.
const (
	EventSessionCreated = "session.created"
	EventSessionUpdated = "session.updated"
	EventSessionClosed  = "session.closed"
	EventProjectAdded   = "project.added"
	EventProjectUpdated = "project.updated"
	EventProjectRemoved = "project.removed"
	EventFsChanged      = "fs.changed"
)

// MaxFileChunk is the most file content one fs.read or fs.write carries.
const MaxFileChunk = 512 << 10

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
	Worktree  *string       `json:"worktree"`
	CreatedAt string        `json:"createdAt"`
	// Title is the window title the program last set, or "".
	Title    string   `json:"title"`
	Activity Activity `json:"activity"`
	// ActivityAt is when Activity last changed. RFC 3339, UTC.
	ActivityAt string `json:"activityAt"`
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

// Project is a git repository the daemon has been told about.
type Project struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Path      string     `json:"path"`
	Worktrees []Worktree `json:"worktrees"`
}

// Worktree is one worktree of a project. Branch is nil when no branch is
// checked out.
type Worktree struct {
	Path   string  `json:"path"`
	Branch *string `json:"branch"`
	Main   bool    `json:"main"`
}

type ProjectListResult struct {
	Projects []Project `json:"projects"`
}

type ProjectAddParams struct {
	Path string `json:"path"`
}

type ProjectResult struct {
	Project Project `json:"project"`
}

type ProjectRenameParams struct {
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
}

// ProjectIDParams is the params of project.remove and the data of
// project.removed.
type ProjectIDParams struct {
	ProjectID string `json:"projectId"`
}

type WorktreeCreateParams struct {
	ProjectID string `json:"projectId"`
	Branch    string `json:"branch"`
	Base      string `json:"base,omitempty"`
	Path      string `json:"path,omitempty"`
}

type WorktreeResult struct {
	Worktree Worktree `json:"worktree"`
}

type WorktreeRemoveParams struct {
	ProjectID string `json:"projectId"`
	Path      string `json:"path"`
	Force     bool   `json:"force,omitempty"`
}

// FileType says what a path leads to.
type FileType string

const (
	TypeFile  FileType = "file"
	TypeDir   FileType = "dir"
	TypeOther FileType = "other"
)

// FsEntry is one entry of a directory.
type FsEntry struct {
	Name string   `json:"name"`
	Type FileType `json:"type"`
	Link bool     `json:"link"`
}

type FsRootsResult struct {
	Roots []string `json:"roots"`
	Home  string   `json:"home"`
}

// FsPathParams is the params of fs.list, fs.stat, fs.mkdir and fs.watch.
type FsPathParams struct {
	Path string `json:"path"`
}

type FsListResult struct {
	Path      string    `json:"path"`
	Entries   []FsEntry `json:"entries"`
	Truncated bool      `json:"truncated"`
}

type FsStatResult struct {
	Path    string   `json:"path"`
	Type    FileType `json:"type"`
	Link    bool     `json:"link"`
	Size    int64    `json:"size"`
	Mtime   string   `json:"mtime"`
	Version string   `json:"version"`
}

type FsReadParams struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset,omitempty"`
	Length *int64 `json:"length,omitempty"`
}

// FsReadResult carries file content. Data is base64 on the wire.
type FsReadResult struct {
	Data    []byte `json:"data"`
	Size    int64  `json:"size"`
	Version string `json:"version"`
}

// FsWriteParams carries file content. Data is base64 on the wire.
type FsWriteParams struct {
	Path      string  `json:"path"`
	Data      []byte  `json:"data"`
	Expect    *string `json:"expect,omitempty"`
	Exclusive bool    `json:"exclusive,omitempty"`
	Append    bool    `json:"append,omitempty"`
}

type FsWriteResult struct {
	Size    int64  `json:"size"`
	Version string `json:"version"`
}

type FsRenameParams struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type FsRemoveParams struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive,omitempty"`
}

type FsWatchResult struct {
	Watch uint32 `json:"watch"`
}

type FsUnwatchParams struct {
	Watch uint32 `json:"watch"`
}

// FsChanged is the data of fs.changed.
type FsChanged struct {
	Watch uint32   `json:"watch"`
	Names []string `json:"names,omitempty"`
	Ended bool     `json:"ended,omitempty"`
}
