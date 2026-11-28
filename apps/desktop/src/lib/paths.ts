// Paths belong to the machine the daemon runs on, not to this one, so
// nothing here asks the local OS. The host's OS says how its paths work.

import type { HostOS } from "@worky/protocol";

export function separator(os: HostOS): string {
  return os === "windows" ? "\\" : "/";
}

function fold(path: string, os: HostOS): string {
  // Windows and the default macOS file system ignore case.
  const text = os === "windows" ? path.replace(/\//g, "\\") : path;
  return os === "linux" ? text : text.toLowerCase();
}

function trimEnd(path: string): string {
  return path.length > 1 ? path.replace(/[\\/]+$/, "") : path;
}

export function samePath(a: string, b: string, os: HostOS): boolean {
  return fold(trimEnd(a), os) === fold(trimEnd(b), os);
}

/** Whether path is root itself or somewhere below it. */
export function isWithin(path: string, root: string, os: HostOS): boolean {
  const p = fold(trimEnd(path), os);
  const r = fold(trimEnd(root), os);
  return p === r || p.startsWith(r.endsWith(separator(os)) ? r : r + separator(os));
}

/** The last part of a path, with either kind of separator. */
export function baseName(path: string): string {
  const trimmed = trimEnd(path);
  return trimmed.slice(Math.max(trimmed.lastIndexOf("/"), trimmed.lastIndexOf("\\")) + 1) || trimmed;
}

/** Everything before the last part of a path. Empty for a bare name. */
export function dirName(path: string): string {
  const at = Math.max(path.lastIndexOf("/"), path.lastIndexOf("\\"));
  return at < 0 ? "" : path.slice(0, at);
}

/** Joins a root on the host with a path that uses forward slashes. */
export function join(root: string, relative: string, os: HostOS): string {
  if (relative === "") return root;
  const sep = separator(os);
  const rest = sep === "/" ? relative : relative.replace(/\//g, sep);
  return root.endsWith(sep) ? root + rest : root + sep + rest;
}

/** The parent directory of an absolute path on the host, or null at a root. */
export function parent(path: string, os: HostOS): string | null {
  const sep = separator(os);
  const trimmed = trimEnd(path);
  const at = trimmed.lastIndexOf(sep);
  if (at < 0) return null;
  if (os === "windows") {
    if (/^[A-Za-z]:$/.test(trimmed)) return null;
    const head = trimmed.slice(0, at);
    return /^[A-Za-z]:$/.test(head) ? head + sep : head;
  }
  if (trimmed === "/") return null;
  return at === 0 ? "/" : trimmed.slice(0, at);
}

/** A path under root as a forward-slash path relative to it. */
export function relative(path: string, root: string, os: HostOS): string {
  if (!isWithin(path, root, os)) return path;
  const rest = trimEnd(path).slice(trimEnd(root).length).replace(/^[\\/]+/, "");
  return rest.replace(/\\/g, "/");
}

/** Shortens the home directory to ~ for display. */
export function tilde(path: string, home: string, os: HostOS): string {
  if (home && isWithin(path, home, os)) {
    return "~" + path.slice(trimEnd(home).length).replace(/\\/g, "/");
  }
  return path;
}
