// The right panel: the workspace's files, a search through them, and what
// git says has changed.

import { signal } from "@preact/signals";
import type { FsEntry, GitFile, SearchMatch } from "@worky/protocol";
import { useEffect, useRef, useState } from "preact/hooks";

import { docKey, reveal, saved } from "../lib/docs.ts";
import { baseName, dirName, isWithin, join, relative } from "../lib/paths.ts";
import {
  clientOf,
  current,
  frontTab,
  gitStatus,
  onQuiet,
  openFile,
  refreshGit,
  report,
  rightView,
  watch,
  type RightView,
  type Workspace,
} from "../lib/store.ts";
import { classes, debounce, load, save } from "../lib/util.ts";
import { BranchIcon, Chevron, ExplorerIcon, FilterIcon, FindIcon, RefreshIcon, SourceIcon } from "./Icons.tsx";
import { gitLetter, showSource } from "./Workbench.tsx";

/** What each open folder holds, by workspace key and path inside it. */
const listings = signal<Record<string, FsEntry[]>>({});
/** The folders that are open, by workspace key. "" is the root. */
const expanded = signal<Record<string, Record<string, boolean>>>(load("expanded", {}));
expanded.subscribe((v) => save("expanded", v));

const listingKey = (ws: Workspace, dir: string) => ws.key + "\n" + dir;

async function list(ws: Workspace, dir: string): Promise<void> {
  try {
    const result = await clientOf(ws.hostId).request("fs.list", { path: join(ws.path, dir, ws.host.os) });
    // The daemon sorts by name. A tree reads better with the folders first.
    const entries = [...result.entries].sort(
      (a, b) =>
        Number(b.type === "dir") - Number(a.type === "dir") ||
        a.name.toLowerCase().localeCompare(b.name.toLowerCase()) ||
        a.name.localeCompare(b.name),
    );
    listings.value = { ...listings.value, [listingKey(ws, dir)]: entries };
  } catch {
    // A folder that was deleted simply closes.
    const open = { ...(expanded.value[ws.key] ?? {}) };
    if (dir !== "" && open[dir]) {
      delete open[dir];
      expanded.value = { ...expanded.value, [ws.key]: open };
    }
  }
}

function refreshTree(ws: Workspace): void {
  void list(ws, "");
  for (const dir of Object.keys(expanded.value[ws.key] ?? {})) void list(ws, dir);
}

function gitFilesOf(ws: Workspace): Map<string, GitFile> {
  const status = gitStatus.value[ws.key];
  const files = new Map<string, GitFile>();
  if (status) {
    // Git reports paths from the working tree's root, which this workspace is.
    const prefix = relative(ws.path, status.root, ws.host.os);
    for (const file of status.files) {
      if (prefix === "" || prefix === ws.path) files.set(file.path, file);
      else if (file.path.startsWith(prefix + "/")) files.set(file.path.slice(prefix.length + 1), file);
    }
  }
  return files;
}

function Letter({ file }: { file: GitFile | undefined }) {
  const letter = gitLetter(file);
  if (!letter) return null;
  return (
    <span class={`git-letter ${letter.cls}`} title={letter.tip}>
      {letter.letter}
    </span>
  );
}

