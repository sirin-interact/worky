// JSON messages. See ../README.md for the meaning of each one.
// The Go side lives in daemon/internal/protocol. Change both together.

/** Newest protocol version this package speaks. */
export const PROTOCOL_VERSION = 1;
/** Oldest protocol version this package speaks. */
export const MIN_PROTOCOL_VERSION = 1;

/** An inclusive range of protocol versions. */
export interface VersionRange {
  min: number;
  max: number;
}

/** Response of `GET /v1/health`. */
export interface Health {
  name: string;
  version: string;
  protocol: VersionRange;
}

export type ErrorCode =
  | "unauthorized"
  | "unsupported_protocol"
  | "invalid_request"
  | "unknown_method"
  | "not_found"
  | "spawn_failed"
  | "already_exists"
  | "permission_denied"
  | "conflict"
  | "io_error"
  | "not_a_repository"
  | "git_failed"
  | "in_use"
  | "busy"
  | "internal";

export interface ProtocolError {
  code: ErrorCode;
  message: string;
  /** For `unsupported_protocol`, the daemon's own VersionRange. */
  data?: unknown;
}

export type HostOS = "windows" | "linux" | "darwin";

export type SessionStatus = "running" | "exited";

/** Whether a session's process is producing output. */
export type Activity = "busy" | "idle";

export interface Session {
  id: string;
  name: string;
  cwd: string;
  command: string[];
  status: SessionStatus;
  exitCode: number | null;
  cols: number;
  rows: number;
  /** Client id of the size owner, or null when nobody owns the size. */
  sizeOwner: string | null;
  /** `path` of the worktree that `cwd` is in, or null when it is in none. */
  worktree: string | null;
  /** RFC 3339, UTC. */
  createdAt: string;
  /** The window title the program last set, or "". */
  title: string;
  activity: Activity;
  /** When `activity` last changed. RFC 3339, UTC. */
  activityAt: string;
}

/** A git repository the daemon has been told about. */
export interface Project {
  id: string;
  name: string;
  /** The main worktree, or the repository itself when it is bare. */
  path: string;
  /** The worktrees whose directory exists, the main one first. */
  worktrees: Worktree[];
}

export interface Worktree {
  path: string;
  /** Null when no branch is checked out. */
  branch: string | null;
  main: boolean;
}

export interface WorktreeCreateParams {
  projectId: string;
  branch: string;
  /** The commit a new branch starts from. */
  base?: string;
  /** Where to put the worktree. The daemon picks a place without it. */
  path?: string;
}

export interface WorktreeRemoveParams {
  projectId: string;
  path: string;
  /** Remove it even if it holds changes that are not committed. */
  force?: boolean;
}

/** The most file content one `fs.read` or `fs.write` carries, in bytes. */
export const MAX_FILE_CHUNK = 512 * 1024;

export type FileType = "file" | "dir" | "other";

export interface FsEntry {
  name: string;
  /** For a link, what it leads to. */
  type: FileType;
  link: boolean;
}

export interface FsListResult {
  path: string;
  entries: FsEntry[];
  /** True when the directory has more entries than fit in one message. */
  truncated: boolean;
}

export interface FsStatResult {
  path: string;
  type: FileType;
  link: boolean;
  size: number;
  /** RFC 3339, UTC. */
  mtime: string;
  version: string;
}

export interface FsReadParams {
  path: string;
  offset?: number;
  length?: number;
}

export interface FsReadResult {
  /** Base64 of the bytes read. */
  data: string;
  /** Size of the whole file. */
  size: number;
  version: string;
}

export interface FsWriteParams {
  path: string;
  /** Base64 of the bytes to write. */
  data: string;
  /** Write only if the file is at this version. */
  expect?: string;
  /** Write only if nothing is at `path`. */
  exclusive?: boolean;
  /** Add to the end instead of replacing the contents. */
  append?: boolean;
}

export type SearchKind = "name" | "content";

export interface FsSearchParams {
  root: string;
  query: string;
  kind: SearchKind;
  caseSensitive?: boolean;
  limit?: number;
}

/** One line that matched a content search. */
export interface SearchMatch {
  /** Relative to the root, with forward slashes. */
  path: string;
  /** Counted from 1. */
  line: number;
  /** The line, cut short when it is long. */
  text: string;
  /** Where the match is in `text`, as string indexes. */
  start: number;
  end: number;
}

export interface FsSearchResult {
  /** Set for a name search. Relative to the root, with forward slashes. */
  files?: string[];
  /** Set for a content search. */
  matches?: SearchMatch[];
  truncated: boolean;
}

/**
 * One changed file. `index` and `worktree` are git's status letters: "." for
 * a side that is unchanged and "?" for a file git does not track.
 */
export interface GitFile {
  /** Relative to the worktree's root, with forward slashes. */
  path: string;
  origPath?: string;
  index: string;
  worktree: string;
  /** Lines added and removed against HEAD. Both 0 for a binary file. */
  added: number;
  removed: number;
}

