// The left panel: projects as folders, each holding its workspaces.

import { STATUS_LABEL, statusOf, type Status } from "../lib/agents.ts";
import {
  agentOf,
  ask,
  closedProjects,
  clientOf,
  counts,
  current,
  dialog,
  filter,
  groups,
  hosts,
  modelOf,
  now,
  OS_LABEL,
  query,
  removeHost,
  report,
  retryLocal,
  seen,
  select,
  showTab,
  titleOf,
  type Filter,
  type Host,
  type ProjectGroup,
  type Workspace,
} from "../lib/store.ts";
import { ago, classes, errorText } from "../lib/util.ts";
import { AgentMark, Chevron, FoldIcon, MoreIcon, PlusIcon, SearchIcon, ServerIcon, StatusGlyph } from "./Icons.tsx";
import { openMenu } from "./Menu.tsx";

function hostLabel(host: Host): string {
  const name = host.config.local ? "로컬" : host.config.label;
  return host.state === "open" ? `${name} ${OS_LABEL[host.os]}` : name;
}

function matches(ws: Workspace, text: string): boolean {
  return [ws.title, ws.name, ws.branch, ws.host.config.label, ws.host.hostname, ...ws.sessions.map((s) => agentOf(s).name)]
    .some((field) => field.toLowerCase().includes(text));
}

function passes(ws: Workspace, f: Filter): boolean {
  if (f === "all") return true;
  if (f === "remote") return !ws.host.config.local;
  return ws.status === f;
}

async function removeWorkspace(ws: Workspace, force: boolean): Promise<void> {
  const question = ws.sessions.length > 0
    ? `세션 ${ws.sessions.length}개를 닫고 "${ws.name}" 작업공간을 삭제할까요?`
    : `"${ws.name}" 작업공간을 삭제할까요?`;
  if (!force && !(await ask(question, "삭제", `${ws.path} 폴더가 지워집니다. 브랜치 ${ws.branch}는 남습니다.`))) return;
  const client = clientOf(ws.hostId);
  try {
    // The folder cannot go while a process still runs in it.
    await Promise.all(ws.sessions.map((s) => client.request("session.close", { sessionId: s.id })));
    await client.request("worktree.remove", { projectId: ws.projectId, path: ws.path, force });
    if (current.value?.key === ws.key) select(null);
  } catch (error) {
    if (!force && (await ask("커밋하지 않은 변경을 버리고 삭제할까요?", "버리고 삭제", errorText(error)))) {
      await removeWorkspace({ ...ws, sessions: [] }, true);
      return;
    }
    if (force) report(error);
  }
}

function WorkspaceRow({ ws }: { ws: Workspace }) {
  const open = current.value?.key === ws.key;
  const agents = ws.sessions.filter((s) => agentOf(s).id !== "shell");
  const marks = [...new Map(agents.map((s) => [agentOf(s).id, agentOf(s)])).values()];
  const model = agents.map(modelOf).find(Boolean) ?? "";
  const showMeta = open || ws.status !== "idle" || !ws.host.config.local;
  const tip = `${ws.name} · ${ws.branch} · ${hostLabel(ws.host)} · ${STATUS_LABEL[ws.status]}`;

  const menu = (event: MouseEvent) =>
    openMenu(event, [
      { label: "작업공간 삭제", danger: true, disabled: ws.tree.main, onPick: () => void removeWorkspace(ws, false) },
    ]);

  return (
    <div class={classes("ws", open && "open", ws.status === "idle" && "quiet")} onContextMenu={menu}>
      <button class="ws-head" title={tip} aria-expanded={open} onClick={() => select(ws)}>
        <span class="ws-line">
          <StatusGlyph status={ws.status} />
          <span class="ws-title">{ws.title}</span>
          <span class="ws-age">{ws.activityAt ? ago(ws.activityAt, now.value) : ""}</span>
        </span>
        {showMeta && (
          <span class="ws-meta">
            <span class={classes("chip", !ws.host.config.local && "remote")}>{hostLabel(ws.host)}</span>
            {!open && marks.map((kind) => <AgentMark key={kind.id} kind={kind} />)}
            <span class="ws-detail">{open ? `${ws.name} · ${ws.branch}` : model}</span>
          </span>
        )}
      </button>
      <div class={classes("fold", open && "open")}>
        <div class="fold-inner">
          {ws.sessions.map((s) => {
            const state: Status = statusOf(s, seen.value[s.id] ?? 0);
            const kind = agentOf(s);
            return (
              <button
                key={s.id}
                class="ws-session"
                title={`${kind.name}${modelOf(s) ? " · " + modelOf(s) : ""} · ${STATUS_LABEL[state]}`}
                onClick={() => {
                  select(ws);
                  showTab(ws, "s:" + s.id);
                }}
              >
                <span class={`dot ${state}`} role="img" aria-label={STATUS_LABEL[state]} />
                <AgentMark kind={kind} />
                <span class="ws-session-title">{titleOf(s)}</span>
                <span class="ws-session-model">{modelOf(s)}</span>
              </button>
            );
          })}
          {open && ws.sessions.length > 0 && <div style={{ height: "5px" }} />}
        </div>
      </div>
    </div>
  );
}