function Explorer({ ws }: { ws: Workspace }) {
  const [needle, setNeedle] = useState("");
  const [found, setFound] = useState<string[] | null>(null);
  const files = gitFilesOf(ws);
  const open = expanded.value[ws.key] ?? {};
  const front = frontTab.value;
  const active = front?.kind === "file" && isWithin(front.path!, ws.path, ws.host.os)
    ? relative(front.path!, ws.path, ws.host.os)
    : "";

  // Every folder that is shown is watched, so the tree follows what agents
  // and the user's own tools do to it.
  const shown = ["", ...Object.keys(open)].sort().join("\n");
  useEffect(() => {
    const stops = shown.split("\n").map((dir) => {
      void list(ws, dir);
      return watch(ws.hostId, join(ws.path, dir, ws.host.os), (_names, ended) => {
        if (!ended) void list(ws, dir);
        // What changed on disk has likely changed what git says, too.
        void refreshGit(ws);
      });
    });
    return () => stops.forEach((stop) => stop());
  }, [ws.key, shown]);

  // Finding by name asks the daemon, which knows every file, not only the
  // folders that happen to be open here.
  const search = useRef(
    debounce((workspace: Workspace, text: string) => {
      clientOf(workspace.hostId)
        .request("fs.search", { root: workspace.path, query: text, kind: "name" })
        .then((result) => setFound(result.files ?? []))
        .catch(() => setFound([]));
    }, 150),
  );
  useEffect(() => {
    if (needle.trim() === "") {
      search.current.cancel();
      setFound(null);
    } else {
      search.current(ws, needle.trim());
    }
  }, [needle, ws.key]);

  const toggle = (dir: string) => {
    const next = { ...open };
    if (next[dir]) delete next[dir];
    else next[dir] = true;
    expanded.value = { ...expanded.value, [ws.key]: next };
  };

  const pick = (path: string) => openFile(ws, join(ws.path, path, ws.host.os));

  const rows: preact.JSX.Element[] = [];
  const walk = (dir: string, depth: number) => {
    for (const entry of listings.value[listingKey(ws, dir)] ?? []) {
      if (entry.name === ".git") continue;
      const path = dir === "" ? entry.name : dir + "/" + entry.name;
      if (entry.type === "dir") {
        rows.push(
          <button key={path} class="row" style={{ paddingLeft: `${6 + depth * 14}px` }} title={path} onClick={() => toggle(path)}>
            <Chevron open={!!open[path]} />
            <span class="row-name">{entry.name}</span>
          </button>,
        );
        if (open[path]) walk(path, depth + 1);
      } else {
        rows.push(
          <button
            key={path}
            class={classes("row", path === active && "on")}
            style={{ paddingLeft: `${24 + depth * 14}px` }}
            title={path}
            onClick={() => pick(path)}
          >
            <span class="row-name">{entry.name}</span>
            <span class="grow" />
            <Letter file={files.get(path)} />
          </button>,
        );
      }
    }
  };
  if (found === null) walk("", 0);

  return (
    <div class="view">
      <label class="field small">
        <FilterIcon />
        <input
          type="text"
          aria-label="파일 이름으로 찾기"
          placeholder="파일 이름으로 찾기"
          value={needle}
          onInput={(e) => setNeedle((e.target as HTMLInputElement).value)}
        />
      </label>
      <div class="view-list scroll">
        {found === null
          ? rows
          : found.map((path) => (
              <button key={path} class={classes("row", path === active && "on")} style={{ paddingLeft: "10px" }} title={path} onClick={() => pick(path)}>
                <span class="row-name">{baseName(path)}</span>
                <span class="row-dir">{dirName(path)}</span>
                <Letter file={files.get(path)} />
              </button>
            ))}
        {found !== null && found.length === 0 && <div class="empty-line">일치하는 파일이 없습니다.</div>}
      </div>
    </div>
  );
}

/**
 * Where to start showing a matched line, so the match is in view in a narrow
 * panel: a little before it, and never in the indentation. Wide characters,
 * such as Korean ones, take two columns.
 */
function excerptStart(match: SearchMatch): number {
  const lead = match.text.length - match.text.trimStart().length;
  let at = match.start;
  for (let columns = 14; at > lead && columns > 0; at--) {
    columns -= match.text.charCodeAt(at - 1) > 0x2e7f ? 2 : 1;
  }
  return Math.max(lead, at);
}

interface Results {
  groups: { path: string; rows: SearchMatch[] }[];
  hits: number;
  truncated: boolean;
}

