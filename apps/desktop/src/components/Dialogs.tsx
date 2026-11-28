// The few forms the app needs: a new workspace, a project to add, another
// machine to connect to, and a command to run.

import type { FsEntry } from "@worky/protocol";
import type { ComponentChildren } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";

import { LAUNCHERS } from "../lib/agents.ts";
import { join, parent } from "../lib/paths.ts";
import {
  addHost,
  allWorkspaces,
  clientOf,
  dialog,
  groups,
  hosts,
  OS_LABEL,
  select,
  startSession,
  workspaceKey,
  type Host,
  type WorkspaceRef,
} from "../lib/store.ts";
import { classes, errorText, load, save, splitCommand } from "../lib/util.ts";
import { Chevron, UpIcon } from "./Icons.tsx";

function close(): void {
  const open = dialog.value;
  dialog.value = null;
  // Closing a question any other way than by its button means no.
  if (open?.kind === "confirm") open.resolve(false);
}

function hostName(host: Host): string {
  return `${host.config.local ? "로컬" : host.config.label} · ${OS_LABEL[host.os]}`;
}

interface FrameProps {
  title: string;
  action: string;
  danger?: boolean;
  busy?: boolean;
  error?: string;
  disabled?: boolean;
  onSubmit: () => void;
  children: ComponentChildren;
}

function Frame({ title, action, danger, busy, error, disabled, onSubmit, children }: FrameProps) {
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    box.current?.querySelector<HTMLElement>("[data-first]")?.focus();
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") close();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
  return (
    <div class="scrim" onMouseDown={(e) => e.target === e.currentTarget && close()}>
      <div class="dialog" ref={box} role="dialog" aria-modal="true" aria-label={title}>
        <form
          class="dialog-form"
          onSubmit={(e) => {
            e.preventDefault();
            if (!busy && !disabled) onSubmit();
          }}
        >
          <h2 class="dialog-title">{title}</h2>
          {children}
          {error && <p class="dialog-error">{error}</p>}
          <div class="dialog-actions">
            <button type="button" class="button quiet" onClick={close}>
              취소
            </button>
            <button type="submit" class={danger ? "button danger" : "button primary"} disabled={busy || disabled}>
              {busy ? "처리 중…" : action}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: ComponentChildren }) {
  return (
    <label class="form-field">
      <span class="form-label">{label}</span>
      {children}
    </label>
  );
}

