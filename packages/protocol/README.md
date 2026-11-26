# Worky protocol

Draft of protocol version 1. It covers the connection, sessions and the
terminal stream. Worktrees, files and agent status are not defined yet.

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
| `not_found` | The session or channel does not exist |
| `spawn_failed` | The session's command could not be started |
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
  "createdAt": "2026-11-26T10:00:00Z"
}
```

`status` is `running` or `exited`. An exited session stays listed, with its
output, until it is closed. `sizeOwner` is a client id or `null`.

| Method | Params | Result |
|---|---|---|
| `session.list` | none | `{ sessions }` |
| `session.create` | `{ cwd, cols, rows, command?, env?, name? }` | `{ session }` |
| `session.rename` | `{ sessionId, name }` | `{}` |
| `session.close` | `{ sessionId }` | `{}` |

- `session.create` without `command` runs the user's default shell. `command`
  is an argument vector, not a shell string. `env` is added to the daemon's
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
size or size owner changes. An event can arrive before the response to
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
than `backlog`, down to none.

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
- **Generated types.** Only the binary frames are checked against shared test
  data. The JSON types are kept in step by hand. Generating one side from the
  other would remove the risk.
