// What the app can say about a session from its command line and activity.
// Nothing here is special to one agent: an agent is any program that is not a
// shell, and the known names only add a nicer label and mark.

import type { Session } from "@worky/protocol";

export type Status = "wait" | "run" | "done" | "idle";

export const STATUS_LABEL: Record<Status, string> = {
  wait: "입력 대기",
  run: "실행 중",
  done: "완료",
  idle: "쉬는 중",
};

export interface AgentKind {
  id: string;
  name: string;
  /** Path data for a 14 by 14 stroked mark. */
  icon: string;
  /** Whether the mark is drawn in the agent colour or the text colour. */
  tinted: boolean;
}

const TERMINAL: AgentKind = {
  id: "shell",
  name: "터미널",
  icon: "M2.5 3.5h9v7h-9zM4.6 5.8l1.6 1.3-1.6 1.3M7.4 8.6h2",
  tinted: false,
};

const OTHER: AgentKind = {
  id: "agent",
  name: "에이전트",
  icon: "M3 4.2 6.2 7 3 9.8M7.6 10.4h3.6",
  tinted: false,
};

const KNOWN: AgentKind[] = [
  {
    id: "claude",
    name: "Claude Code",
    icon: "M7 2.2v9.6M2.2 7h9.6M3.6 3.6l6.8 6.8M10.4 3.6l-6.8 6.8",
    tinted: true,
  },
  { id: "codex", name: "Codex CLI", icon: "M3 4.2 6.2 7 3 9.8M7.6 10.4h3.6", tinted: false },
  {
    id: "gemini",
    name: "Gemini CLI",
    icon: "M7 1.8c.5 3 2.2 4.7 5.2 5.2-3 .5-4.7 2.2-5.2 5.2-.5-3-2.2-4.7-5.2-5.2 3-.5 4.7-2.2 5.2-5.2z",
    tinted: false,
  },
];

const SHELLS = new Set([
  "sh", "bash", "zsh", "fish", "dash", "ksh", "nu", "pwsh", "powershell", "cmd", "xonsh", "elvish",
]);

function programName(arg: string): string {
  const base = arg.slice(Math.max(arg.lastIndexOf("/"), arg.lastIndexOf("\\")) + 1);
  return base.replace(/\.(exe|cmd|bat|ps1|js|mjs)$/i, "").toLowerCase();
}

export function isShell(session: Session): boolean {
  return SHELLS.has(programName(session.command[0] ?? ""));
}

export function agentOf(session: Session): AgentKind {
  if (isShell(session)) return TERMINAL;
  // The first word can be a launcher such as npx, so look a little further.
  for (const arg of session.command.slice(0, 3)) {
    const name = programName(arg);
    const known = KNOWN.find((k) => name === k.id || name.startsWith(k.id + "-"));
    if (known) return known;
  }
  return { ...OTHER, name: programName(session.command[0] ?? "") || OTHER.name };
}

/** The model named on the command line, or "". */
export function modelOf(session: Session): string {
  const args = session.command;
  for (let i = 1; i < args.length; i++) {
    const arg = args[i]!;
    if ((arg === "--model" || arg === "-m") && i + 1 < args.length) return args[i + 1]!;
    if (arg.startsWith("--model=")) return arg.slice("--model=".length);
  }
  return "";
}

/**
 * What to call a session. Agents put a summary of their task in the window
 * title, often behind a symbol that animates while they work.
 */
export function titleOf(session: Session): string {
  if (!isShell(session)) {
    const title = session.title.replace(/^[^\p{L}\p{N}]+/u, "").trim();
    if (title) return title;
  }
  return session.name;
}

/**
 * The state of one session. seenAt is when the user last looked at it: an
 * agent that went quiet after that is waiting to be answered.
 */
export function statusOf(session: Session, seenAt: number): Status {
  const shell = isShell(session);
  if (session.status === "exited") {
    return !shell && Date.parse(session.activityAt) > seenAt ? "done" : "idle";
  }
  if (session.activity === "busy") return shell ? "idle" : "run";
  if (shell) return "idle";
  return Date.parse(session.activityAt) > seenAt ? "wait" : "idle";
}

const RANK: Record<Status, number> = { wait: 3, run: 2, done: 1, idle: 0 };

/** The state that matters most among several. */
export function worst(states: Status[]): Status {
  return states.reduce<Status>((a, b) => (RANK[b] > RANK[a] ? b : a), "idle");
}

export interface Launcher {
  id: string;
  label: string;
  /** Undefined runs the user's default shell. */
  command?: string[];
  kind: AgentKind;
}

export const LAUNCHERS: Launcher[] = [
  ...KNOWN.map((k) => ({ id: k.id, label: k.name, command: [k.id], kind: k })),
  { id: "shell", label: "터미널", kind: TERMINAL },
];
