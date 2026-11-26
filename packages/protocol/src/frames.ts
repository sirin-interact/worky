// Binary frames. See ../README.md for the layout.
// The Go side lives in daemon/internal/protocol/frames.go.

export const FRAME_OUTPUT = 0x01;
export const FRAME_INPUT = 0x02;

/** Largest payload either side may put in one frame. */
export const MAX_FRAME_PAYLOAD = 64 * 1024;

const CHANNEL_END = 5;
const OUTPUT_HEADER = 13;

export type Frame =
  | { type: "output"; channel: number; offset: number; payload: Uint8Array }
  | { type: "input"; channel: number; payload: Uint8Array };

export function encodeInputFrame(channel: number, payload: Uint8Array): Uint8Array {
  const frame = new Uint8Array(CHANNEL_END + payload.length);
  frame[0] = FRAME_INPUT;
  new DataView(frame.buffer).setUint32(1, channel);
  frame.set(payload, CHANNEL_END);
  return frame;
}

export function encodeOutputFrame(
  channel: number,
  offset: number,
  payload: Uint8Array,
): Uint8Array {
  if (!Number.isSafeInteger(offset) || offset < 0) {
    throw new RangeError(`offset out of range: ${offset}`);
  }
  const frame = new Uint8Array(OUTPUT_HEADER + payload.length);
  const view = new DataView(frame.buffer);
  frame[0] = FRAME_OUTPUT;
  view.setUint32(1, channel);
  view.setBigUint64(CHANNEL_END, BigInt(offset));
  frame.set(payload, OUTPUT_HEADER);
  return frame;
}

/**
 * Decodes one binary frame. Returns null for a frame type this version does
 * not know, which callers ignore. Throws on a malformed frame.
 *
 * The payload is a view into `bytes`, not a copy.
 */
export function decodeFrame(bytes: Uint8Array): Frame | null {
  if (bytes.length < CHANNEL_END) {
    throw new RangeError(`frame too short: ${bytes.length} bytes`);
  }
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  const channel = view.getUint32(1);

  switch (bytes[0]) {
    case FRAME_OUTPUT: {
      if (bytes.length < OUTPUT_HEADER) {
        throw new RangeError(`output frame too short: ${bytes.length} bytes`);
      }
      const offset = view.getBigUint64(CHANNEL_END);
      if (offset > BigInt(Number.MAX_SAFE_INTEGER)) {
        throw new RangeError(`offset out of range: ${offset}`);
      }
      return {
        type: "output",
        channel,
        offset: Number(offset),
        payload: bytes.subarray(OUTPUT_HEADER),
      };
    }
    case FRAME_INPUT:
      return { type: "input", channel, payload: bytes.subarray(CHANNEL_END) };
    default:
      return null;
  }
}
