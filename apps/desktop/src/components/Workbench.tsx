// The middle of the window: a workspace's tabs and whatever the front tab
// shows. Agents and terminals share one row of tabs with files.

import { signal } from "@preact/signals";
import type { GitFile } from "@worky/protocol";
import { useEffect } from "preact/hooks";

import { LAUNCHERS, STATUS_LABEL, statusOf } from "../lib/agents.ts";
import { checkDoc, closeDoc, currentText, docKey, docs, openDoc, reloadDoc, saveDoc } from "../lib/docs.ts";
import { baseName, parent, relative, tilde } from "../lib/paths.ts";
import {
  agentOf,
  allWorkspaces,
  ask,
  closeFile,
  closeSession,
  counts,
  current,
  dialog,
  frontTab,
  gitStatus,
  modelOf,
  OS_LABEL,
  rightOpen,
  seen,
  select,
  showTab,
  startSession,
  tabs,
  theme,
  THEMES,
  titleOf,
  watch,
  type Tab,
  type Workspace,
} from "../lib/store.ts";
import { classes } from "../lib/util.ts";
import { CodeEditor, cursorInfo } from "./CodeEditor.tsx";
import {
  AgentMark,
  BranchIcon,
  CloseIcon,
  FileIcon,
  LaptopIcon,
  PanelIcon,
  PlusIcon,
  ServerIcon,
  StatusGlyph,
  ThemeIcon,
} from "./Icons.tsx";
import { MarkdownView, outline, outlineOpen, stats } from "./MarkdownView.tsx";
import { openMenu } from "./Menu.tsx";
import { TerminalView } from "./TerminalView.tsx";

type MdMode = "preview" | "source";
/** How each Markdown file is shown, by document key. Preview unless chosen. */
const mdModes = signal<Record<string, MdMode>>({});

/** Shows a Markdown file as text, for when a line in it is about to be shown. */
export function showSource(key: string): void {
  if (mdModes.value[key] !== "source") mdModes.value = { ...mdModes.value, [key]: "source" };
}

function isMarkdown(path: string): boolean {
  return /\.(md|markdown|mdx)$/i.test(path);
}

/** The letter shown for a changed file, the way the mockup shows it. */
export function gitLetter(file: GitFile | undefined): { letter: string; tip: string; cls: string } | null {
  if (!file) return null;
  if (file.index === "?") return { letter: "U", tip: "새 파일", cls: "new" };
  const code = file.worktree !== "." ? file.worktree : file.index;
  if (code === "D") return { letter: "D", tip: "삭제됨", cls: "del" };
  if (code === "A") return { letter: "A", tip: "추가됨", cls: "new" };
  if (code === "R") return { letter: "R", tip: "이름 바뀜", cls: "mod" };
  if (code === "U") return { letter: "!", tip: "충돌", cls: "del" };
  return { letter: "M", tip: "수정됨", cls: "mod" };
}

async function closeFileTab(ws: Workspace, path: string): Promise<void> {
  const key = docKey(ws.hostId, path);
  if (docs.value[key]?.dirty && !(await ask(`${baseName(path)}의 변경을 버릴까요?`, "버리기", "저장하지 않은 변경이 사라집니다."))) return;
  closeFile(ws, path);
  closeDoc(key);
}

function TabButton({ ws, tab, front }: { ws: Workspace; tab: Tab; front: boolean }) {
  const pick = () => showTab(ws, tab.id);
  const middle = (event: MouseEvent, close: () => unknown) => {
    if (event.button === 1) {
      event.preventDefault();
      close();
    }
  };

  if (tab.kind === "session") {
    const s = tab.session!;
    const kind = agentOf(s);
    const state = statusOf(s, seen.value[s.id] ?? 0);
    const close = async () => {
      if (s.status === "running" && !(await ask(`"${titleOf(s)}" 세션을 끝낼까요?`, "끝내기", "실행 중인 프로세스가 종료됩니다."))) return;
      void closeSession(ws.hostId, s.id);
    };
    return (
      <div class={classes("tab", front && "front")} onAuxClick={(e) => middle(e, close)}>
        <button class="tab-main" aria-pressed={front} title={`${kind.name} · ${STATUS_LABEL[state]}`} onClick={pick}>
          {kind.id !== "shell" && <StatusGlyph status={state} />}
          <AgentMark kind={kind} />
          <span class="tab-title">{titleOf(s)}</span>
          {modelOf(s) && <span class="tab-model">{modelOf(s)}</span>}
        </button>
        <button class="tab-close" aria-label="세션 닫기" onClick={() => void close()}>
          <CloseIcon />
        </button>
      </div>
    );
  }

  const path = tab.path!;
  const doc = docs.value[docKey(ws.hostId, path)];
  const status = gitStatus.value[ws.key];
  const letter = gitLetter(status?.files.find((f) => f.path === relative(path, status.root, ws.host.os)));
  const close = () => void closeFileTab(ws, path);
  return (
    <div class={classes("tab", front && "front")} onAuxClick={(e) => middle(e, close)}>
      <button class="tab-main" aria-pressed={front} title={path} onClick={pick}>
        <FileIcon />
        <span class="tab-title">{baseName(path)}</span>
        {letter && (
          <span class={`git-letter ${letter.cls}`} title={letter.tip}>
            {letter.letter}
          </span>
        )}
      </button>
      <button class="tab-close" aria-label={doc?.dirty ? "저장하지 않음, 닫기" : "닫기"} onClick={close}>
        {doc?.dirty ? <span class="dirty-dot" /> : <CloseIcon />}
      </button>
    </div>
  );
}