function ProjectFolder({ group, forced }: { group: ProjectGroup; forced: boolean }) {
  const text = query.value.trim().toLowerCase();
  const nameHit = text !== "" && group.name.toLowerCase().includes(text);
  const items = group.workspaces.filter(
    (ws) => passes(ws, filter.value) && (text === "" || nameHit || matches(ws, text)),
  );
  // A filter or a search hides the folders that have nothing to show.
  if (items.length === 0 && forced) return null;

  const open = forced || !closedProjects.value[group.name];
  const waiting = group.workspaces.filter((w) => w.status === "wait").length;
  const running = group.workspaces.filter((w) => w.status === "run").length;

  const menu = (event: MouseEvent) => {
    event.stopPropagation();
    openMenu(event.type === "contextmenu" ? event : (event.currentTarget as Element), [
      { label: "새 작업공간", onPick: () => (dialog.value = { kind: "workspace", project: group.name }) },
      null,
      ...group.members.map(({ host, project }) => ({
        label: group.members.length > 1 ? `목록에서 제거 (${hostLabel(host)})` : "목록에서 제거",
        danger: true,
        onPick: () => {
          void ask(`"${project.name}"을 목록에서 제거할까요?`, "제거", "디스크의 파일과 작업공간은 그대로 남습니다.").then((ok) => {
            if (ok) clientOf(host.config.id).request("project.remove", { projectId: project.id }).catch(report);
          });
        },
      })),
    ]);
  };

  return (
    <div class="project">
      <div class="project-head" onContextMenu={menu}>
        <button
          class="project-toggle"
          aria-expanded={open}
          onClick={() => (closedProjects.value = { ...closedProjects.value, [group.name]: open })}
        >
          <Chevron open={open} />
          <span class="project-name">{group.name}</span>
          {!open && waiting > 0 && (
            <span class="rollup wait" title="입력 대기">
              <StatusGlyph status="wait" size={11} />
              {waiting}
            </span>
          )}
          {!open && running > 0 && (
            <span class="rollup" title="실행 중">
              <StatusGlyph status="run" size={11} />
              {running}
            </span>
          )}
          {!open && waiting === 0 && running === 0 && (
            <span class="rollup total" title="작업공간 수">{group.workspaces.length}</span>
          )}
        </button>
        <button class="icon-button small project-more" aria-label={`${group.name} 메뉴`} onClick={menu}>
          <MoreIcon />
        </button>
      </div>
      <div class={classes("fold", open && "open")}>
        <div class="fold-inner">
          {items.map((ws) => (
            <WorkspaceRow key={ws.key} ws={ws} />
          ))}
          {items.length === 0 && <div class="empty-line">작업공간이 없습니다.</div>}
          <div style={{ height: "4px" }} />
        </div>
      </div>
    </div>
  );
}

