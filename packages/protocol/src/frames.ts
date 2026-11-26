// Binary frames. See ../README.md for the layout.
// The Go side lives in daemon/internal/protocol/frames.go.

export const FRAME_OUTPUT = 0x01;
export const FRAME_RESET = 0x02;
export const FRAME_STATE = 0x03;
export const FRAME_RESIZE = 0x04;
export const FRAME_INPUT = 0x10;

/** Largest payload either side may put in one frame. */
export const MAX_FRAME_PAYLOAD = 64 * 1024;

const HEADER = 5; // type + channel
const OFFSET_END = HEADER + 8;
const SIZE_END = OFFSET_END + 4; // cols + rows

export type Frame =
  | { type: "output"; channel: number; offset: number; payload: Uint8Array }
  | { type: "reset"; channel: number; offset: number; cols: number; rows: number }
  | { type: "state"; channel: number; payload: Uint8Array }
  | { type: "resize"; channel: number; offset: number; cols: number; rows: number }
  | { type: "input"; channel: number; payload: Uint8Array };

function writeOffset(view: DataView, offset: number): void {
  if (!Number.isSafeInteger(offset) || offset < 0) {
    throw new RangeError(`offset out of range: ${offset}`);
  }
  view.setBigUint64(HEADER, BigInt(offset));
}

function readOffset(view: DataView): number {
  const offset = view.getBigUint64(HEADER);
  if (offset > BigInt(Number.MAX_SAFE_INTEGER)) {
    throw new RangeError(`offset out of range: ${offset}`);
  }
  return Number(offset);
}

function withPayload(type: number, channel: number, payload: Uint8Array): Uint8Array {
  const bytes = new Uint8Array(HEADER + payload.length);
  bytes[0] = type;
  new DataView(bytes.buffer).setUint32(1, channel);
  bytes.set(payload, HEADER);
  return bytes;
}

function withSize(
  type: number,
  frame: { channel: number; offset: number; cols: number; rows: number },
): Uint8Array {
  const bytes = new Uint8Array(SIZE_END);
  const view = new DataView(bytes.buffer);
  bytes[0] = type;
  view.setUint32(1, frame.channel);
  writeOffset(view, frame.offset);
  view.setUint16(OFFSET_END, frame.cols);
  view.setUint16(OFFSET_END + 2, frame.rows);
  return bytes;
}

export function encodeFrame(frame: Frame): Uint8Array {
  switch (frame.type) {
    case "output": {
      const bytes = new Uint8Array(OFFSET_END + frame.payload.length);
      const view = new DataView(bytes.buffer);
      bytes[0] = FRAME_OUTPUT;
      view.setUint32(1, frame.channel);
      writeOffset(view, frame.offset);
      bytes.set(frame.payload, OFFSET_END);
      return bytes;
    }
    case "reset":
      return withSize(FRAME_RESET, frame);
    case "state":
      return withPayload(FRAME_STATE, frame.channel, frame.payload);
    case "resize":
      return withSize(FRAME_RESIZE, frame);
    case "input":
      return withPayload(FRAME_INPUT, frame.channel, frame.payload);
  }
}

/**
 * Decodes one binary frame. Returns null for a frame type this version does
 * not know, which callers ignore. Throws on a malformed frame.
 *
 * A payload is a view into `bytes`, not a copy.
 */
export function decodeFrame(bytes: Uint8Array): Frame | null {
  if (bytes.length < HEADER) {
    throw new RangeError(`frame too short: ${bytes.length} bytes`);
  }
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  const channel = view.getUint32(1);

  const need = (length: number) => {
    if (bytes.length < length) {
      throw new RangeError(`frame type ${bytes[0]} too short: ${bytes.length} bytes`);
    }
  };
  const sized = () => {
    need(SIZE_END);
    return {
      channel,
      offset: readOffset(view),
      cols: view.getUint16(OFFSET_END),
      rows: view.getUint16(OFFSET_END + 2),
    };
  };

  switch (bytes[0]) {
    case FRAME_OUTPUT:
      need(OFFSET_END);
      return {
        type: "output",
        channel,
        offset: readOffset(view),
        payload: bytes.subarray(OFFSET_END),
      };
    case FRAME_RESET:
      return { type: "reset", ...sized() };
    case FRAME_STATE:
      return { type: "state", channel, payload: bytes.subarray(HEADER) };
    case FRAME_RESIZE:
      return { type: "resize", ...sized() };
    case FRAME_INPUT:
      return { type: "input", channel, payload: bytes.subarray(HEADER) };
    default:
      return null;
  }
}