function WorkspaceDialog({ project }: { project?: string }) {
  const all = groups.value;
  const [name, setName] = useState(project ?? all[0]?.name ?? "");
  const group = all.find((g) => g.name === name);
  const members = (group?.members ?? []).filter((m) => m.host.state === "open");
  const [hostId, setHostId] = useState(members[0]?.host.config.id ?? "");
  const member = members.find((m) => m.host.config.id === hostId) ?? members[0];
  const [branch, setBranch] = useState("");
  const [base, setBase] = useState("");
  const [launcher, setLauncher] = useState<string>(load("lastLauncher", "claude"));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const submit = async () => {
    if (!member) return;
    setBusy(true);
    setError("");
    try {
      const client = clientOf(member.host.config.id);
      const result = await client.request("worktree.create", {
        projectId: member.project.id,
        branch: branch.trim(),
        ...(base.trim() ? { base: base.trim() } : {}),
      });
      save("lastLauncher", launcher);
      const ref: WorkspaceRef = {
        hostId: member.host.config.id,
        projectId: member.project.id,
        path: result.worktree.path,
      };
      select(ref);
      close();
      const chosen = LAUNCHERS.find((l) => l.id === launcher);
      if (chosen) {
        // The list of working trees arrives a moment after the answer.
        const key = workspaceKey(ref);
        const started = Date.now();
        const wait = window.setInterval(() => {
          const ws = allWorkspaces.value.find((w) => w.key === key);
          if (ws) void startSession(ws, chosen.command);
          if (ws || Date.now() - started > 5000) window.clearInterval(wait);
        }, 50);
      }
    } catch (e) {
      setError(errorText(e));
      setBusy(false);
    }
  };

  return (
    <Frame title="새 작업공간" action="만들기" busy={busy} error={error} disabled={!member || branch.trim() === ""} onSubmit={() => void submit()}>
      <Field label="프로젝트">
        <select value={name} onChange={(e) => setName((e.target as HTMLSelectElement).value)}>
          {all.map((g) => (
            <option key={g.name} value={g.name}>
              {g.name}
            </option>
          ))}
        </select>
      </Field>
      {members.length > 1 && (
        <Field label="기기">
          <select value={member?.host.config.id} onChange={(e) => setHostId((e.target as HTMLSelectElement).value)}>
            {members.map((m) => (
              <option key={m.host.config.id} value={m.host.config.id}>
                {hostName(m.host)}
              </option>
            ))}
          </select>
        </Field>
      )}
      <Field label="브랜치">
        <input
          data-first
          type="text"
          class="mono"
          placeholder="feat/login"
          value={branch}
          spellcheck={false}
          onInput={(e) => setBranch((e.target as HTMLInputElement).value)}
        />
      </Field>
      <Field label="시작 지점 (비우면 현재 HEAD)">
        <input
          type="text"
          class="mono"
          placeholder="main"
          value={base}
          spellcheck={false}
          onInput={(e) => setBase((e.target as HTMLInputElement).value)}
        />
      </Field>
      <div class="form-field">
        <span class="form-label">바로 시작할 것</span>
        <div class="choices">
          {[...LAUNCHERS.map((l) => ({ id: l.id, label: l.label })), { id: "", label: "없음" }].map((l) => (
            <button key={l.id} type="button" class={classes("choice", launcher === l.id && "on")} aria-pressed={launcher === l.id} onClick={() => setLauncher(l.id)}>
              {l.label}
            </button>
          ))}
        </div>
      </div>
    </Frame>
  );
}