function HostNotes() {
  const down = hosts.value.filter((h) => h.state !== "open");
  if (down.length === 0) return null;
  return (
    <div class="host-notes">
      {down.map((host) => {
        const failed = host.state === "failed";
        const text = failed
          ? host.detail === "unauthorized"
            ? "토큰이 맞지 않습니다"
            : host.detail === "unsupported_protocol"
              ? "데몬 버전이 맞지 않습니다"
              : "연결할 수 없습니다"
          : "연결 중…";
        return (
          <div key={host.config.id} class="host-note">
            <span class={classes("dot", failed ? "wait" : "idle")} />
            <span class="host-note-text">
              {host.config.local ? "로컬 데몬" : host.config.label} · {text}
            </span>
            {host.config.local ? (
              <button class="text-button" onClick={() => void retryLocal()}>
                다시 시도
              </button>
            ) : (
              <button class="text-button" onClick={() => removeHost(host.config.id)}>
                제거
              </button>
            )}
          </div>
        );
      })}
    </div>
  );
}

export function Sidebar() {
  const all = groups.value;
  const f = filter.value;
  const forced = f !== "all" || query.value.trim() !== "";
  const anyOpen = all.some((g) => !closedProjects.value[g.name]);
  const n = counts.value;
  const flip = (id: Filter) => () => (filter.value = f === id ? "all" : id);

  const addMenu = (event: MouseEvent) =>
    openMenu(event.currentTarget as Element, [
      { label: "새 작업공간", disabled: all.length === 0, onPick: () => (dialog.value = { kind: "workspace" }) },
      { label: "프로젝트 추가", onPick: () => (dialog.value = { kind: "project" }) },
      null,
      { label: "원격 기기 추가", onPick: () => (dialog.value = { kind: "host" }) },
      ...hosts.value
        .filter((h) => !h.config.local)
        .map((h) => ({ label: `${h.config.label} 연결 끊기`, danger: true, onPick: () => removeHost(h.config.id) })),
    ]);

  const folders = all.map((g) => <ProjectFolder key={g.name} group={g} forced={forced} />);
  const shown = all.some(
    (g) =>
      !forced ||
      g.workspaces.some((ws) => {
        const text = query.value.trim().toLowerCase();
        return passes(ws, f) && (text === "" || g.name.toLowerCase().includes(text) || matches(ws, text));
      }),
  );

  return (
    <aside class="panel sidebar">
      <div class="sidebar-top">
        <label class="field">
          <SearchIcon />
          <input
            type="text"
            aria-label="작업, 프로젝트, 기기 찾기"
            placeholder="찾기"
            value={query.value}
            onInput={(e) => (query.value = (e.target as HTMLInputElement).value)}
          />
        </label>
        <button class="icon-button" aria-label="새 작업공간, 프로젝트 또는 기기" onClick={addMenu}>
          <PlusIcon />
        </button>
      </div>

      <HostNotes />

      <div class="sidebar-list scroll">
        {folders}
        {all.length === 0 && (
          <div class="empty">
            <p>아직 프로젝트가 없습니다.</p>
            <button class="button" onClick={() => (dialog.value = { kind: "project" })}>
              프로젝트 추가
            </button>
          </div>
        )}
        {all.length > 0 && !shown && <div class="empty-line">일치하는 작업공간이 없습니다.</div>}
      </div>

      <div class="sidebar-bottom">
        <button class={classes("pill wait", f === "wait" && "on")} aria-pressed={f === "wait"} title="입력 대기만 보기" onClick={flip("wait")}>
          <StatusGlyph status="wait" size={12} />
          {n.wait}
        </button>
        <button class={classes("pill", f === "run" && "on")} aria-pressed={f === "run"} title="실행 중만 보기" onClick={flip("run")}>
          <StatusGlyph status="run" size={12} />
          {n.run}
        </button>
        <button class={classes("pill", f === "remote" && "on")} aria-pressed={f === "remote"} title="원격 기기만 보기" onClick={flip("remote")}>
          <ServerIcon />
          {n.remote}
        </button>
        <div class="grow" />
        <button
          class="icon-button small"
          aria-label="프로젝트 모두 접기 또는 펼치기"
          onClick={() => (closedProjects.value = Object.fromEntries(all.map((g) => [g.name, anyOpen])))}
        >
          <FoldIcon />
        </button>
      </div>
    </aside>
  );
}
