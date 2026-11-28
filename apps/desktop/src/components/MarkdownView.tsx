// The Markdown viewer: a file's text shown as a document.

import { LanguageDescription } from "@codemirror/language";
import { languages } from "@codemirror/language-data";
import { classHighlighter, highlightTree } from "@lezer/highlight";
import { signal } from "@preact/signals";
import DOMPurify from "dompurify";
import { marked } from "marked";
import { useEffect, useMemo, useRef } from "preact/hooks";

import { dirName, join } from "../lib/paths.ts";
import { clientOf, openFile, type Workspace } from "../lib/store.ts";
import { fromBase64 } from "../lib/util.ts";

export interface Heading {
  id: string;
  text: string;
  level: number;
}

/** The headings of the document on screen, for the outline. */
export const outline = signal<Heading[]>([]);
/** Whether the outline is shown beside the document. */
export const outlineOpen = signal(false);

const IMAGE_TYPES: Record<string, string> = {
  png: "image/png",
  jpg: "image/jpeg",
  jpeg: "image/jpeg",
  gif: "image/gif",
  webp: "image/webp",
  svg: "image/svg+xml",
  avif: "image/avif",
};

/** A file in a repository is not trusted: its HTML is cleaned before use. */
function render(text: string): string {
  const html = marked.parse(text, { gfm: true, async: false }) as string;
  return DOMPurify.sanitize(html, { FORBID_TAGS: ["style", "form"], FORBID_ATTR: ["style"] });
}

function escapeHtml(text: string): string {
  return text.replace(/[&<>]/g, (ch) => (ch === "&" ? "&amp;" : ch === "<" ? "&lt;" : "&gt;"));
}

async function highlight(block: HTMLElement, name: string): Promise<void> {
  const description = LanguageDescription.matchLanguageName(languages, name, true);
  if (!description) return;
  const support = await description.load();
  const code = block.textContent ?? "";
  let html = "";
  let at = 0;
  highlightTree(support.language.parser.parse(code), classHighlighter, (from, to, cls) => {
    html += escapeHtml(code.slice(at, from)) + `<span class="${cls}">${escapeHtml(code.slice(from, to))}</span>`;
    at = to;
  });
  if (block.isConnected) block.innerHTML = html + escapeHtml(code.slice(at));
}

interface Props {
  workspace: Workspace;
  /** The file's absolute path on the host. */
  path: string;
  text: string;
}

export function MarkdownView({ workspace, path, text }: Props) {
  const body = useRef<HTMLDivElement>(null);
  const html = useMemo(() => render(text), [text]);
  const os = workspace.host.os;

  useEffect(() => {
    const root = body.current;
    if (!root) return;
    const urls: string[] = [];
    let live = true;

    const used = new Map<string, number>();
    const headings: Heading[] = [];
    for (const h of root.querySelectorAll<HTMLElement>("h1, h2, h3, h4")) {
      const label = h.textContent?.trim() ?? "";
      let id = label.toLowerCase().replace(/[^\p{L}\p{N}]+/gu, "-").replace(/^-|-$/g, "") || "section";
      const count = used.get(id) ?? 0;
      used.set(id, count + 1);
      if (count > 0) id += "-" + count;
      h.id = "md-" + id;
      h.dataset.slug = id;
      const level = Number(h.tagName[1]);
      if (level <= 3) headings.push({ id: h.id, text: label, level });
    }
    outline.value = headings;

    for (const code of root.querySelectorAll<HTMLElement>("pre > code[class*='language-']")) {
      const name = /language-([\w+#-]+)/.exec(code.className)?.[1];
      if (name) void highlight(code, name).catch(() => {});
    }

    // Pictures kept next to the file come from the daemon, like the file.
    for (const img of root.querySelectorAll<HTMLImageElement>("img[src]")) {
      const src = img.getAttribute("src") ?? "";
      if (/^([a-z][a-z0-9+.-]*:|\/\/|#)/i.test(src)) continue;
      const type = IMAGE_TYPES[src.split(/[?#]/)[0]!.split(".").pop()?.toLowerCase() ?? ""];
      if (!type) continue;
      img.removeAttribute("src");
      const target = join(dirName(path), decodeURIComponent(src.split(/[?#]/)[0]!), os);
      void clientOf(workspace.hostId)
        .request("fs.read", { path: target })
        .then((file) => {
          const bytes = fromBase64(file.data);
          // A picture that does not fit in one piece is left out.
          if (!live || bytes.length < file.size) return;
          const url = URL.createObjectURL(new Blob([bytes.buffer as ArrayBuffer], { type }));
          urls.push(url);
          img.src = url;
        })
        .catch(() => {});
    }

    return () => {
      live = false;
      urls.forEach((url) => URL.revokeObjectURL(url));
    };
  }, [html]);

  useEffect(() => () => void (outline.value = []), []);

  // A link must never take the app's own window somewhere else.
  const onClick = (event: MouseEvent) => {
    const link = (event.target as HTMLElement).closest("a");
    if (!link) return;
    event.preventDefault();
    const href = link.getAttribute("href") ?? "";
    if (href.startsWith("#")) {
      const slug = decodeURIComponent(href.slice(1)).toLowerCase();
      const target = [...(body.current?.querySelectorAll<HTMLElement>("[data-slug]") ?? [])].find(
        (h) => h.dataset.slug === slug,
      );
      target?.scrollIntoView({ behavior: "smooth", block: "start" });
    } else if (/^(https?:|mailto:)/i.test(href)) {
      void openExternal(href);
    } else if (!/^[a-z][a-z0-9+.-]*:/i.test(href) && href) {
      openFile(workspace, join(dirName(path), decodeURIComponent(href.split(/[?#]/)[0]!), os));
    }
  };

  const headings = outline.value;
  return (
    <div class="md">
      <div class="md-scroll">
        <div class="md-body" ref={body} onClick={onClick} dangerouslySetInnerHTML={{ __html: html }} />
      </div>
      {outlineOpen.value && headings.length > 0 && (
        <nav class="md-outline" aria-label="목차">
          <span class="md-outline-title">목차</span>
          {headings.map((h) => (
            <button
              key={h.id}
              class="md-outline-item"
              style={{ paddingLeft: `${10 + (h.level - 1) * 10}px` }}
              onClick={() => document.getElementById(h.id)?.scrollIntoView({ behavior: "smooth", block: "start" })}
            >
              {h.text}
            </button>
          ))}
        </nav>
      )}
    </div>
  );
}

interface Shell {
  invoke: (command: string, args?: Record<string, unknown>) => Promise<unknown>;
}

/** Opens a link in the user's browser. Outside the app shell it does nothing. */
async function openExternal(url: string): Promise<void> {
  const shell = (window as unknown as { __TAURI_INTERNALS__?: Shell }).__TAURI_INTERNALS__;
  if (shell) await shell.invoke("plugin:opener|open_url", { url }).catch(() => {});
  else window.open(url, "_blank", "noopener");
}

/** Lines, words and characters, for the status bar. */
export function stats(text: string): string {
  const lines = text === "" ? 0 : text.split("\n").length;
  const words = text.split(/\s+/).filter(Boolean).length;
  const chars = text.replace(/\s/g, "").length;
  return `${lines}줄 · 단어 ${words} · 글자 ${chars}`;
}
