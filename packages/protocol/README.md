# Worky protocol

Draft of protocol version 1. It covers the connection, sessions, the terminal
stream, projects, worktrees and files. Search, git status and agent status are
not defined yet.

The TypeScript types are in `src/`. The Go types are in
`daemon/internal/protocol`. They are written by hand, so change both together.

## Transport

One WebSocket per daemon, at `GET /v1/ws`.

- **Text frames** carry JSON: requests, responses and events. This is the
  control plane.
- **Binary frames** carry terminal streams and terminal input.

A receiver must process frames in the order they arrive.

Receivers ignore JSON fields and binary frame types they do not know.

The daemon sends WebSocket pings. There is no application-level ping.

Limits: a JSON message is at most 1 MiB. A binary frame payload is at most
64 KiB, so a client must split a large paste into several input frames.

### Security

The token gives full access to the machine: a client can start any command as
the daemon's user. The connection is not encrypted, so a daemon must only be
reached over loopback or a network that is already trusted.

The daemon checks the `Origin` header and refuses a connection from a web page
that is not the Worky app.

## JSON messages

Request, from the client:

```json
{ "id": 1, "method": "session.list" }
```

`id` is a positive integer chosen by the client, unique within the connection.
`params` may be left out when a method takes none.

Response, from the daemon. Exactly one of `result` and `error` is present:

```json
{ "id": 1, "result": { "sessions": [] } }
{ "id": 1, "error": { "code": "not_found", "message": "no such session" } }
```

An error can carry a `data` field with details. Only `unsupported_protocol`
defines one.

The daemon answers `session.*` and `terminal.*` requests in the order they
were sent. It can answer any other request late, after requests sent later. A
client that needs one of those to take effect before the next waits for its
response. The daemon works on 16 of them at a time for one client and keeps
up to 240 more waiting. Past that it answers `busy`.

Event, from the daemon:

```json
{ "event": "session.closed", "data": { "sessionId": "s_1" } }
```

Error codes:

| Code | Meaning |
|---|---|
| `unauthorized` | Missing or wrong token |
| `unsupported_protocol` | The client and the daemon share no protocol version |
| `invalid_request` | Malformed message or bad parameters |
| `unknown_method` | The method does not exist |
| `not_found` | The session, channel, project, worktree, watch, file or directory does not exist |
| `spawn_failed` | The session's command could not be started |
| `already_exists` | Something is already at that path |
| `permission_denied` | The daemon's user may not do that to the file |
| `conflict` | The file is not at the version the client expected |
| `io_error` | The operating system failed the file operation |
| `not_a_repository` | The path is not in a git repository |
| `git_failed` | git refused, or is not installed. The message is what git said |
| `in_use` | The worktree still has sessions |
| `busy` | Too many of the client's requests are waiting. It should wait for some answers and ask again |
| `internal` | Anything else |

## Handshake

The first message on a connection must be a `hello` request. A browser
WebSocket cannot set an `Authorization` header, so the token travels here.

```json
{
  "id": 1,
  "method": "hello",
  "params": {
    "protocol": { "min": 1, "max": 1 },
    "token": "…",
    "client": { "name": "worky-desktop", "version": "0.1.0" }
  }
}
```

```json
{
  "id": 1,
  "result": {
    "protocol": 1,
    "clientId": "c_1",
    "daemon": { "name": "workyd", "version": "0.1.0" },
    "host": { "hostname": "desk", "os": "windows" }
  }
}
```

`os` is `windows`, `linux` or `darwin`.

A daemon that has running sessions keeps running when the app is updated, so a
newer client has to work with an older daemon. The client sends the range of
versions it speaks, and the daemon answers with the highest one in that range
that it speaks too. If there is none, the error is `unsupported_protocol` and
its `data` is the daemon's own range, `{ "min": 1, "max": 1 }`.

If the token is wrong, no version is shared, the first message is not
`hello`, or no `hello` arrives within 5 seconds, the daemon answers with an
error where it can and closes the connection. Close code `4401` means
unauthorized. `4400` means a protocol violation.

The daemon sends no events before the handshake succeeds.

## Sessions

A session is one process running in a PTY on the daemon's machine. It outlives
every client connection.

```json
{
  "id": "s_1",
  "name": "fix login",
  "cwd": "C:\\src\\app",
  "command": ["claude"],
  "status": "running",
  "exitCode": null,
  "cols": 120,
  "rows": 30,
  "sizeOwner": "c_1",
  "worktree": "C:\\src\\app",
  "createdAt": "2026-11-26T10:00:00Z"
}
```

`status` is `running` or `exited`. An exited session stays listed, with its
output, until it is closed. `sizeOwner` is a client id or `null`.

`worktree` is the `path` of the worktree that `cwd` is in, character for
character as that worktree's entry in its project has it. It is `null` when
`cwd` is in no worktree of a project. See Projects.

