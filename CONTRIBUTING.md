# Contributing to Worky

Thanks for taking the time. This guide covers what kind of changes fit the
project, how the repository is organised, and what a pull request needs.

Worky is pre-alpha. Right now the most useful contributions are:

- Feedback on the design
- Terminal rendering bug reports with a way to reproduce them
- Testing on a platform or shell the maintainer does not use

## Scope comes first

Worky is meant to stay small. Every feature costs memory, startup time and
maintenance, and the project is maintained by one person.

Before you write code for anything larger than a small fix, **open an issue and
wait for a reply**. This saves you from building something that will not be
merged.

A change is likely to be accepted if it:

- Makes an existing feature more correct, faster or lighter
- Fixes rendering, resize or reconnect behaviour
- Improves support for a platform, shell or agent CLI without special-casing it

A change is likely to be declined if it:

- Is out of scope: a mobile app, team or chat features, issue tracker or
  messaging integrations, an embedded browser, a plugin marketplace, accounts,
  cloud sync, telemetry, or a relay service
- Adds a background process, a long-running worker or a heavy dependency
- Adds a second way to do something the app already does
- Only works for one agent CLI

"No" is about fit, not quality. A fork is a fine place for a feature that does
not belong here.

## Repository layout

```
.
├── daemon/            Go. `workyd`, the per-machine daemon
├── apps/
│   └── desktop/       Desktop app: Tauri 2 shell, TypeScript frontend
└── packages/
    └── protocol/      Wire protocol shared by the daemon and the UI
```

Keep a pull request inside one area where you can. A protocol change is the
exception: it touches `packages/protocol`, the daemon and the UI together, and
all three belong in the same pull request.

## Development setup

You need:

| Tool | Version | Used for |
|---|---|---|
| Go | 1.26 or newer | `daemon/` |
| Rust | current stable | The Tauri shell in `apps/desktop/` |
| Node.js | 22 or newer | The app's frontend |
| pnpm | 9 or newer | Workspace package manager |

Tauri also needs some system packages. Follow the
[Tauri prerequisites](https://tauri.app/start/prerequisites/) for your OS.

Install the JavaScript dependencies once from the repository root:

```
pnpm install
```

Run the daemon and the app in two terminals:

```
go run ./cmd/workyd     # in daemon/
pnpm dev                # in the repository root
```

The daemon listens on `127.0.0.1:7433`. On first run it writes a token to
`worky/token` in your user config directory.

Before you open a pull request, run the checks for the area you touched:

| Area | Where | Commands |
|---|---|---|
| Daemon | `daemon/` | `gofmt -l .`, `go vet ./...`, `go test ./...` |
| UI and protocol | repository root | `pnpm typecheck`, `pnpm test` |
| Tauri shell | `apps/desktop/src-tauri/` | `cargo fmt --check`, `cargo clippy` |

## Design rules to keep

A change that breaks one of these needs a very good reason and an issue
discussing it first.

**Daemon**

- Never interpret PTY output. Treat it as bytes from the PTY to the wire. Do
  not convert it to a string, and do not split or rewrite it.
- Record resize events in the same ordered stream as output.
- Listen on loopback by default. Never accept a connection without a token.
- Every goroutine has an owner that stops it. Pass a `context.Context` and
  respect cancellation.
- Prefer the standard library. A new dependency needs a sentence in the pull
  request explaining why.

**Protocol**

- The protocol is versioned. Do not change the meaning of an existing message;
  add a new one.
- A daemon and an app from different releases must fail clearly, not silently
  misbehave.

**Desktop app**

- The app is a viewer. Session state belongs in the daemon.
- No code path may behave differently for a local daemon than for a remote one.
- Do not create a terminal instance for a session that is not on screen.
- Keep the Rust shell thin. Logic goes in the daemon or the UI.

## Testing

- Add tests for daemon logic. Session lifecycle, stream ordering and reconnect
  handling are the parts that must not regress.
- For a terminal rendering change, include the exact input that triggered the
  problem. Cover wide characters (for example Korean, Japanese or Chinese
  text) and at least one resize.
- Say which OS and shell you tested on. Nobody expects you to test every
  platform, but reviewers need to know which ones were covered.

## Commits and pull requests

**Commits**

- Follow [Conventional Commits](https://www.conventionalcommits.org/):
  `type(scope): subject`.
- Types: `feat`, `fix`, `docs`, `refactor`, `perf`, `test`, `build`, `ci`,
  `chore`.
- Scopes: `daemon`, `desktop`, `protocol`. Leave the scope out for a change
  that spans the repository.
- Write the subject in the imperative, in lower case, with no trailing period,
  for example `feat(daemon): record resize events in the output log`.
- One logical change per commit.

**Pull requests**

- Keep them small. A pull request that does one thing gets reviewed quickly.
- In the description, say what changed, why, and how you tested it.
- Link the issue it addresses.
- For a UI change, add a screenshot or a short recording.
- Do not mix refactoring with behaviour changes.

Review is done by one maintainer, so replies can take a few days.

Contributions are licensed under [AGPL-3.0](LICENSE).

## Reporting bugs

Open an issue with:

- OS and version, and the shell you use
- Worky version or commit
- Steps to reproduce
- What you expected and what happened

For rendering problems, also include the text or command that triggers it, the
window size if it matters, and a screenshot. If the session was remote, say
what OS the daemon was running on.

## Security issues

Do not open a public issue for a security problem. Use the repository's private
vulnerability reporting, under **Security → Report a vulnerability**.
