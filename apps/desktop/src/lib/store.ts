// What the app knows and shows. Sessions, projects and files live in the
// daemons: this keeps a copy of what they said, and the choices that are only
// about this window, such as which tab is open.

import { batch, computed, signal } from "@preact/signals";
import type {
  EventMessage,
  GitStatus,
  HostOS,
  Project,
  Session,
  Worktree,
} from "@worky/protocol";

import { agentOf, isShell, modelOf, statusOf, titleOf, worst, type Status } from "./agents.ts";
import { DaemonClient, type ConnectionState } from "./daemon.ts";
import { baseName, samePath } from "./paths.ts";
import { errorText, load, save } from "./util.ts";
import { changed, reconnected, watchDir, type Listener } from "./watch.ts";

export interface HostConfig {
  id: string;
  label: string;
  url: string;
  token: string;
  /** The daemon on this machine. It is found, not typed in. */
  local: boolean;
}

export interface Host {
  config: HostConfig;
  state: ConnectionState;
  detail: string;
  os: HostOS;
  hostname: string;
  clientId: string;
  home: string;
}

export interface WorkspaceRef {
  hostId: string;
  projectId: string;
  path: string;
}

export interface Workspace extends WorkspaceRef {
  key: string;
  host: Host;
  project: Project;
  tree: Worktree;
  /** The folder's own name. */
  name: string;
  branch: string;
  sessions: Session[];
  status: Status;
  /** What the workspace is doing, in the words of its agent. */
  title: string;
  /** When its agents last changed between working and waiting. */
  activityAt: string;
}

export interface ProjectGroup {
  name: string;
  /** The same project on each machine that has it. */
  members: { host: Host; project: Project }[];
  workspaces: Workspace[];
}

export type ThemeId = "graphite" | "neutral" | "charcoal" | "light";
export type RightView = "files" | "search" | "git";
export type Filter = "all" | "wait" | "run" | "remote";

export const THEMES: { id: ThemeId; name: string }[] = [
  { id: "graphite", name: "쿨 그래파이트" },
  { id: "neutral", name: "중성 회색" },
  { id: "charcoal", name: "웜 차콜" },
  { id: "light", name: "라이트" },
];

export const OS_LABEL: Record<HostOS, string> = { windows: "Windows", linux: "Linux", darwin: "macOS" };

export function workspaceKey(ref: { hostId: string; path: string }): string {
  return ref.hostId + "\n" + ref.path;
}

const clients = new Map<string, DaemonClient>();

export const hosts = signal<Host[]>([]);
/** Projects by host id. Each one carries its worktrees. */
export const projects = signal<Record<string, Project[]>>({});
/** Sessions by host id. */
export const sessions = signal<Record<string, Session[]>>({});
/** Git status by workspace key. */
export const gitStatus = signal<Record<string, GitStatus | null>>({});

export const selected = signal<WorkspaceRef | null>(load<WorkspaceRef | null>("selected", null));
/** Open files by workspace key, as absolute paths on the host. */
export const fileTabs = signal<Record<string, string[]>>(load("fileTabs", {}));
/** The tab in front by workspace key: "s:" and a session id, or "f:" and a path. */
export const activeTab = signal<Record<string, string>>(load("activeTab", {}));
/** When the user last looked at each session, by session id. */
export const seen = signal<Record<string, number>>(load("seen", {}));

export const theme = signal<ThemeId>(load<ThemeId>("theme", "graphite"));
export const rightOpen = signal<boolean>(load("rightOpen", true));
export const rightView = signal<RightView>(load<RightView>("rightView", "files"));
export const closedProjects = signal<Record<string, boolean>>(load("closedProjects", {}));
export const filter = signal<Filter>("all");
export const query = signal("");
export const now = signal(Date.now());
/** A short message about something that went wrong. */
export const notice = signal<string | null>(null);

export type Dialog =
  | { kind: "workspace"; project?: string }
  | { kind: "project" }
  | { kind: "host" }
  | { kind: "command"; workspace: WorkspaceRef }
  | { kind: "confirm"; message: string; detail: string; action: string; resolve: (ok: boolean) => void };
export const dialog = signal<Dialog | null>(null);

/** Asks the user to confirm something that cannot be undone. */
export function ask(message: string, action: string, detail = ""): Promise<boolean> {
  return new Promise((resolve) => {
    dialog.value = { kind: "confirm", message, detail, action, resolve };
  });
}