| Method | Params | Result |
|---|---|---|
| `session.list` | none | `{ sessions }` |
| `session.create` | `{ cwd, cols, rows, command?, env?, name? }` | `{ session }` |
| `session.rename` | `{ sessionId, name }` | `{}` |
| `session.close` | `{ sessionId }` | `{}` |

- `session.create` without `command` runs the user's default shell. `cwd` is
  an absolute path. `command` is an argument vector, not a shell string. `env` is added to the daemon's
  environment. `cols` and `rows` are the starting size. A new session has no
  size owner.
- `session.close` kills the process if it is still running, ends every channel
  attached to the session, and deletes the session and its output.

Every connected client receives session events. There is nothing to subscribe
to.

| Event | Data |
|---|---|
| `session.created` | `{ session }` |
| `session.updated` | `{ session }` |
| `session.closed` | `{ sessionId }` |

`session.updated` carries the whole session and is sent when its name, status,
size, size owner or worktree changes. An event can arrive before the response to
`session.list`, so a client should apply both by session id.

## Terminal stream

For each session the daemon keeps a log of the PTY's output bytes and of the
points where the size changed. A position in the log is an **offset**: the
number of output bytes before it, counted from the start of the session.

A client attaches to a session and receives a stream of binary frames on a
channel. It feeds that stream to its own terminal emulator. Everything the
client needs to draw the terminal arrives in that one stream, in order.

| Method | Params | Result |
|---|---|---|
| `terminal.attach` | `{ sessionId, fromOffset?, backlog? }` | `{ channel, head }` |
| `terminal.detach` | `{ channel }` | `{}` |
| `terminal.resize` | `{ channel, cols, rows }` | `{}` |

- `channel` identifies this attachment in binary frames. It is unique within
  the connection and is not reused.
- `head` is the end of the log at the moment of attaching. The client has
  caught up once it has processed output up to `head`.

### Frames

All integers are big-endian. Every frame starts with a type byte and a channel.

| Bytes | Field |
|---|---|
| 0 | frame type |
| 1 to 4 | channel, uint32 |

From the daemon:

| Type | Name | Rest of the frame |
|---|---|---|
| `0x01` | output | offset, uint64. Then raw PTY output, the first byte of which is at that offset. |
| `0x02` | reset | offset, uint64. cols, uint16. rows, uint16. |
| `0x03` | state | Bytes for the terminal emulator that are not part of the log. |
| `0x04` | resize | offset, uint64. cols, uint16. rows, uint16. |

From the client:

| Type | Name | Rest of the frame |
|---|---|---|
| `0x10` | input | Raw bytes the user typed or pasted. |
| `0x11` | reply | offset, uint64. Then bytes the client's terminal produced to answer a query. See Queries. |

How a client handles each frame:

- **reset**: discard the terminal's contents and state, set its size to `cols`
  by `rows`, and expect the log to continue from `offset`.
- **state**: write the bytes to the terminal. They put it in the state it
  should have at the reset's `offset`. State frames only come directly after a
  reset.
- **output**: write the bytes to the terminal. The next expected offset moves
  forward by the payload length.
- **resize**: set the terminal's size to `cols` by `rows`. It applies to
  output from `offset` on.

An output or state frame can end in the middle of a multi-byte character or an
escape sequence. Pass the payload to the terminal emulator as bytes. Do not
decode it to a string first.

A terminal emulator can parse bytes some time after they were written to it.
xterm.js does. Apply a reset or a resize only once the emulator has parsed the
output that came before it, or the output is laid out at the wrong size.

Any attached client may send input. Input for an exited session is dropped.

Offsets fit in a JavaScript number. A client rejects an offset above
2^53 - 1.

`testdata/frames.json` holds frame encodings that both implementations are
tested against.

### Where a stream starts

Without `fromOffset`, the stream starts with a reset. `backlog` is the most
history the client wants, in bytes. The daemon starts from the oldest output it
kept, or `backlog` bytes before the end if that is later. Without `backlog` it
sends everything it kept. A stream can only start between escape sequences, at
points the daemon noted about every 64 KiB, so a client can get less history
than `backlog`, down to none, or somewhat more.

With `fromOffset`, the offset just past the last byte the client processed,
the stream continues from there with no reset. This is how a client resumes
after a reconnect, or returns to a terminal it kept. If the daemon no longer
has the log from that point, the stream starts with a reset instead.

The daemon may send a reset in the middle of a stream, for example when a
client reads so slowly that the output it has not received yet is no longer
kept. The client handles it like any other reset. A client never has to detach
and attach again to recover.

When a reset does not start at the beginning of the session, the state frames
after it restore what the skipped output had set: terminal modes, the keypad
mode, keyboard protocol flags and the cursor style. They do not restore what
was on the screen, the colours in effect or a scroll region.