function TabStrip({ ws, front }: { ws: Workspace; front: Tab | null }) {
  const add = (event: MouseEvent) =>
    openMenu(event.currentTarget as Element, [
      ...LAUNCHERS.map((l) => ({
        label: l.label,
        icon: <AgentMark kind={l.kind} />,
        onPick: () => void startSession(ws, l.command),
      })),
      null,
      { label: "명령 실행…", onPick: () => (dialog.value = { kind: "command", workspace: ws }) },
    ]);

  return (
    <div class="tabs">
      <div class="tabs-list">
        {tabs.value.map((tab) => (
          <TabButton key={tab.id} ws={ws} tab={tab} front={front?.id === tab.id} />
        ))}
        <button class="icon-button tab-add" aria-label="새 탭: 에이전트, 터미널 또는 명령" onClick={add}>
          <PlusIcon />
        </button>
      </div>
      <button
        class={classes("icon-button tab-add", rightOpen.value && "on")}
        aria-label="파일과 Git 패널 열기 또는 접기"
        aria-pressed={rightOpen.value}
        onClick={() => (rightOpen.value = !rightOpen.value)}
      >
        <PanelIcon />
      </button>
    </div>
  );
}

function ThemeButton() {
  const pick = (event: MouseEvent) =>
    openMenu(
      event.currentTarget as Element,
      THEMES.map((t) => ({
        label: t.name,
        icon: (
          <span class="swatch" data-theme={t.id}>
            <span />
            <span />
            <span />
          </span>
        ),
        checked: theme.value === t.id,
        onPick: () => (theme.value = t.id),
      })),
    );
  return (
    <button class="status-button" aria-label="테마 바꾸기" title="테마" onClick={pick}>
      <ThemeIcon />
    </button>
  );
}

function nextWaiting(from: Workspace | null): void {
  const waiting = allWorkspaces.value.filter((w) => w.status === "wait");
  if (waiting.length === 0) return;
  const at = from ? waiting.findIndex((w) => w.key === from.key) : -1;
  const next = waiting[(at + 1) % waiting.length]!;
  select(next);
  const session = next.sessions.find((s) => statusOf(s, seen.value[s.id] ?? 0) === "wait");
  if (session) showTab(next, "s:" + session.id);
}

function StatusBar({ ws, front }: { ws: Workspace | null; front: Tab | null }) {
  const n = counts.value;
  const key = ws && front?.kind === "file" ? docKey(ws.hostId, front.path!) : "";
  const doc = key ? docs.value[key] : undefined;
  const md = !!front?.path && isMarkdown(front.path) && (mdModes.value[key] ?? "preview") === "preview";
  const cursor = cursorInfo.value;

  return (
    <div class="status">
      {ws && (
        <>
          <span class="status-host" title="이 작업공간이 실행 중인 기기">
            {ws.host.config.local ? <LaptopIcon /> : <ServerIcon />}
            {ws.host.config.local ? "로컬" : ws.host.config.label} · {OS_LABEL[ws.host.os]}
          </span>
          <span class="status-branch">
            <BranchIcon />
            <span class="mono">{ws.branch}</span>
          </span>
        </>
      )}
      {n.wait > 0 && (
        <button class="status-wait" onClick={() => nextWaiting(ws)}>
          <StatusGlyph status="wait" size={12} />
          입력 대기 {n.wait} · 다음으로 이동
        </button>
      )}
      {n.run > 0 && <span>실행 중 {n.run}</span>}
      <div class="grow" />
      {doc && doc.status === "ready" && !doc.unreadable && !md && (
        <span class="status-group">
          <span>
            Ln {cursor.line}, Col {cursor.column}
          </span>
          <span>UTF-8</span>
          {cursor.language && <span>{cursor.language}</span>}
        </span>
      )}
      {doc && doc.status === "ready" && md && (
        <span class="status-group">
          <span>{stats(currentText(key))}</span>
          {outline.value.length > 0 && (
            <button
              class={classes("status-toggle", outlineOpen.value && "on")}
              aria-pressed={outlineOpen.value}
              onClick={() => (outlineOpen.value = !outlineOpen.value)}
            >
              목차
            </button>
          )}
        </span>
      )}
      <ThemeButton />
    </div>
  );
}

