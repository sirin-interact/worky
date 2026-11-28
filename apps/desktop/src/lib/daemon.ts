// One connection to one daemon. It speaks the protocol and nothing else: what
// the app does with sessions and files is decided elsewhere.

import {
  decodeFrame,
  encodeFrame,
  MAX_FRAME_PAYLOAD,
  MIN_PROTOCOL_VERSION,
  PROTOCOL_VERSION,
  type ErrorCode,
  type EventMessage,
  type Frame,
  type HelloResult,
  type Methods,
  type ProtocolError,
  type TerminalAttachParams,
  type TerminalAttachResult,
} from "@worky/protocol";

export type ConnectionState = "connecting" | "open" | "closed" | "failed";

/** A request the daemon refused, or one that never got an answer. */
export class RequestError extends Error {
  readonly code: ErrorCode | "disconnected";
  readonly data: unknown;

  constructor(code: ErrorCode | "disconnected", message: string, data?: unknown) {
    super(message);
    this.name = "RequestError";
    this.code = code;
    this.data = data;
  }
}

type FrameHandler = (frame: Frame) => void;

interface Pending {
  resolve: (result: unknown) => void;
  reject: (error: RequestError) => void;
  /** Runs before any later message is looked at. */
  first?: (result: unknown) => void;
}

export interface DaemonOptions {
  url: string;
  token: string;
  onState: (state: ConnectionState, detail: string) => void;
  onEvent: (message: EventMessage) => void;
}

const CLIENT = { name: "worky-desktop", version: "0.1.0" };
const CLOSE_UNAUTHORIZED = 4401;
const RETRY_MIN = 500;
const RETRY_MAX = 8000;

type Params<M extends keyof Methods> =
  Methods[M]["params"] extends Record<string, never> ? [] : [params: Methods[M]["params"]];

export class DaemonClient {
  state: ConnectionState = "closed";
  hello: HelloResult | null = null;

  private ws: WebSocket | null = null;
  private nextId = 1;
  private pending = new Map<number, Pending>();
  private channels = new Map<number, FrameHandler>();
  private retry = RETRY_MIN;
  private timer: number | undefined;
  private stopped = false;
  private readonly options: DaemonOptions;

  constructor(options: DaemonOptions) {
    this.options = options;
  }

  /** Connects, and keeps connecting again whenever the connection is lost. */
  start(): void {
    this.stopped = false;
    this.open();
  }

  stop(): void {
    this.stopped = true;
    window.clearTimeout(this.timer);
    this.ws?.close();
    this.ws = null;
    this.setState("closed", "");
  }

  private setState(state: ConnectionState, detail: string): void {
    this.state = state;
    this.options.onState(state, detail);
  }

  private open(): void {
    window.clearTimeout(this.timer);
    this.setState("connecting", "");
    let ws: WebSocket;
    try {
      ws = new WebSocket(this.options.url);
    } catch (error) {
      this.setState("failed", String(error));
      return;
    }
    ws.binaryType = "arraybuffer";
    this.ws = ws;

    ws.onopen = () => {
      this.send("hello", {
        protocol: { min: MIN_PROTOCOL_VERSION, max: PROTOCOL_VERSION },
        token: this.options.token,
        client: CLIENT,
      }).then(
        (hello) => {
          this.hello = hello;
          this.retry = RETRY_MIN;
          this.setState("open", "");
        },
        (error: RequestError) => {
          // Trying again with the same token or the same versions changes
          // nothing, so these two stop the reconnecting.
          if (error.code === "unauthorized" || error.code === "unsupported_protocol") {
            this.stopped = true;
            this.setState("failed", error.code);
          }
        },
      );
    };

    ws.onmessage = (event) => {
      if (this.ws !== ws) return;
      if (typeof event.data === "string") {
        this.onText(event.data);
        return;
      }
      let frame: Frame | null;
      try {
        frame = decodeFrame(new Uint8Array(event.data as ArrayBuffer));
      } catch {
        return;
      }
      if (frame) this.channels.get(frame.channel)?.(frame);
    };

    ws.onclose = (event) => {
      if (this.ws !== ws) return;
      this.ws = null;
      this.hello = null;
      this.channels.clear();
      for (const waiting of this.pending.values()) {
        waiting.reject(new RequestError("disconnected", "the connection was lost"));
      }
      this.pending.clear();

      if (event.code === CLOSE_UNAUTHORIZED) {
        this.stopped = true;
        this.setState("failed", "unauthorized");
        return;
      }
      if (this.stopped) {
        if (this.state !== "failed") this.setState("closed", "");
        return;
      }
      this.setState("closed", event.reason);
      this.timer = window.setTimeout(() => this.open(), this.retry);
      this.retry = Math.min(this.retry * 2, RETRY_MAX);
    };
  }

