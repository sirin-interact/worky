import assert from "node:assert/strict";
import { test } from "node:test";

import { ago, fromBase64, splitCommand, toBase64 } from "./util.ts";

test("base64 carries any bytes there and back", () => {
  const bytes = new Uint8Array(70000);
  for (let i = 0; i < bytes.length; i++) bytes[i] = (i * 31) & 0xff;
  assert.deepEqual(fromBase64(toBase64(bytes)), bytes);
  assert.equal(toBase64(new TextEncoder().encode("한글")), "7ZWc6riA");
  assert.equal(toBase64(new Uint8Array(0)), "");
});

test("a command line splits into arguments", () => {
  assert.deepEqual(splitCommand("claude --model claude-fable-5-2"), ["claude", "--model", "claude-fable-5-2"]);
  assert.deepEqual(splitCommand('  node "C:/my scripts/run.js"  --flag '), ["node", "C:/my scripts/run.js", "--flag"]);
  assert.deepEqual(splitCommand("echo 'a b' \"\" c"), ["echo", "a b", "", "c"]);
  assert.deepEqual(splitCommand("   "), []);
});

test("ages are short", () => {
  const now = Date.parse("2026-11-27T12:00:00Z");
  assert.equal(ago("2026-11-27T11:59:40Z", now), "방금");
  assert.equal(ago("2026-11-27T11:48:00Z", now), "12분");
  assert.equal(ago("2026-11-27T09:00:00Z", now), "3시간");
  assert.equal(ago("2026-11-26T10:00:00Z", now), "어제");
  assert.equal(ago("2026-11-23T12:00:00Z", now), "4일");
  assert.equal(ago("2026-11-10T12:00:00Z", now), "2주");
  assert.equal(ago("not a time", now), "");
});
