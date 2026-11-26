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
  | "not_size_owner"
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
  /** Offset just past the last byte the client processed. */
  fromOffset?: number;
}

export interface TerminalAttachResult {
  channel: number;
  offset: number;
  head: number;
  cols: number;
  rows: number;
  /** Clear the terminal before applying the stream. */
  reset: boolean;
}

export interface TerminalSizeParams {
  sessionId: string;
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
  "terminal.claimSize": { params: TerminalSizeParams; result: Empty };
  "terminal.resize": { params: TerminalSizeParams; result: Empty };
}

export type Method = keyof Methods;

export type DetachReason = "closed" | "lagging";

/** Every event, with its data. */
export interface Events {
  "session.created": { session: Session };
  "session.updated": { session: Session };
  "session.closed": { sessionId: string };
  "terminal.resized": { channel: number; offset: number; cols: number; rows: number };
  "terminal.detached": { channel: number; reason: DetachReason };
}

export type EventName = keyof Events;

export type Request<M extends Method = Method> = {
  [K in M]: { id: number; method: K; params: Methods[K]["params"] };
}[M];

export type Response<M extends Method = Method> =
  | { id: number; result: Methods[M]["result"] }
  | { id: number; error: ProtocolError };

export type EventMessage<E extends EventName = EventName> = {
  [K in E]: { event: K; data: Events[K] };
}[E];

/** Any JSON message the daemon can send. */
export type ServerMessage = Response | EventMessage;