  private onText(text: string): void {
    let message: { id?: number; result?: unknown; error?: ProtocolError; event?: string };
    try {
      message = JSON.parse(text);
    } catch {
      return;
    }
    if (typeof message.event === "string") {
      this.options.onEvent(message as EventMessage);
      return;
    }
    if (typeof message.id !== "number") return;
    const waiting = this.pending.get(message.id);
    if (!waiting) return;
    this.pending.delete(message.id);
    if (message.error) {
      waiting.reject(new RequestError(message.error.code, message.error.message, message.error.data));
      return;
    }
    waiting.first?.(message.result);
    waiting.resolve(message.result);
  }

  private send<M extends keyof Methods>(
    method: M,
    params: Methods[M]["params"] | undefined,
    first?: (result: Methods[M]["result"]) => void,
  ): Promise<Methods[M]["result"]> {
    return new Promise((resolve, reject) => {
      const ws = this.ws;
      if (!ws || ws.readyState !== WebSocket.OPEN) {
        reject(new RequestError("disconnected", "not connected"));
        return;
      }
      const id = this.nextId++;
      this.pending.set(id, {
        resolve: resolve as (result: unknown) => void,
        reject,
        first: first as ((result: unknown) => void) | undefined,
      });
      ws.send(JSON.stringify(params === undefined ? { id, method } : { id, method, params }));
    });
  }

  request<M extends Exclude<keyof Methods, "hello">>(
    method: M,
    ...params: Params<M>
  ): Promise<Methods[M]["result"]> {
    if (this.state !== "open") {
      return Promise.reject(new RequestError("disconnected", "not connected"));
    }
    return this.send(method, params[0]);
  }

  /**
   * Attaches to a session's terminal. The handler is in place before the
   * first frame of the channel can arrive.
   */
  attach(params: TerminalAttachParams, handler: FrameHandler): Promise<TerminalAttachResult> {
    if (this.state !== "open") {
      return Promise.reject(new RequestError("disconnected", "not connected"));
    }
    return this.send("terminal.attach", params, (result) => {
      this.channels.set(result.channel, handler);
    });
  }

  detach(channel: number): void {
    if (!this.channels.delete(channel)) return;
    this.request("terminal.detach", { channel }).catch(() => {});
  }

  private sendFrame(frame: Frame): void {
    const ws = this.ws;
    if (ws && ws.readyState === WebSocket.OPEN && this.state === "open") {
      ws.send(encodeFrame(frame) as Uint8Array<ArrayBuffer>);
    }
  }

  /** Sends what the user typed or pasted, split to fit the frame limit. */
  input(channel: number, bytes: Uint8Array): void {
    for (let at = 0; at < bytes.length; at += MAX_FRAME_PAYLOAD) {
      this.sendFrame({
        type: "input",
        channel,
        payload: bytes.subarray(at, at + MAX_FRAME_PAYLOAD),
      });
    }
  }

  /** Sends an answer the terminal emulator produced to a query. */
  reply(channel: number, offset: number, bytes: Uint8Array): void {
    this.sendFrame({ type: "reply", channel, offset, payload: bytes });
  }
}