export interface GitStatus {
  /** The worktree the path belongs to. */
  root: string;
  /** Null when no branch is checked out. */
  branch: string | null;
  head: string;
  upstream: string | null;
  ahead: number;
  behind: number;
  files: GitFile[];
}

export interface GitFilesParams {
  path: string;
  /** Relative to the worktree's root. */
  files: string[];
}

export interface FsChanged {
  watch: number;
  /** The entries that changed. Absent when too much happened to tell. */
  names?: string[];
  /** True when the directory is gone and the watch is over. */
  ended?: boolean;
}

export interface HelloParams {
  /** The versions the client speaks. The daemon picks the highest it shares. */
  protocol: VersionRange;
  token: string;
  client: { name: string; version: string };
}

export interface HelloResult {
  /** The version the daemon picked for this connection. */
  protocol: number;
  clientId: string;
  daemon: { name: string; version: string };
  host: { hostname: string; os: HostOS };
}

export interface SessionCreateParams {
  cwd: string;
  cols: number;
  rows: number;
  /** Argument vector. Omit to run the user's default shell. */
  command?: string[];
  env?: Record<string, string>;
  name?: string;
}

export interface TerminalAttachParams {
  sessionId: string;
  /** Offset just past the last byte the client processed. Resumes there. */
  fromOffset?: number;
  /** Most history wanted, in bytes, when the stream starts with a reset. */
  backlog?: number;
}

export interface TerminalAttachResult {
  channel: number;
  /** End of the log at the moment of attaching. */
  head: number;
}

export interface TerminalResizeParams {
  channel: number;
  cols: number;
  rows: number;
}

type Empty = Record<string, never>;

/** Every method, with its params and result. */
export interface Methods {
  hello: { params: HelloParams; result: HelloResult };
  "session.list": { params: Empty; result: { sessions: Session[] } };
  "session.create": { params: SessionCreateParams; result: { session: Session } };
  "session.rename": { params: { sessionId: string; name: string }; result: Empty };
  "session.close": { params: { sessionId: string }; result: Empty };
  "terminal.attach": { params: TerminalAttachParams; result: TerminalAttachResult };
  "terminal.detach": { params: { channel: number }; result: Empty };
  "terminal.resize": { params: TerminalResizeParams; result: Empty };
  "project.list": { params: Empty; result: { projects: Project[] } };
  "project.add": { params: { path: string }; result: { project: Project } };
  "project.rename": { params: { projectId: string; name: string }; result: Empty };
  "project.remove": { params: { projectId: string }; result: Empty };
  "worktree.create": { params: WorktreeCreateParams; result: { worktree: Worktree } };
  "worktree.remove": { params: WorktreeRemoveParams; result: Empty };
  "fs.roots": { params: Empty; result: { roots: string[]; home: string } };
  "fs.list": { params: { path: string }; result: FsListResult };
  "fs.stat": { params: { path: string }; result: FsStatResult };
  "fs.read": { params: FsReadParams; result: FsReadResult };
  "fs.write": { params: FsWriteParams; result: { size: number; version: string } };
  "fs.mkdir": { params: { path: string }; result: Empty };
  "fs.rename": { params: { from: string; to: string }; result: Empty };
  "fs.remove": { params: { path: string; recursive?: boolean }; result: Empty };
  "fs.watch": { params: { path: string }; result: { watch: number } };
  "fs.unwatch": { params: { watch: number }; result: Empty };
  "fs.search": { params: FsSearchParams; result: FsSearchResult };
  "git.status": { params: { path: string }; result: GitStatus };
  "git.stage": { params: GitFilesParams; result: Empty };
  "git.unstage": { params: GitFilesParams; result: Empty };
  "git.commit": { params: { path: string; message: string }; result: { head: string } };
}

export type Method = keyof Methods;

/** Every event, with its data. */
export interface Events {
  "session.created": { session: Session };
  "session.updated": { session: Session };
  "session.closed": { sessionId: string };
  "project.added": { project: Project };
  "project.updated": { project: Project };
  "project.removed": { projectId: string };
  "fs.changed": FsChanged;
}

export type EventName = keyof Events;

/** `params` may be left out for a method that takes none. */
export type Request<M extends Method = Method> = {
  [K in M]: Methods[K]["params"] extends Empty
    ? { id: number; method: K; params?: Empty }
    : { id: number; method: K; params: Methods[K]["params"] };
}[M];

export type Response<M extends Method = Method> =
  | { id: number; result: Methods[M]["result"] }
  | { id: number; error: ProtocolError };

export type EventMessage<E extends EventName = EventName> = {
  [K in E]: { event: K; data: Events[K] };
}[E];

/** Any JSON message the daemon can send. */
export type ServerMessage = Response | EventMessage;
