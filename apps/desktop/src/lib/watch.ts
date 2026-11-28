// Directory watches. The daemon tells a connection when the entries of a
// directory it watches change. Several parts of the app can care about the
// same directory, so each one is watched once and shared.

import type { FsChanged } from "@worky/protocol";

import type { DaemonClient } from "./daemon.ts";

/**
 * Called when something in the directory changed. names is null when the
 * daemon cannot say what: look at everything again. ended means the directory
 * itself is gone.
 */
export type Listener = (names: string[] | null, ended: boolean) => void;

interface Entry {
  hostId: string;
  path: string;
  client: DaemonClient;
  /** The daemon's id for the watch, once it has answered. */
  id: number | null;
  /** Goes up whenever an answer that is still on its way stops mattering. */
  turn: number;
  listeners: Set<Listener>;
}

const byPath = new Map<string, Entry>();
const byId = new Map<string, Entry>();

const idKey = (hostId: string, id: number) => hostId + "\n" + id;

function begin(entry: Entry): void {
  const turn = ++entry.turn;
  entry.id = null;
  entry.client.request("fs.watch", { path: entry.path }).then(
    (result) => {
      if (entry.turn !== turn) {
        // Nobody is waiting for this watch any more.
        entry.client.request("fs.unwatch", { watch: result.watch }).catch(() => {});
        return;
      }
      entry.id = result.watch;
      byId.set(idKey(entry.hostId, result.watch), entry);
    },
    () => {
      // Not a directory, or gone. There is nothing to hear about.
    },
  );
}

/** Watches a directory on a host until the returned function is called. */
export function watchDir(client: DaemonClient, hostId: string, path: string, fn: Listener): () => void {
  const key = hostId + "\n" + path;
  let entry = byPath.get(key);
  if (!entry) {
    entry = { hostId, path, client, id: null, turn: 0, listeners: new Set() };
    byPath.set(key, entry);
    begin(entry);
  }
  const mine = entry;
  mine.listeners.add(fn);
  return () => {
    mine.listeners.delete(fn);
    if (mine.listeners.size > 0 || byPath.get(key) !== mine) return;
    byPath.delete(key);
    mine.turn++;
    if (mine.id !== null) {
      byId.delete(idKey(hostId, mine.id));
      client.request("fs.unwatch", { watch: mine.id }).catch(() => {});
    }
  };
}

/** Passes an event from the daemon on to whoever watches that directory. */
export function changed(hostId: string, data: FsChanged): void {
  const entry = byId.get(idKey(hostId, data.watch));
  if (!entry) return;
  if (data.ended) {
    byId.delete(idKey(hostId, data.watch));
    entry.id = null;
  }
  for (const fn of [...entry.listeners]) fn(data.names ?? null, data.ended === true);
}

/**
 * A new connection has none of the old one's watches. They are asked for
 * again, and since changes may have been missed, everyone looks again.
 */
export function reconnected(hostId: string): void {
  for (const entry of byPath.values()) {
    if (entry.hostId !== hostId) continue;
    if (entry.id !== null) byId.delete(idKey(hostId, entry.id));
    begin(entry);
    for (const fn of [...entry.listeners]) fn(null, false);
  }
}
