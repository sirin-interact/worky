# Worky

A lightweight desktop app for running coding agents in git worktrees, on this
machine or on other machines.

Pre-alpha. Not usable yet.

## Features

- One git worktree per agent session
- Attach to sessions running on other machines
- Terminals that survive resize, reconnect and CJK text
- Session list sorted by what needs attention
- File editor and Markdown viewer

Works with any agent CLI that runs in a terminal.

## How it works

A small Go daemon (`workyd`) runs on each machine and owns the sessions. The
desktop app (Tauri) connects to one or more daemons over WebSocket. Local and
remote sessions use the same protocol.

## Layout

```
daemon/             workyd (Go)
apps/desktop/       desktop app (Tauri, TypeScript)
packages/protocol/  wire protocol
```

## Development

Needs Go, Rust, Node.js and pnpm. See [CONTRIBUTING.md](CONTRIBUTING.md) for
versions.

```
pnpm install
go run ./cmd/workyd -allow-origin http://localhost:1420     # in daemon/
pnpm dev                                                    # desktop app
```

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[AGPL-3.0](LICENSE). Copyright (C) 2026 SiRin.