function Search({ ws }: { ws: Workspace }) {
  const [text, setText] = useState("");
  const [exact, setExact] = useState(false);
  const [results, setResults] = useState<Results | null>(null);
  const [busy, setBusy] = useState(false);
  const turn = useRef(0);

  const search = useRef(
    debounce((workspace: Workspace, query: string, caseSensitive: boolean) => {
      const mine = ++turn.current;
      setBusy(true);
      clientOf(workspace.hostId)
        .request("fs.search", { root: workspace.path, query, kind: "content", caseSensitive })
        .then((result) => {
          if (mine !== turn.current) return; // a newer search is on its way
          const groups: Results["groups"] = [];
          for (const match of result.matches ?? []) {
            const last = groups[groups.length - 1];
            if (last && last.path === match.path) last.rows.push(match);
            else groups.push({ path: match.path, rows: [match] });
          }
          setResults({ groups, hits: result.matches?.length ?? 0, truncated: result.truncated });
          setBusy(false);
        })
        .catch(() => {
          if (mine === turn.current) setBusy(false);
        });
    }, 250),
  );
  useEffect(() => {
    if (text === "") {
      search.current.cancel();
      turn.current++;
      setResults(null);
      setBusy(false);
    } else {
      search.current(ws, text, exact);
    }
  }, [text, exact, ws.key]);

  const open = (match: SearchMatch) => {
    const path = join(ws.path, match.path, ws.host.os);
    openFile(ws, path);
    // A line number means something in the source, not in a rendered page.
    showSource(docKey(ws.hostId, path));
    reveal(docKey(ws.hostId, path), match.line);
  };

  const summary =
    text === ""
      ? "검색어를 입력하세요"
      : results === null || busy
        ? "검색 중…"
        : results.hits === 0
          ? "결과 없음"
          : `파일 ${results.groups.length}개 · ${results.hits}건${results.truncated ? " 이상" : ""}`;

  return (
    <div class="view">
      <div class="field small">
        <input
          type="text"
          aria-label="작업공간에서 검색"
          placeholder="검색"
          value={text}
          onInput={(e) => setText((e.target as HTMLInputElement).value)}
        />
        <button class={classes("case", exact && "on")} aria-pressed={exact} aria-label="대소문자 구분" title="대소문자 구분" onClick={() => setExact(!exact)}>
          Aa
        </button>
      </div>
      <div class="view-summary">{summary}</div>
      <div class="view-list scroll">
        {results?.groups.map((group) => (
          <div key={group.path} class="result">
            <div class="result-head">
              <span class="row-name strong">{baseName(group.path)}</span>
              <span class="row-dir">{dirName(group.path)}</span>
              <span class="count">{group.rows.length}</span>
            </div>
            {group.rows.map((match, i) => {
              const from = excerptStart(match);
              return (
                <button key={i} class="result-row mono" onClick={() => open(match)}>
                  <span class="result-line">{match.line}</span>
                  <span class="result-text">
                    {match.text.slice(from, match.start)}
                    <mark>{match.text.slice(match.start, match.end)}</mark>
                    {match.text.slice(match.end)}
                  </span>
                </button>
              );
            })}
          </div>
        ))}
      </div>
    </div>
  );
}

function SourceControl({ ws }: { ws: Workspace }) {
  const status = gitStatus.value[ws.key];
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const client = clientOf(ws.hostId);

  if (status === undefined) return <div class="view"><div class="empty-line">불러오는 중…</div></div>;
  if (status === null) return <div class="view"><div class="empty-line">Git 상태를 읽을 수 없습니다.</div></div>;

  const staged = status.files.filter((f) => f.index !== "." && f.index !== "?");
  const changed = status.files.filter((f) => f.worktree !== "." || f.index === "?");

  const run = async (work: Promise<unknown>) => {
    try {
      await work;
    } catch (error) {
      report(error);
    }
    await refreshGit(ws);
  };
  const stage = (files: GitFile[]) => run(client.request("git.stage", { path: status.root, files: files.map((f) => f.path) }));
  const unstage = (files: GitFile[]) => run(client.request("git.unstage", { path: status.root, files: files.map((f) => f.path) }));
  const commit = async () => {
    if (busy || staged.length === 0 || message.trim() === "") return;
    setBusy(true);
    try {
      await client.request("git.commit", { path: status.root, message });
      setMessage("");
    } catch (error) {
      report(error);
    }
    setBusy(false);
    await refreshGit(ws);
  };

  const section = (label: string, files: GitFile[], isStaged: boolean) =>
    files.length > 0 && (
      <div class="section">
        <div class="section-head">
          <span>{label}</span>
          <span class="grow" />
          <button class="text-button" onClick={() => void (isStaged ? unstage(files) : stage(files))}>
            {isStaged ? "모두 빼기" : "모두 스테이징"}
          </button>
        </div>
        {files.map((file) => {
          // What is shown is this side of the file only.
          const letter = gitLetter(isStaged ? { ...file, worktree: "." } : file.index === "?" ? file : { ...file, index: "." });
          const delta = [file.added ? `+${file.added}` : "", file.removed ? `−${file.removed}` : ""].filter(Boolean).join(" ");
          return (
            <div key={file.path} class="change">
              <button class="change-main" title={file.path} onClick={() => openFile(ws, join(status.root, file.path, ws.host.os))}>
                <span class="row-name">{baseName(file.path)}</span>
                <span class="row-dir">{dirName(file.path)}</span>
                <span class="delta mono">{delta}</span>
                {letter && (
                  <span class={`git-letter ${letter.cls}`} title={letter.tip}>
                    {letter.letter}
                  </span>
                )}
              </button>
              <button
                class="change-action"
                aria-label={`${baseName(file.path)} ${isStaged ? "스테이징 취소" : "스테이징"}`}
                onClick={() => void (isStaged ? unstage([file]) : stage([file]))}
              >
                <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" aria-hidden="true">
                  <path d={isStaged ? "M2.5 6h7" : "M6 2.5v7M2.5 6h7"} />
                </svg>
              </button>
            </div>
          );
        })}
      </div>
    );

  const track = status.upstream
    ? [status.ahead ? `↑${status.ahead}` : "", status.behind ? `↓${status.behind}` : ""].filter(Boolean).join(" ") || "최신"
    : "";

  return (
    <div class="view">
      <div class="view-list scroll">
        <div class="branch-line">
          <BranchIcon />
          <span class="branch-name mono">{status.branch ?? status.head.slice(0, 7)}</span>
          <span class="branch-track">{track}</span>
        </div>
        {section("스테이징됨", staged, true)}
        {section("변경 사항", changed, false)}
        {status.files.length === 0 && <div class="empty-line">변경 사항이 없습니다.</div>}
      </div>
      <div class="commit">
        <div class="commit-box">
          <input
            type="text"
            aria-label="커밋 메시지"
            placeholder="feat(ui): 커밋 메시지"
            value={message}
            onInput={(e) => setMessage((e.target as HTMLInputElement).value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.isComposing) void commit();
            }}
          />
          <button class="commit-button" disabled={busy || staged.length === 0 || message.trim() === ""} onClick={() => void commit()}>
            커밋 {staged.length}
          </button>
        </div>
      </div>
    </div>
  );
}