### Queries

A program can ask the terminal a question, such as where the cursor is, and
wait for the answer. A terminal emulator writes the answer as if it had been
typed. With several clients attached, or with a client replaying old output,
that would put duplicate or stale answers into the PTY.

So a client never sends such an answer as input. It sends it in a reply frame,
with the offset just past the output its terminal had processed when it
produced the answer. The daemon writes a reply to the PTY only when both of
these hold, and drops it otherwise:

- It comes from the session's size owner.
- It answers output the daemon sent live, not output it sent as backlog after
  an attach or a reset.
- It answers output from after the client became the size owner.

The daemon ends an output frame at each of those boundaries, so a reply's
offset always falls clearly on one side.

While a session has no size owner, the daemon answers one query itself: the
primary device attributes query, because some programs wait for that answer
before they start. Any other query goes unanswered until a client owns the
size.

xterm.js reports typed input and its own answers through the same `onData`
event, with no public flag to tell them apart. A client built on it has to
separate the two itself.

### Size

A session has one size. `terminal.resize` sets it and makes the caller the
session's size owner. The size owner is also the one client whose replies the
daemon accepts. Ownership ends when that channel is detached or its
connection closes. The size stays as it was and `sizeOwner` becomes `null`.

A client should call `terminal.resize` only on behalf of its user: when they
start using the terminal, or when the layout changes while the client is the
size owner. A client that is not the size owner shows the session at the
session's size, scaled to fit, and does not resize it to match its own window.

## Paths

A path is an absolute path on the daemon's machine, written the way that
machine writes it: `C:\src\app` on Windows, `/home/me/app` elsewhere. On
Windows the daemon also accepts `/` as a separator.

