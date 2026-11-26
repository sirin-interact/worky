import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

import { decodeFrame, encodeFrame } from "./frames.ts";
import type { Frame } from "./frames.ts";

interface ValidCase {
  name: string;
  hex: string;
  type: Frame["type"];
  channel: number;
  offset?: number;
  cols?: number;
  rows?: number;
  payloadHex?: string;
}

interface Vectors {
  valid: ValidCase[];
  unknown: { name: string; hex: string }[];
  malformed: { name: string; hex: string }[];
}

const vectors: Vectors = JSON.parse(
  readFileSync(new URL("../testdata/frames.json", import.meta.url), "utf8"),
);

const fromHex = (hex: string) => Uint8Array.from(Buffer.from(hex, "hex"));
const toHex = (bytes: Uint8Array) => Buffer.from(bytes).toString("hex");

/** Builds the frame a vector describes. */
function expected(c: ValidCase): Frame {
  const payload = fromHex(c.payloadHex ?? "");
  switch (c.type) {
    case "output":
    case "reply":
      return { type: c.type, channel: c.channel, offset: c.offset ?? 0, payload };
    case "reset":
    case "resize":
      return {
        type: c.type,
        channel: c.channel,
        offset: c.offset ?? 0,
        cols: c.cols ?? 0,
        rows: c.rows ?? 0,
      };
    case "state":
    case "input":
      return { type: c.type, channel: c.channel, payload };
  }
}

for (const c of vectors.valid) {
  test(`decode: ${c.name}`, () => {
    assert.deepEqual(decodeFrame(fromHex(c.hex)), expected(c));
  });

  test(`encode: ${c.name}`, () => {
    assert.equal(toHex(encodeFrame(expected(c))), c.hex);
  });
}

for (const c of vectors.unknown) {
  test(`ignore: ${c.name}`, () => {
    assert.equal(decodeFrame(fromHex(c.hex)), null);
  });
}

for (const c of vectors.malformed) {
  test(`reject: ${c.name}`, () => {
    assert.throws(() => decodeFrame(fromHex(c.hex)), RangeError);
  });
}

test("the vectors cover every frame type", () => {
  const covered = new Set(vectors.valid.map((c) => c.type));
  assert.deepEqual([...covered].sort(), ["input", "output", "reply", "reset", "resize", "state"]);
});

test("decode reads a frame that is a view into a larger buffer", () => {
  const frame = fromHex("0400000001000000000000000500500018");
  const padded = new Uint8Array(frame.length + 4);
  padded.set(frame, 2);
  assert.deepEqual(decodeFrame(padded.subarray(2, 2 + frame.length)), {
    type: "resize",
    channel: 1,
    offset: 5,
    cols: 80,
    rows: 24,
  });
});

test("encode rejects an offset a JavaScript number cannot hold", () => {
  assert.throws(
    () =>
      encodeFrame({
        type: "output",
        channel: 1,
        offset: Number.MAX_SAFE_INTEGER + 1,
        payload: new Uint8Array(),
      }),
    RangeError,
  );
});