const VIEWS: { id: RightView; label: string; icon: () => preact.JSX.Element }[] = [
  { id: "files", label: "탐색기", icon: ExplorerIcon },
  { id: "search", label: "검색", icon: FindIcon },
  { id: "git", label: "소스 제어", icon: SourceIcon },
];

export function RightPanel() {
  const ws = current.value;
  const view = rightView.value;
  const key = ws?.key ?? "";

  // Git is asked now and then while the window is in use, and whenever
  // something just happened that is likely to have changed files.
  useEffect(() => {
    if (!ws) return;
    const ask = () => {
      if (document.hasFocus()) void refreshGit(ws);
    };
    void refreshGit(ws);
    const timer = window.setInterval(ask, 4000);
    window.addEventListener("focus", ask);
    const stopQuiet = onQuiet((hostId, session) => {
      if (hostId === ws.hostId && session.worktree === ws.path) void refreshGit(ws);
    });
    const stopSaved = saved.subscribe(() => void refreshGit(ws));
    return () => {
      window.clearInterval(timer);
      window.removeEventListener("focus", ask);
      stopQuiet();
      stopSaved();
    };
  }, [key]);

  return (
    <aside class="panel right">
      <div class="right-top">
        {VIEWS.map((v) => (
          <button
            key={v.id}
            class={classes("icon-button", view === v.id && "on")}
            aria-label={v.label}
            aria-pressed={view === v.id}
            title={v.label}
            onClick={() => (rightView.value = v.id)}
          >
            <v.icon />
          </button>
        ))}
        <div class="grow" />
        <span class="right-label">{VIEWS.find((v) => v.id === view)?.label}</span>
        {ws && view !== "search" && (
          <button
            class="icon-button small"
            aria-label="새로 고침"
            title="새로 고침"
            onClick={() => {
              refreshTree(ws);
              void refreshGit(ws);
            }}
          >
            <RefreshIcon />
          </button>
        )}
      </div>
      {!ws && <div class="view"><div class="empty-line">작업공간을 고르면 파일이 보입니다.</div></div>}
      {ws && view === "files" && <Explorer key={key} ws={ws} />}
      {ws && view === "search" && <Search key={key} ws={ws} />}
      {ws && view === "git" && <SourceControl key={key} ws={ws} />}
    </aside>
  );
}
