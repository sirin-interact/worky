// JSON messages. See ../README.md for the meaning of each one.
// The Go side lives in daemon/internal/protocol. Change both together.

export const PROTOCOL_VERSION = 1;

/** Response of `GET /v1/health`. */
export interface Health {
  name: string;
  version: string;
  protocol: number;
}

export type ErrorCode =
  | "unauthorized"
  | "unsupported_protocol"
  | "invalid_request"
  | "unknown_method"
  | "not_found"
  | "spawn_failed"
  | "internal";

export interface ProtocolError {
  code: ErrorCode;
  message: string;
}

export type HostOS = "windows" | "linux" | "darwin";

export type SessionStatus = "running" | "exited";

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
  /** RFC 3339, UTC. */
  createdAt: string;
}

export interface HelloParams {
  protocol: number;
  token: string;
  client: { name: string; version: string };
}

export interface HelloResult {
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
}

export type Method = keyof Methods;

/** Every event, with its data. */
export interface Events {
  "session.created": { session: Session };
  "session.updated": { session: Session };
  "session.closed": { sessionId: string };
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
