// The files that are open in tabs: what was read from the daemon, what the
// editor holds now, and whether the two differ.

import type { EditorState } from "@codemirror/state";
import { signal } from "@preact/signals";
import { MAX_FILE_CHUNK } from "@worky/protocol";

import { RequestError } from "./daemon.ts";
import { clientOf, report } from "./store.ts";
import { errorText, fromBase64, toBase64 } from "./util.ts";

/** A larger file is not opened: it would not be pleasant to edit here. */
const MAX_OPEN = 4 * 1024 * 1024;

export interface Doc {
  key: string;
  hostId: string;
  path: string;
  status: "loading" | "ready" | "error";
  error: string;
  /** Why the file is shown as a note instead of its contents. */
  unreadable: "" | "binary" | "large";
  size: number;
  /** The daemon's name for the contents that were read or written last. */
  version: string;
  dirty: boolean;
  /** The file changed on disk while there were unsaved edits. */
  conflict: boolean;
  /** Goes up whenever the text read from disk is replaced. */
  revision: number;
}

interface Contents {
  saved: string;
  /** The editor's state, kept while the file's tab is not in front. */
  state: EditorState | null;
  crlf: boolean;
  bom: boolean;
}

export const docs = signal<Record<string, Doc>>({});
const contents = new Map<string, Contents>();
/** Lines to show once a file has been opened, counted from 1. */
const reveals = new Map<string, number>();

export function docKey(hostId: string, path: string): string {
  return hostId + "\n" + path;
}

function patch(key: string, change: Partial<Doc>): void {
  const doc = docs.value[key];
  if (doc) docs.value = { ...docs.value, [key]: { ...doc, ...change } };
}

interface Whole {
  bytes: Uint8Array;
  version: string;
  size: number;
}

/** Reads a whole file, piece by piece, as it was at one moment. */
async function read(hostId: string, path: string): Promise<Whole | "large"> {
  const client = clientOf(hostId);
  for (let attempt = 0; ; attempt++) {
    const parts: Uint8Array[] = [];
    let offset = 0;
    let version = "";
    let size = 0;
    let moved = false;
    for (;;) {
      const piece = await client.request("fs.read", { path, offset, length: MAX_FILE_CHUNK });
      if (piece.size > MAX_OPEN) return "large";
      // Another version means the file changed between two pieces, and
      // what was read so far belongs to a file that no longer exists.
      if (parts.length > 0 && piece.version !== version) {
        moved = true;
        break;
      }
      version = piece.version;
      size = piece.size;
      const bytes = fromBase64(piece.data);
      parts.push(bytes);
      offset += bytes.length;
      if (offset >= size || bytes.length === 0) break;
    }
    if (moved && attempt < 4) continue;
    const all = new Uint8Array(offset);
    let at = 0;
    for (const part of parts) {
      all.set(part, at);
      at += part.length;
    }
    return { bytes: all, version, size };
  }
}

async function loadInto(key: string, hostId: string, path: string): Promise<void> {
  try {
    const file = await read(hostId, path);
    if (!docs.value[key]) return; // closed while loading
    if (file === "large") {
      patch(key, { status: "ready", unreadable: "large" });
      return;
    }
    let text: string;
    try {
      if (file.bytes.subarray(0, 8000).includes(0)) throw new Error("binary");
      text = new TextDecoder("utf-8", { fatal: true }).decode(file.bytes);
    } catch {
      patch(key, { status: "ready", unreadable: "binary", size: file.size, version: file.version });
      return;
    }
    const bom = file.bytes[0] === 0xef && file.bytes[1] === 0xbb && file.bytes[2] === 0xbf;
    const crlf = text.includes("\r\n");
    contents.set(key, { saved: crlf ? text.replace(/\r\n/g, "\n") : text, state: null, crlf, bom });
    patch(key, {
      status: "ready",
      unreadable: "",
      error: "",
      size: file.size,
      version: file.version,
      dirty: false,
      conflict: false,
      revision: (docs.value[key]?.revision ?? 0) + 1,
    });
  } catch (error) {
    patch(key, { status: "error", error: errorText(error) });
  }
}

