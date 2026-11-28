// Small helpers with no state of their own.

export function toBase64(bytes: Uint8Array): string {
  let binary = "";
  const step = 0x8000;
  for (let at = 0; at < bytes.length; at += step) {
    binary += String.fromCharCode(...bytes.subarray(at, at + step));
  }
  return btoa(binary);
}

export function fromBase64(text: string): Uint8Array {
  const binary = atob(text);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return bytes;
}

/** Reads a value kept in localStorage, or returns the fallback. */
export function load<T>(key: string, fallback: T): T {
  try {
    const raw = localStorage.getItem("worky." + key);
    return raw === null ? fallback : (JSON.parse(raw) as T);
  } catch {
    return fallback;
  }
}

export function save(key: string, value: unknown): void {
  try {
    localStorage.setItem("worky." + key, JSON.stringify(value));
  } catch {
    // A full or disabled store only costs the user a remembered setting.
  }
}

export function classes(...names: (string | false | null | undefined)[]): string {
  return names.filter(Boolean).join(" ");
}

export function debounce<A extends unknown[]>(fn: (...args: A) => void, ms: number) {
  let timer: number | undefined;
  const run = (...args: A) => {
    window.clearTimeout(timer);
    timer = window.setTimeout(() => fn(...args), ms);
  };
  run.cancel = () => window.clearTimeout(timer);
  return run;
}

/** How long ago a time was, in the short form the sidebar uses. */
export function ago(iso: string, now: number): string {
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return "";
  const minutes = Math.floor((now - then) / 60000);
  if (minutes < 1) return "방금";
  if (minutes < 60) return `${minutes}분`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}시간`;
  const days = Math.floor(hours / 24);
  if (days === 1) return "어제";
  if (days < 7) return `${days}일`;
  return `${Math.floor(days / 7)}주`;
}

/** Splits a command line into arguments, honouring quotes. */
export function splitCommand(line: string): string[] {
  const args: string[] = [];
  let current = "";
  let quote = "";
  let started = false;
  for (const ch of line.trim()) {
    if (quote) {
      if (ch === quote) quote = "";
      else current += ch;
    } else if (ch === '"' || ch === "'") {
      quote = ch;
      started = true;
    } else if (/\s/.test(ch)) {
      if (started || current) args.push(current);
      current = "";
      started = false;
    } else {
      current += ch;
    }
  }
  if (started || current) args.push(current);
  return args;
}

export function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