function FileView({ ws, path }: { ws: Workspace; path: string }) {
  const key = docKey(ws.hostId, path);
  const doc = docs.value[key];

  useEffect(() => {
    openDoc(ws.hostId, path);
  }, [key]);

  // Agents edit the same files. The daemon says when something in the
  // file's directory changed, and the file on screen is then looked at again.
  useEffect(() => {
    const dir = parent(path, ws.host.os);
    if (dir === null) return;
    const name = baseName(path);
    const sameName = ws.host.os === "linux" ? (n: string) => n === name : (n: string) => n.toLowerCase() === name.toLowerCase();
    return watch(ws.hostId, dir, (names) => {
      if (names === null || names.some(sameName)) void checkDoc(key);
    });
  }, [key]);

  if (!doc || doc.status === "loading") return <div class="note">여는 중…</div>;
  if (doc.status === "error") return <div class="note">열 수 없습니다 · {doc.error}</div>;
  if (doc.unreadable === "binary") return <div class="note">텍스트가 아닌 파일이라 표시하지 않습니다.</div>;
  if (doc.unreadable === "large") return <div class="note">파일이 너무 커서 열지 않습니다.</div>;

  const md = isMarkdown(path);
  const mode = mdModes.value[key] ?? "preview";
  return (
    <>
      {doc.conflict && (
        <div class="banner">
          <span>디스크의 파일이 바뀌었습니다. 저장하지 않은 변경이 있습니다.</span>
          <button class="text-button" onClick={() => void reloadDoc(key)}>
            디스크 내용으로 되돌리기
          </button>
          <button class="text-button" onClick={() => void saveDoc(key)}>
            내 변경으로 덮어쓰기
          </button>
        </div>
      )}
      {md && mode === "preview" ? (
        <MarkdownView workspace={ws} path={path} text={currentText(key)} />
      ) : (
        <CodeEditor docKey={key} wrap={md} />
      )}
    </>
  );
}

function PathRow({ ws, front }: { ws: Workspace; front: Tab | null }) {
  const os = ws.host.os;
  let text = tilde(ws.path, ws.host.home, os);
  let key = "";
  if (front?.kind === "session") {
    const s = front.session!;
    text = `${tilde(s.cwd, ws.host.home, os)}  ·  ${s.command.join(" ")}  ·  ${ws.branch}`;
  } else if (front?.kind === "file") {
    text = tilde(front.path!, ws.host.home, os);
    key = docKey(ws.hostId, front.path!);
  }
  const md = front?.kind === "file" && isMarkdown(front.path!);
  const mode = mdModes.value[key] ?? "preview";
  const set = (m: MdMode) => () => (mdModes.value = { ...mdModes.value, [key]: m });

  return (
    <div class="pathrow">
      <span class="path mono">{text}</span>
      {md && (
        <div class="segmented" data-at={mode === "source" ? 1 : 0}>
          <span class="segmented-thumb" aria-hidden="true" />
          <button aria-pressed={mode === "preview"} onClick={set("preview")}>
            미리보기
          </button>
          <button aria-pressed={mode === "source"} onClick={set("source")}>
            소스
          </button>
        </div>
      )}
    </div>
  );
}

export function Workbench() {
  const ws = current.value;
  const front = frontTab.value;

  return (
    <main class="panel workbench">
      {ws ? (
        <>
          <TabStrip ws={ws} front={front} />
          <PathRow ws={ws} front={front} />
          <div class="content">
            {front?.kind === "session" && (
              <TerminalView key={front.session!.id} host={ws.host} session={front.session!} />
            )}
            {front?.kind === "file" && <FileView ws={ws} path={front.path!} />}
            {!front && (
              <div class="start">
                <p class="start-title">{ws.title}</p>
                <p class="start-sub mono">
                  {ws.name} · {ws.branch}
                </p>
                <div class="start-actions">
                  {LAUNCHERS.map((l) => (
                    <button key={l.id} class="start-button" onClick={() => void startSession(ws, l.command)}>
                      <AgentMark kind={l.kind} size={15} />
                      {l.label}
                    </button>
                  ))}
                </div>
              </div>
            )}
          </div>
        </>
      ) : (
        <div class="content">
          <div class="start">
            <p class="start-title">작업공간을 고르세요</p>
            <p class="start-sub">왼쪽에서 작업공간을 고르거나 새로 만들면 여기에 에이전트와 파일이 열립니다.</p>
          </div>
        </div>
      )}
      <StatusBar ws={ws} front={front} />
    </main>
  );
}