/** Starts reading a file unless it is open already. */
export function openDoc(hostId: string, path: string): string {
  const key = docKey(hostId, path);
  if (!docs.value[key]) {
    docs.value = {
      ...docs.value,
      [key]: {
        key, hostId, path,
        status: "loading", error: "", unreadable: "",
        size: 0, version: "", dirty: false, conflict: false, revision: 0,
      },
    };
    void loadInto(key, hostId, path);
  }
  return key;
}

export function closeDoc(key: string): void {
  if (!docs.value[key]) return;
  const { [key]: _, ...rest } = docs.value;
  docs.value = rest;
  contents.delete(key);
  reveals.delete(key);
}

/** The text as it was last read from or written to disk. */
export function savedText(key: string): string {
  return contents.get(key)?.saved ?? "";
}

/** The text as it is in the editor now. */
export function currentText(key: string): string {
  const c = contents.get(key);
  return c ? (c.state ? c.state.sliceDoc() : c.saved) : "";
}

export function keptState(key: string): EditorState | null {
  return contents.get(key)?.state ?? null;
}

export function keepState(key: string, state: EditorState): void {
  const c = contents.get(key);
  if (!c) return;
  c.state = state;
  const dirty = state.sliceDoc() !== c.saved;
  if (docs.value[key] && docs.value[key]!.dirty !== dirty) patch(key, { dirty });
}

export function reveal(key: string, line: number): void {
  reveals.set(key, line);
  revealed.value++;
}

export function takeReveal(key: string): number | undefined {
  const line = reveals.get(key);
  reveals.delete(key);
  return line;
}

/** Changes when a line is waiting to be shown. */
export const revealed = signal(0);

/** Goes up after every save, for views that show what is on disk. */
export const saved = signal(0);

export async function saveDoc(key: string): Promise<void> {
  const doc = docs.value[key];
  const c = contents.get(key);
  if (!doc || !c || doc.status !== "ready" || doc.unreadable) return;
  const text = currentText(key);
  const bytes = new TextEncoder().encode((c.bom ? "﻿" : "") + (c.crlf ? text.replace(/\n/g, "\r\n") : text));
  const client = clientOf(doc.hostId);
  try {
    // The first piece replaces the file, but only if it is still the one
    // that was read. The rest is added to the end, each piece only if the
    // one before it is still the last thing written.
    let expect: string | undefined = doc.conflict || doc.version === "" ? undefined : doc.version;
    let result = { size: 0, version: "" };
    for (let at = 0; at === 0 || at < bytes.length; at += MAX_FILE_CHUNK) {
      result = await client.request("fs.write", {
        path: doc.path,
        data: toBase64(bytes.subarray(at, at + MAX_FILE_CHUNK)),
        ...(expect === undefined ? {} : { expect }),
        ...(at > 0 ? { append: true } : {}),
      });
      expect = result.version;
    }
    c.saved = text;
    patch(key, {
      version: result.version,
      size: result.size,
      conflict: false,
      dirty: currentText(key) !== text,
    });
    saved.value++;
  } catch (error) {
    if (error instanceof RequestError && (error.code === "conflict" || error.code === "not_found")) {
      patch(key, { conflict: true });
      return;
    }
    report(error);
  }
}

/** Reads the file again, dropping any unsaved edits. */
export async function reloadDoc(key: string): Promise<void> {
  const doc = docs.value[key];
  if (!doc) return;
  await loadInto(key, doc.hostId, doc.path);
}

/**
 * Looks whether a file changed on disk. An agent may be editing the same file,
 * so a file with no unsaved edits simply follows the disk.
 */
export async function checkDoc(key: string): Promise<void> {
  const doc = docs.value[key];
  if (!doc || doc.status !== "ready" || doc.unreadable === "large") return;
  try {
    const stat = await clientOf(doc.hostId).request("fs.stat", { path: doc.path });
    const now = docs.value[key];
    if (!now || stat.version === now.version) return;
    if (now.dirty) patch(key, { conflict: true });
    else await loadInto(key, doc.hostId, doc.path);
  } catch {
    // Deleted or unreachable. The tab keeps what it has.
  }
}