To name an entry of a directory, join the directory's path and the entry's
name with `\` on Windows and `/` elsewhere. `host.os` from the handshake says
which.

The same file can be spelled in more than one way, so a client compares two
paths as strings only where this document says they match.

## Projects

A project is a git repository the daemon has been told about. The daemon
remembers its projects when it restarts. It runs `git` for this part, so git
has to be installed on its machine.

```json
{
  "id": "p_3f9a1c2b7d10",
  "name": "app",
  "path": "C:\\src\\app",
  "worktrees": [
    { "path": "C:\\src\\app", "branch": "main", "main": true },
    { "path": "C:\\src\\app.worktrees\\fix-login", "branch": "fix-login", "main": false }
  ]
}
```

`path` is the repository's main worktree, or the repository itself when it is
bare. `worktrees` lists the worktrees whose directory exists, the main one
first. `branch` is `null` when no branch is checked out. `worktrees` is empty
when the repository cannot be read, for example when its drive is not there.

| Method | Params | Result |
|---|---|---|
| `project.list` | none | `{ projects }` |
| `project.add` | `{ path }` | `{ project }` |
| `project.rename` | `{ projectId, name }` | `{}` |
| `project.remove` | `{ projectId }` | `{}` |
| `worktree.create` | `{ projectId, branch, base?, path? }` | `{ worktree }` |
| `worktree.remove` | `{ projectId, path, force? }` | `{}` |

- `project.add` takes any directory inside a repository. Adding a repository
  the daemon already has returns the existing project. A new project is named
  after its directory.
- `project.remove` makes the daemon forget the project. Nothing on disk
  changes and the project's sessions keep running.
- `worktree.create` adds a worktree with `branch` checked out. A branch that
  does not exist yet is created from `base`, which names a commit. Without
  `base` it is created from the branch of that name on a remote, if exactly
  one remote has it, and otherwise from the commit the main worktree is on.
  Without `path` the daemon picks a directory next to the project: its `path`
  with `.worktrees` added, then a directory named after the branch.
- `worktree.remove` deletes the worktree's directory and git's record of it.
  The branch stays. It fails with `in_use` while a session's `worktree` is
  this one. Without `force` it fails when the worktree holds changes that are
  not committed. The main worktree cannot be removed.

Every connected client receives project events.

| Event | Data |
|---|---|
| `project.added` | `{ project }` |
| `project.updated` | `{ project }` |
| `project.removed` | `{ projectId }` |

`project.updated` carries the whole project and is sent when its name or its
worktrees change. That includes changes made outside Worky, such as an agent
that adds a worktree or switches branch in a terminal.

## Files

A client can read and change any file the daemon's user can. The token
already lets it run any command, so limiting paths would protect nothing.

| Method | Params | Result |
|---|---|---|
| `fs.roots` | none | `{ roots, home }` |
| `fs.list` | `{ path }` | `{ path, entries, truncated }` |
| `fs.stat` | `{ path }` | `{ path, type, link, size, mtime, version }` |
| `fs.read` | `{ path, offset?, length? }` | `{ data, size, version }` |
| `fs.write` | `{ path, data, expect?, exclusive?, append? }` | `{ size, version }` |
| `fs.mkdir` | `{ path }` | `{}` |
| `fs.rename` | `{ from, to }` | `{}` |
| `fs.remove` | `{ path, recursive? }` | `{}` |
| `fs.watch` | `{ path }` | `{ watch }` |
| `fs.unwatch` | `{ watch }` | `{}` |

- `fs.roots` gives the places to start browsing from: `roots` is the drives
  on Windows and `/` elsewhere, and `home` is the home directory of the
  daemon's user.
- `fs.list` gives a directory's entries, sorted by name. An entry is
  `{ name, type, link }`. `type` is `file`, `dir` or `other`. `link` is true
  for a symbolic link or a Windows junction, and `type` is then what it leads
  to, or `other` when it leads nowhere. `truncated` is true when the
  directory has more entries than fit in one message.
- `fs.list` and `fs.stat` return `path` as the daemon spells it, with
  repeated and trailing separators gone.
- `fs.stat` follows a link. `mtime` is RFC 3339, UTC.
- `fs.mkdir` creates a directory and any parents it lacks.
- `fs.rename` moves a file or a directory. It fails with `already_exists`
  when something else is at `to`.
- `fs.remove` deletes for good. A directory that is not empty needs
  `recursive`. A link is deleted, not what it leads to.

### File contents

`data` is base64 of the file's bytes. The daemon does not look at the
encoding or the line endings. One call carries at most 512 KiB of content.

`version` is an opaque string that changes when the file's content does. It
comes from the file's size and modification time, so it can miss a write that
keeps the size on a file system with coarse timestamps.

`fs.read` returns the bytes from `offset`, at most `length` of them and at
most 512 KiB, with the file's whole `size`. To read a longer file, call again
from where the last call ended until `size` is reached. If `version` differs
between two calls the file changed in between, and the client starts over.

`fs.write` replaces the file's contents, and creates the file if it is not
there. It writes into the existing file, so a link stays a link and the
file's permissions stay as they were. The directory has to exist.

- With `expect`, the write happens only if the file is at that version. The
  error is `conflict` if it is at another one and `not_found` if it is gone.
  This is how an editor avoids overwriting what an agent wrote meanwhile.
- With `exclusive`, the write happens only if nothing is at `path`. The error
  is `already_exists` otherwise.
- With `append`, `data` is added to the end. To save more than fits in one
  call, write the first part without `append` and the rest with it, each
  time passing the `version` the last call returned as `expect`.

### Watching

`fs.watch` watches the entries directly inside one directory, not the
directories below it. `watch` identifies it. It is unique within the
connection, and the watch ends with the connection.

A client watches the directories it shows and the directories of the files it
has open. To follow one file it watches the file's directory and looks for the
file's name.

| Event | Data |
|---|---|
| `fs.changed` | `{ watch, names?, ended? }` |

Only the connection that asked receives it. The daemon collects changes for a
moment and reports them together.

- `names` lists the entries that were created, deleted, renamed or written
  to. Without `names`, the daemon cannot say which: the client should list
  the directory again and check every file it follows there.
- `ended` is true when the directory itself was deleted or moved, also by a
  rename of a directory above it. The watch reports nothing after that.

Some file systems tell the daemon nothing, network mounts among them. The
daemon looks at every watched directory every few seconds, so there it still
reports entries that came or went, late and without `names`. It does not
notice that a file's content changed.

An event says a name may have changed, not how. The client finds out by
listing the directory or by comparing a file's `version`.

## Open questions

- **The screen after a reset.** A reset that starts partway through a session
  restores modes but not what was on the screen, so a full-screen program can
  look wrong until it repaints. The daemon may have to prompt a repaint, or
  run a terminal emulator and send a full snapshot. The frames above carry
  either. Changing the session's size made the programs tried so far repaint
  in full. Setting the size it already has did not.
- **Queries with nobody attached.** Only the primary device attributes query
  is answered without a size owner. A program that asks for the cursor
  position then gets no answer.
- **Retention.** The daemon keeps the last 8 MiB of output per session, in
  memory. Whether to keep more on disk is not decided.
- **Deleting.** `fs.remove` cannot be undone. Moving to the trash instead
  works differently on every system and has no meaning on a server without a
  desktop.
- **Folders without git.** A project has to be a git repository. A plain
  folder can be browsed and can run sessions, but it cannot be a project.
- **Naming a piece of work.** A worktree has no title of its own to show in
  place of its branch. Where one would come from depends on agent status.
- **Generated types.** Only the binary frames are checked against shared test
  data. The JSON types are kept in step by hand. Generating one side from the
  other would remove the risk.
