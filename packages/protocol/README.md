# Worky protocol

Draft of protocol version 1. It covers the connection, sessions and the
terminal stream. Worktrees, files and agent status are not defined yet.

The TypeScript types are in `src/`. The Go types are in
`daemon/internal/protocol`. They are written by hand, so change both together.

## Transport

One WebSocket per daemon, at `GET /v1/ws`.

- **Text frames** carry JSON: requests, responses and events.
- **Binary frames** carry terminal bytes.

Both kinds share one ordered stream. A receiver must process frames in the
order they arrive.

Receivers ignore JSON fields and binary frame types they do not know.

The daemon sends WebSocket pings. There is no application-level ping.

Limits: a JSON message is at most 1 MiB. A binary frame payload is at most
64 KiB, so a client must split a large paste into several input frames.

## JSON messages

Request, from the client:

```json
{ "id": 1, "method": "session.list", "params": {} }
```

`id` is a positive integer chosen by the client, unique within the connection.

Response, from the daemon. Exactly one of `result` and `error` is present:

```json
{ "id": 1, "result": { "sessions": [] } }
{ "id": 1, "error": { "code": "not_found", "message": "no such session" } }
```

Event, from the daemon:

```json
{ "event": "session.closed", "data": { "sessionId": "s_1" } }
```

Error codes:

| Code | Meaning |
|---|---|
| `unauthorized` | Missing or wrong token |
| `unsupported_protocol` | The daemon does not speak the requested version |
| `invalid_request` | Malformed message or bad parameters |
| `unknown_method` | The method does not exist |
| `not_found` | The session or channel does not exist |
| `not_size_owner` | Only the size owner may resize the session |
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
    "protocol": 1,
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

If the token is wrong, the version is unsupported, the first message is not
`hello`, or no `hello` arrives within 5 seconds, the daemon answers with an
error where it can and closes the connection. Close code `4401` means
unauthorized. `4400` means a protocol violation.

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
  environment. The creator becomes the size owner.
- `session.close` kills the process if it is still running and deletes the
  session and its output.

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

The daemon does not interpret terminal output. For each session it keeps an
ordered log of output bytes and resize events, and a client replays that log
through its own terminal emulator.

A position in the log is an **offset**: the number of output bytes before it,
counted from the start of the session.

### Attaching

| Method | Params | Result |
|---|---|---|
| `terminal.attach` | `{ sessionId, fromOffset? }` | `{ channel, offset, head, cols, rows, reset }` |
| `terminal.detach` | `{ channel }` | `{}` |

- `channel` identifies this attachment in binary frames. It is unique within
  the connection.
- `offset` is where the stream that follows starts.
- `head` is the end of the log at the moment of attaching. The client has
  caught up once it has processed output up to `head`.
- `cols` and `rows` are the terminal size in effect at `offset`.
- `reset` tells the client to clear its terminal before applying the stream.

To resume after a reconnect, pass the offset just past the last byte the client
processed as `fromOffset`. If the daemon still has the log from that point,
`offset` equals `fromOffset` and `reset` is `false`. Otherwise the stream
starts from the oldest byte the daemon has, and `reset` is `true`. Without
`fromOffset` the stream always starts from the oldest byte and `reset` is
`true`.

After the response, the daemon sends the backlog and then live output for the
channel: output frames, with `terminal.resized` events between them at the
points where the size changed.

| Event | Data |
|---|---|
| `terminal.resized` | `{ channel, offset, cols, rows }` |
| `terminal.detached` | `{ channel, reason }` |

`terminal.resized` means the size is `cols` by `rows` for output from `offset`
on. The client resizes its terminal when the event arrives.

`terminal.detached` means the daemon ended the attachment. `reason` is `closed`
when the session was closed, or `lagging` when the client fell too far behind.
After `lagging`, attach again with `fromOffset`.

### Binary frames

All integers are big-endian.

Output, daemon to client:

| Bytes | Field |
|---|---|
| 0 | `0x01` |
| 1 to 4 | channel, uint32 |
| 5 to 12 | offset of the first payload byte, uint64 |
| 13 on | raw PTY output |

Input, client to daemon:

| Bytes | Field |
|---|---|
| 0 | `0x02` |
| 1 to 4 | channel, uint32 |
| 5 on | raw bytes to write to the PTY |

An output frame can end in the middle of a multi-byte character or an escape
sequence. Pass the payload to the terminal emulator as bytes. Do not decode it
to a string first.

Any attached client may send input. Input for an exited session is dropped.

Offsets fit in a JavaScript number. A client rejects an offset above
2^53 - 1.

`testdata/frames.json` holds frame encodings that both implementations are
tested against.

### Size

A session has one size, and at most one client decides it.

| Method | Params | Result |
|---|---|---|
| `terminal.claimSize` | `{ sessionId, cols, rows }` | `{}` |
| `terminal.resize` | `{ sessionId, cols, rows }` | `{}` |

- `terminal.claimSize` makes the caller the size owner and resizes the session
  in one step.
- `terminal.resize` succeeds only for the size owner. Anyone else gets
  `not_size_owner`.
- Ownership ends when the owner's connection closes or it detaches its last
  channel for the session. `sizeOwner` becomes `null` until a client claims it.

A client that is not the size owner shows the session at the session's size,
scaled to fit. It does not reflow.

## Open questions

- **Truncated replay.** The daemon keeps a bounded amount of output. When a
  replay starts after the true beginning, a full-screen program can look wrong
  until it repaints. The daemon may need to prompt a repaint in that case.
- **Retention.** How much output the daemon keeps per session is not decided.
- **Generated types.** Only the binary frames are checked against shared test
  data. The JSON types are kept in step by hand. Generating one side from the
  other would remove the risk.