selected.subscribe((v) => save("selected", v));
fileTabs.subscribe((v) => save("fileTabs", v));
activeTab.subscribe((v) => save("activeTab", v));
seen.subscribe((v) => save("seen", v));
theme.subscribe((v) => {
  save("theme", v);
  document.documentElement.dataset.theme = v;
});
rightOpen.subscribe((v) => save("rightOpen", v));
rightView.subscribe((v) => save("rightView", v));
closedProjects.subscribe((v) => save("closedProjects", v));

window.setInterval(() => (now.value = Date.now()), 30000);

let noticeTimer: number | undefined;
export function report(error: unknown): void {
  notice.value = errorText(error);
  window.clearTimeout(noticeTimer);
  noticeTimer = window.setTimeout(() => (notice.value = null), 6000);
}

export function clientOf(hostId: string): DaemonClient {
  const client = clients.get(hostId);
  if (!client) throw new Error("no such host");
  return client;
}

export function hostOf(hostId: string): Host | undefined {
  return hosts.value.find((h) => h.config.id === hostId);
}

function patchHost(id: string, change: Partial<Host>): void {
  hosts.value = hosts.value.map((h) => (h.config.id === id ? { ...h, ...change } : h));
}

/** Watches a directory on a host until the returned function is called. */
export function watch(hostId: string, path: string, fn: Listener): () => void {
  return watchDir(clientOf(hostId), hostId, path, fn);
}

async function loadHost(hostId: string): Promise<void> {
  const client = clientOf(hostId);
  const hello = client.hello;
  if (!hello) return;
  patchHost(hostId, { os: hello.host.os, hostname: hello.host.hostname, clientId: hello.clientId });
  try {
    const [roots, projectList, sessionList] = await Promise.all([
      client.request("fs.roots"),
      client.request("project.list"),
      client.request("session.list"),
    ]);
    batch(() => {
      patchHost(hostId, { home: roots.home });
      projects.value = { ...projects.value, [hostId]: projectList.projects };
      // Events may have arrived before this answer. What they said is
      // newer than the list, so the two are merged by session id.
      const heard = sessions.value[hostId] ?? [];
      const closed = closedWhileLoading.get(hostId) ?? new Set<string>();
      const listed = sessionList.sessions
        .filter((s) => !closed.has(s.id))
        .map((s) => heard.find((h) => h.id === s.id) ?? s);
      const later = heard.filter((h) => !listed.some((s) => s.id === h.id));
      sessions.value = { ...sessions.value, [hostId]: [...listed, ...later] };
      closedWhileLoading.delete(hostId);
    });
  } catch (error) {
    report(error);
  }
}

/** Sessions closed by an event while a host's list was still on its way. */
const closedWhileLoading = new Map<string, Set<string>>();

function onEvent(hostId: string, message: EventMessage): void {
  const list = sessions.value[hostId] ?? [];
  switch (message.event) {
    case "session.created":
    case "session.updated": {
      const next = message.data.session;
      const at = list.findIndex((s) => s.id === next.id);
      const merged = at < 0 ? [...list, next] : list.map((s, i) => (i === at ? next : s));
      sessions.value = { ...sessions.value, [hostId]: merged };
      const before = at < 0 ? undefined : list[at];
      if (before?.activity === "busy" && next.activity === "idle") quietListeners.forEach((fn) => fn(hostId, next));
      break;
    }
    case "session.closed":
      closedWhileLoading.get(hostId)?.add(message.data.sessionId);
      sessions.value = {
        ...sessions.value,
        [hostId]: list.filter((s) => s.id !== message.data.sessionId),
      };
      break;
    case "project.added":
    case "project.updated": {
      // Either one carries the whole project, worktrees included.
      const next = message.data.project;
      const current = projects.value[hostId] ?? [];
      const known = current.some((p) => p.id === next.id);
      projects.value = {
        ...projects.value,
        [hostId]: known ? current.map((p) => (p.id === next.id ? next : p)) : [...current, next],
      };
      break;
    }
    case "project.removed":
      projects.value = {
        ...projects.value,
        [hostId]: (projects.value[hostId] ?? []).filter((p) => p.id !== message.data.projectId),
      };
      break;
    case "fs.changed":
      changed(hostId, message.data);
      break;
  }
}

const quietListeners = new Set<(hostId: string, session: Session) => void>();
/** Calls fn when a session stops producing output. */
export function onQuiet(fn: (hostId: string, session: Session) => void): () => void {
  quietListeners.add(fn);
  return () => quietListeners.delete(fn);
}

