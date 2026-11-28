import assert from "node:assert/strict";
import { test } from "node:test";

import type { Session } from "@worky/protocol";

import { agentOf, isShell, modelOf, statusOf, titleOf, worst } from "./agents.ts";

function session(change: Partial<Session>): Session {
  return {
    id: "s_1",
    name: "claude",
    cwd: "/src/app",
    command: ["claude"],
    status: "running",
    exitCode: null,
    cols: 80,
    rows: 24,
    sizeOwner: null,
    worktree: null,
    createdAt: "2026-11-26T10:00:00Z",
    title: "",
    activity: "idle",
    activityAt: "2026-11-26T10:05:00Z",
    ...change,
  };
}

const before = Date.parse("2026-11-26T10:04:00Z");
const after = Date.parse("2026-11-26T10:06:00Z");

test("a shell is told from an agent by its program, whatever the path", () => {
  assert.ok(isShell(session({ command: ["C:\\Program Files\\PowerShell\\7\\pwsh.exe"] })));
  assert.ok(isShell(session({ command: ["/bin/zsh", "-l"] })));
  assert.ok(!isShell(session({ command: ["claude"] })));
  assert.ok(!isShell(session({ command: ["my-own-agent", "--fast"] })));
});

test("known agents get their name and unknown ones keep their own", () => {
  assert.equal(agentOf(session({ command: ["claude"] })).name, "Claude Code");
  assert.equal(agentOf(session({ command: ["C:\\npm\\codex.cmd"] })).name, "Codex CLI");
  assert.equal(agentOf(session({ command: ["npx", "gemini"] })).name, "Gemini CLI");
  assert.equal(agentOf(session({ command: ["my-own-agent"] })).name, "my-own-agent");
  assert.equal(agentOf(session({ command: ["bash"] })).id, "shell");
});

test("the model is read from the command line", () => {
  assert.equal(modelOf(session({ command: ["claude", "--model", "claude-fable-5-2"] })), "claude-fable-5-2");
  assert.equal(modelOf(session({ command: ["codex", "--model=o9"] })), "o9");
  assert.equal(modelOf(session({ command: ["codex", "-m", "o9"] })), "o9");
  assert.equal(modelOf(session({ command: ["claude"] })), "");
});

test("a title loses the symbol an agent animates in front of it", () => {
  assert.equal(titleOf(session({ title: "✳ 로그인 리다이렉트 수정" })), "로그인 리다이렉트 수정");
  assert.equal(titleOf(session({ title: "⠂ Fix the login redirect" })), "Fix the login redirect");
  assert.equal(titleOf(session({ title: "" })), "claude");
  // A shell's title is its own path, which says nothing.
  assert.equal(titleOf(session({ command: ["pwsh"], name: "pwsh", title: "C:\\pwsh.exe" })), "pwsh");
});

test("an agent that went quiet waits until it has been looked at", () => {
  assert.equal(statusOf(session({ activity: "busy" }), after), "run");
  assert.equal(statusOf(session({ activity: "idle" }), before), "wait");
  assert.equal(statusOf(session({ activity: "idle" }), after), "idle");
  assert.equal(statusOf(session({ status: "exited" }), before), "done");
  assert.equal(statusOf(session({ status: "exited" }), after), "idle");
});

test("a shell never asks for attention", () => {
  const shell = { command: ["bash"], name: "bash" };
  assert.equal(statusOf(session({ ...shell, activity: "busy" }), before), "idle");
  assert.equal(statusOf(session({ ...shell, activity: "idle" }), before), "idle");
  assert.equal(statusOf(session({ ...shell, status: "exited" }), before), "idle");
});

test("the state that needs the user most wins", () => {
  assert.equal(worst([]), "idle");
  assert.equal(worst(["idle", "done", "run"]), "run");
  assert.equal(worst(["run", "wait", "done"]), "wait");
});
