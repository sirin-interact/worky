import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

import { decodeFrame, encodeInputFrame, encodeOutputFrame } from "./frames.ts";

interface ValidCase {
  name: string;
  hex: string;
  type: "output" | "input";
  channel: number;
  offset?: number;
  payloadHex: string;
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

for (const c of vectors.valid) {
  test(`decode: ${c.name}`, () => {
    const frame = decodeFrame(fromHex(c.hex));
    assert.ok(frame);
    assert.equal(frame.type, c.type);
    assert.equal(frame.channel, c.channel);
    assert.equal(toHex(frame.payload), c.payloadHex);
    if (frame.type === "output") {
      assert.equal(frame.offset, c.offset);
    }
  });

  test(`encode: ${c.name}`, () => {
    const payload = fromHex(c.payloadHex);
    const frame =
      c.type === "output"
        ? encodeOutputFrame(c.channel, c.offset ?? 0, payload)
        : encodeInputFrame(c.channel, payload);
    assert.equal(toHex(frame), c.hex);
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

test("decode reads a frame that is a view into a larger buffer", () => {
  const frame = fromHex("02000000016c730d");
  const padded = new Uint8Array(frame.length + 4);
  padded.set(frame, 2);
  const decoded = decodeFrame(padded.subarray(2, 2 + frame.length));
  assert.deepEqual(decoded, {
    type: "input",
    channel: 1,
    payload: fromHex("6c730d"),
  });
});

test("encode rejects an offset a JavaScript number cannot hold", () => {
  assert.throws(
    () => encodeOutputFrame(1, Number.MAX_SAFE_INTEGER + 1, new Uint8Array()),
    RangeError,
  );
});