function connect(config: HostConfig): void {
  const client = new DaemonClient({
    url: config.url,
    token: config.token,
    onState: (state, detail) => {
      patchHost(config.id, { state, detail });
      if (state === "open") {
        // What was known before the connection dropped may be out of date.
        sessions.value = { ...sessions.value, [config.id]: [] };
        closedWhileLoading.set(config.id, new Set());
        void loadHost(config.id);
        reconnected(config.id);
      }
    },
    onEvent: (message) => onEvent(config.id, message),
  });
  clients.set(config.id, client);
  hosts.value = [
    ...hosts.value,
    { config, state: "connecting", detail: "", os: "linux", hostname: "", clientId: "", home: "" },
  ];
  client.start();
}

function forget(hostId: string): void {
  clients.get(hostId)?.stop();
  clients.delete(hostId);
  batch(() => {
    hosts.value = hosts.value.filter((h) => h.config.id !== hostId);
    const { [hostId]: _p, ...restProjects } = projects.value;
    projects.value = restProjects;
    const { [hostId]: _s, ...restSessions } = sessions.value;
    sessions.value = restSessions;
  });
}

function remoteConfigs(): HostConfig[] {
  return load<HostConfig[]>("hosts", []);
}

export function addHost(label: string, address: string, token: string): void {
  let url = address.trim();
  if (!/^wss?:\/\//.test(url)) url = "ws://" + url;
  if (!/\/v1\/ws$/.test(url)) url = url.replace(/\/+$/, "") + "/v1/ws";
  const config: HostConfig = {
    id: "h_" + Math.random().toString(36).slice(2, 10),
    label: label.trim() || new URL(url).hostname,
    url,
    token: token.trim(),
    local: false,
  };
  save("hosts", [...remoteConfigs(), config]);
  connect(config);
}

export function removeHost(hostId: string): void {
  save("hosts", remoteConfigs().filter((h) => h.id !== hostId));
  forget(hostId);
}

/** Connects to the local daemon again with another token. */
export function setLocal(token: string, url = LOCAL_URL): void {
  forget("local");
  save("localToken", token);
  connect({ id: "local", label: "로컬", url, token, local: true });
}

export const LOCAL_URL = "ws://127.0.0.1:7433/v1/ws";

interface TauriInternals {
  invoke: (command: string, args?: Record<string, unknown>) => Promise<unknown>;
}

function tauri(): TauriInternals | undefined {
  return (window as unknown as { __TAURI_INTERNALS__?: TauriInternals }).__TAURI_INTERNALS__;
}

/** Finds the local daemon and connects to every known machine. */
export async function start(): Promise<void> {
  document.documentElement.dataset.theme = theme.value;
  let token = "";
  let url = LOCAL_URL;
  const shell = tauri();
  if (shell) {
    try {
      token = (await shell.invoke("local_token")) as string;
    } catch {
      // No token file yet: the daemon has never run on this machine.
    }
  } else {
    // In a plain browser, during development, the page's address says
    // where the daemon is and what its token is.
    const params = new URLSearchParams(location.search);
    token = params.get("token") ?? load("localToken", "");
    url = params.get("daemon") ?? LOCAL_URL;
  }
  connect({ id: "local", label: "로컬", url, token, local: true });
  for (const config of remoteConfigs()) connect(config);
}

/** Reads the local token again, for when the daemon was started after the app. */
export async function retryLocal(): Promise<void> {
  const shell = tauri();
  const before = hostOf("local")?.config;
  let token = before?.token ?? "";
  if (shell) {
    try {
      token = (await shell.invoke("local_token")) as string;
    } catch {
      // Still no token file.
    }
  }
  setLocal(token, before?.url);
}

window.addEventListener("focus", () => (now.value = Date.now()));

// ---- What is shown ----

function describe(host: Host, project: Project, tree: Worktree, all: Session[], seenAt: Record<string, number>): Workspace {
  // The daemon says which worktree a session is in, spelled as the worktree
  // itself is.
  const mine = all.filter((s) => s.worktree === tree.path);
  const agents = mine.filter((s) => !isShell(s));
  const states = agents.map((s) => statusOf(s, seenAt[s.id] ?? 0));
  const latest = [...agents].sort((a, b) => b.activityAt.localeCompare(a.activityAt))[0];
  const branch = tree.branch ?? "분리된 HEAD";
  return {
    key: workspaceKey({ hostId: host.config.id, path: tree.path }),
    hostId: host.config.id,
    projectId: project.id,
    path: tree.path,
    host,
    project,
    tree,
    name: baseName(tree.path),
    branch,
    sessions: mine,
    status: worst(states),
    title: latest ? titleOf(latest) : branch,
    activityAt: latest?.activityAt ?? "",
  };
}

export const groups = computed<ProjectGroup[]>(() => {
  const byName = new Map<string, ProjectGroup>();
  for (const host of hosts.value) {
    const hostSessions = sessions.value[host.config.id] ?? [];
    for (const project of projects.value[host.config.id] ?? []) {
      let group = byName.get(project.name);
      if (!group) {
        group = { name: project.name, members: [], workspaces: [] };
        byName.set(project.name, group);
      }
      group.members.push({ host, project });
      for (const tree of project.worktrees) {
        group.workspaces.push(describe(host, project, tree, hostSessions, seen.value));
      }
    }
  }
  return [...byName.values()];
});

export const allWorkspaces = computed<Workspace[]>(() => groups.value.flatMap((g) => g.workspaces));

export const current = computed<Workspace | null>(() => {
  const ref = selected.value;
  if (!ref) return null;
  return (
    allWorkspaces.value.find(
      (w) => w.hostId === ref.hostId && samePath(w.path, ref.path, w.host.os),
    ) ?? null
  );
});

export interface Tab {
  id: string;
  kind: "session" | "file";
  session?: Session;
  path?: string;
}

export const tabs = computed<Tab[]>(() => {
  const ws = current.value;
  if (!ws) return [];
  const sessionTabs: Tab[] = ws.sessions.map((s) => ({ id: "s:" + s.id, kind: "session", session: s }));
  const files: Tab[] = (fileTabs.value[ws.key] ?? []).map((p) => ({ id: "f:" + p, kind: "file", path: p }));
  return [...sessionTabs, ...files];
});

export const frontTab = computed<Tab | null>(() => {
  const ws = current.value;
  if (!ws) return null;
  const all = tabs.value;
  return all.find((t) => t.id === activeTab.value[ws.key]) ?? all[0] ?? null;
});

export const counts = computed(() => {
  const all = allWorkspaces.value;
  return {
    wait: all.filter((w) => w.status === "wait").length,
    run: all.filter((w) => w.status === "run").length,
    remote: all.filter((w) => !w.host.config.local).length,
  };
});

export { agentOf, modelOf, titleOf };

// ---- What the user does ----

export function select(ws: WorkspaceRef | null): void {
  selected.value = ws ? { hostId: ws.hostId, projectId: ws.projectId, path: ws.path } : null;
}

export function showTab(ws: Workspace, tabId: string): void {
  activeTab.value = { ...activeTab.value, [ws.key]: tabId };
}

export function markSeen(sessionId: string): void {
  seen.value = { ...seen.value, [sessionId]: Date.now() };
}

export function openFile(ws: Workspace, path: string): void {
  const open = fileTabs.value[ws.key] ?? [];
  if (!open.some((p) => samePath(p, path, ws.host.os))) {
    fileTabs.value = { ...fileTabs.value, [ws.key]: [...open, path] };
  }
  const kept = (fileTabs.value[ws.key] ?? []).find((p) => samePath(p, path, ws.host.os)) ?? path;
  showTab(ws, "f:" + kept);
}

export function closeFile(ws: Workspace, path: string): void {
  const open = fileTabs.value[ws.key] ?? [];
  const at = open.indexOf(path);
  if (at < 0) return;
  const all = tabs.value;
  const index = all.findIndex((t) => t.id === "f:" + path);
  fileTabs.value = { ...fileTabs.value, [ws.key]: open.filter((p) => p !== path) };
  if (activeTab.value[ws.key] === "f:" + path) {
    const next = all[index + 1] ?? all[index - 1];
    if (next) showTab(ws, next.id);
  }
}

export async function startSession(ws: Workspace, command?: string[]): Promise<void> {
  try {
    const result = await clientOf(ws.hostId).request("session.create", {
      cwd: ws.path,
      cols: 100,
      rows: 30,
      ...(command ? { command } : {}),
    });
    showTab(ws, "s:" + result.session.id);
  } catch (error) {
    report(error);
  }
}

export async function closeSession(hostId: string, sessionId: string): Promise<void> {
  try {
    await clientOf(hostId).request("session.close", { sessionId });
  } catch (error) {
    report(error);
  }
}

export async function refreshGit(ws: { hostId: string; path: string }): Promise<void> {
  const key = workspaceKey(ws);
  try {
    const status = await clientOf(ws.hostId).request("git.status", { path: ws.path });
    gitStatus.value = { ...gitStatus.value, [key]: status };
  } catch {
    gitStatus.value = { ...gitStatus.value, [key]: null };
  }
}

/** Drops what is remembered about sessions that no longer exist. */
export function prune(): void {
  const alive = new Set(Object.values(sessions.value).flatMap((list) => list.map((s) => s.id)));
  const kept = Object.fromEntries(Object.entries(seen.value).filter(([id]) => alive.has(id)));
  if (Object.keys(kept).length !== Object.keys(seen.value).length) seen.value = kept;
}