function ProjectDialog() {
  const ready = hosts.value.filter((h) => h.state === "open");
  const [hostId, setHostId] = useState(ready[0]?.config.id ?? "");
  const host = ready.find((h) => h.config.id === hostId) ?? ready[0];
  const [path, setPath] = useState("");
  const [entries, setEntries] = useState<FsEntry[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  // The folder is picked on the machine the daemon runs on, which may not
  // be this one, so the daemon lists it.
  useEffect(() => {
    if (host) setPath(host.home);
  }, [host?.config.id]);
  useEffect(() => {
    if (!host || path === "") return;
    let live = true;
    clientOf(host.config.id)
      .request("fs.list", { path })
      .then((result) => live && setEntries(result.entries.filter((e) => e.type === "dir")))
      .catch(() => live && setEntries([]));
    return () => {
      live = false;
    };
  }, [host?.config.id, path]);

  const submit = async () => {
    if (!host) return;
    setBusy(true);
    setError("");
    try {
      await clientOf(host.config.id).request("project.add", { path });
      close();
    } catch (e) {
      setError(errorText(e));
      setBusy(false);
    }
  };

  const up = host ? parent(path, host.os) : null;
  const isRepo = entries.some((e) => e.name === ".git");
  return (
    <Frame title="프로젝트 추가" action="추가" busy={busy} error={error} disabled={!host || path === ""} onSubmit={() => void submit()}>
      {ready.length > 1 && (
        <Field label="기기">
          <select value={host?.config.id} onChange={(e) => setHostId((e.target as HTMLSelectElement).value)}>
            {ready.map((h) => (
              <option key={h.config.id} value={h.config.id}>
                {hostName(h)}
              </option>
            ))}
          </select>
        </Field>
      )}
      <Field label="Git 저장소 폴더">
        <div class="path-input">
          <button type="button" class="icon-button small" aria-label="상위 폴더" disabled={!up} onClick={() => up && setPath(up)}>
            <UpIcon />
          </button>
          <input
            data-first
            type="text"
            class="mono"
            value={path}
            spellcheck={false}
            onInput={(e) => setPath((e.target as HTMLInputElement).value)}
          />
        </div>
      </Field>
      <div class="browser scroll">
        {entries
          .filter((e) => e.name !== ".git")
          .map((entry) => (
            <button key={entry.name} type="button" class="row" onClick={() => host && setPath(join(path, entry.name, host.os))}>
              <Chevron open={false} />
              <span class="row-name">{entry.name}</span>
            </button>
          ))}
        {entries.length === 0 && <div class="empty-line">하위 폴더가 없습니다.</div>}
      </div>
      <p class="dialog-hint">{isRepo ? "이 폴더는 Git 저장소입니다." : "저장소 안의 폴더를 고르면 그 저장소가 추가됩니다."}</p>
    </Frame>
  );
}

function HostDialog() {
  const [label, setLabel] = useState("");
  const [address, setAddress] = useState("");
  const [token, setToken] = useState("");
  const [error, setError] = useState("");

  const submit = () => {
    try {
      addHost(label, address, token);
      close();
    } catch (e) {
      setError(errorText(e));
    }
  };

  return (
    <Frame title="원격 기기 추가" action="연결" error={error} disabled={address.trim() === "" || token.trim() === ""} onSubmit={submit}>
      <Field label="이름">
        <input data-first type="text" placeholder="dev-box" value={label} onInput={(e) => setLabel((e.target as HTMLInputElement).value)} />
      </Field>
      <Field label="주소">
        <input
          type="text"
          class="mono"
          placeholder="100.64.0.12:7433"
          value={address}
          spellcheck={false}
          onInput={(e) => setAddress((e.target as HTMLInputElement).value)}
        />
      </Field>
      <Field label="토큰">
        <input type="password" class="mono" value={token} onInput={(e) => setToken((e.target as HTMLInputElement).value)} />
      </Field>
      <p class="dialog-hint">
        그 기기에서 workyd가 실행 중이어야 합니다. 토큰은 그 기기의 설정 폴더 안 worky/token 파일에 있습니다. 연결은 암호화되지
        않으므로 VPN처럼 이미 믿을 수 있는 망에서만 쓰세요.
      </p>
    </Frame>
  );
}

function CommandDialog({ workspace }: { workspace: WorkspaceRef }) {
  const recent = load<string[]>("commands", []);
  const [line, setLine] = useState(recent[0] ?? "");
  const ws = allWorkspaces.value.find((w) => w.key === workspaceKey(workspace));

  const submit = () => {
    const command = splitCommand(line);
    if (!ws || command.length === 0) return;
    save("commands", [line, ...recent.filter((r) => r !== line)].slice(0, 8));
    close();
    void startSession(ws, command);
  };

  return (
    <Frame title="명령 실행" action="실행" disabled={line.trim() === ""} onSubmit={submit}>
      <Field label="이 작업공간에서 새 탭으로 실행할 명령">
        <input
          data-first
          type="text"
          class="mono"
          placeholder="claude --model claude-fable-5-2"
          value={line}
          spellcheck={false}
          onInput={(e) => setLine((e.target as HTMLInputElement).value)}
        />
      </Field>
      {recent.length > 0 && (
        <div class="choices">
          {recent.map((r) => (
            <button key={r} type="button" class="choice mono" onClick={() => setLine(r)}>
              {r}
            </button>
          ))}
        </div>
      )}
    </Frame>
  );
}

function ConfirmDialog({ d }: { d: Extract<NonNullable<typeof dialog.value>, { kind: "confirm" }> }) {
  const yes = () => {
    dialog.value = null;
    d.resolve(true);
  };
  return (
    <Frame title={d.message} action={d.action} danger onSubmit={yes}>
      {d.detail && <p class="dialog-detail">{d.detail}</p>}
      <span data-first tabIndex={-1} />
    </Frame>
  );
}

export function DialogLayer() {
  const d = dialog.value;
  if (!d) return null;
  switch (d.kind) {
    case "workspace":
      return <WorkspaceDialog project={d.project} />;
    case "project":
      return <ProjectDialog />;
    case "host":
      return <HostDialog />;
    case "command":
      return <CommandDialog workspace={d.workspace} />;
    case "confirm":
      return <ConfirmDialog key={d.message} d={d} />;
  }
}
