// The code editor. One editor view shows whichever file's tab is in front;
// each file keeps its own state, with its undo history, while it is away.

import { closeBrackets, closeBracketsKeymap } from "@codemirror/autocomplete";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import {
  bracketMatching,
  indentOnInput,
  indentUnit,
  LanguageDescription,
  syntaxHighlighting,
} from "@codemirror/language";
import { languages } from "@codemirror/language-data";
import { highlightSelectionMatches, search, searchKeymap } from "@codemirror/search";
import { Compartment, EditorState, type Extension } from "@codemirror/state";
import {
  drawSelection,
  EditorView,
  highlightActiveLine,
  highlightActiveLineGutter,
  keymap,
  lineNumbers,
} from "@codemirror/view";
import { classHighlighter } from "@lezer/highlight";
import { signal } from "@preact/signals";
import { useEffect, useRef } from "preact/hooks";

import { docs, keepState, keptState, revealed, saveDoc, savedText, takeReveal } from "../lib/docs.ts";
import { baseName } from "../lib/paths.ts";

/** Where the cursor is and what the file is, for the status bar. */
export const cursorInfo = signal({ line: 1, column: 1, language: "" });

const language = new Compartment();

const look = EditorView.theme({
  "&": { height: "100%", color: "var(--text2)", backgroundColor: "transparent", fontSize: "12.5px" },
  "&.cm-focused": { outline: "none" },
  ".cm-scroller": {
    fontFamily: "var(--mono)",
    lineHeight: "20px",
    scrollbarWidth: "thin",
    scrollbarColor: "var(--chip) transparent",
  },
  ".cm-content": { padding: "4px 0 24px", caretColor: "var(--text)" },
  ".cm-line": { padding: "0 16px 0 0" },
  ".cm-gutters": { backgroundColor: "transparent", border: "none", color: "var(--dim)" },
  ".cm-lineNumbers .cm-gutterElement": { minWidth: "44px", padding: "0 16px 0 0" },
  ".cm-activeLine": { backgroundColor: "var(--soft)" },
  ".cm-activeLineGutter": { backgroundColor: "var(--soft)", color: "var(--text)" },
  ".cm-cursor, .cm-dropCursor": { borderLeftColor: "var(--text)" },
  "&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground": {
    backgroundColor: "var(--sel)",
  },
  ".cm-selectionMatch": { backgroundColor: "var(--raised2)" },
  ".cm-searchMatch": { backgroundColor: "var(--wait-bg-on)", borderRadius: "3px" },
  ".cm-searchMatch.cm-searchMatch-selected": { backgroundColor: "var(--hit)" },
  ".cm-matchingBracket, &.cm-focused .cm-matchingBracket": {
    backgroundColor: "var(--raised2)",
    outline: "none",
  },
  ".cm-panels": {
    backgroundColor: "var(--panel)",
    color: "var(--text)",
    borderTop: "1px solid var(--border)",
    fontFamily: "var(--sans)",
  },
  ".cm-panels.cm-panels-top": { borderBottom: "1px solid var(--border)", borderTop: "none" },
  ".cm-textfield": {
    backgroundColor: "var(--ground)",
    color: "var(--text)",
    border: "1px solid var(--border)",
    borderRadius: "7px",
    padding: "3px 8px",
    fontSize: "12.5px",
  },
  ".cm-button": {
    backgroundImage: "none",
    backgroundColor: "var(--raised)",
    color: "var(--text)",
    border: "0",
    borderRadius: "7px",
    padding: "3px 10px",
    fontSize: "12px",
  },
  ".cm-panel.cm-search label": { fontSize: "12px", color: "var(--muted)" },
  ".cm-panel.cm-search [name=close]": { color: "var(--muted)", fontSize: "16px" },
});

function baseExtensions(key: string): Extension {
  return [
    lineNumbers(),
    highlightActiveLine(),
    highlightActiveLineGutter(),
    history(),
    drawSelection(),
    indentOnInput(),
    bracketMatching(),
    closeBrackets(),
    indentUnit.of("  "),
    search({ top: true }),
    highlightSelectionMatches(),
    syntaxHighlighting(classHighlighter),
    language.of([]),
    look,
    keymap.of([
      {
        key: "Mod-s",
        run: () => {
          void saveDoc(key);
          return true;
        },
      },
      ...closeBracketsKeymap,
      ...defaultKeymap,
      ...historyKeymap,
      ...searchKeymap,
      indentWithTab,
    ]),
  ];
}

function report(state: EditorState, name: string): void {
  const head = state.selection.main.head;
  const line = state.doc.lineAt(head);
  cursorInfo.value = { line: line.number, column: head - line.from + 1, language: name };
}

interface Props {
  docKey: string;
  /** Wraps long lines, for prose such as Markdown. */
  wrap?: boolean;
}

export function CodeEditor({ docKey, wrap = false }: Props) {
  const parent = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const shown = useRef("");
  const names = useRef(new Map<string, string>());
  const doc = docs.value[docKey];
  const version = doc?.revision ?? 0;
  const pending = revealed.value;

  useEffect(() => {
    const editor = new EditorView({
      parent: parent.current!,
      dispatch: (tr) => {
        editor.update([tr]);
        if (shown.current && (tr.docChanged || tr.selection)) {
          if (tr.docChanged) keepState(shown.current, editor.state);
          report(editor.state, names.current.get(shown.current) ?? "");
        }
      },
    });
    view.current = editor;
    return () => {
      if (shown.current) keepState(shown.current, editor.state);
      editor.destroy();
      view.current = null;
      shown.current = "";
    };
  }, []);

  // Swap in the state of the file whose tab came to the front.
  useEffect(() => {
    const editor = view.current;
    if (!editor || !doc || doc.status !== "ready" || doc.unreadable) return;
    if (shown.current === docKey) {
      // The same file was read from disk again: follow it.
      const text = savedText(docKey);
      if (!doc.dirty && editor.state.sliceDoc() !== text) {
        editor.dispatch({ changes: { from: 0, to: editor.state.doc.length, insert: text } });
        keepState(docKey, editor.state);
      }
      return;
    }
    if (shown.current) keepState(shown.current, editor.state);
    shown.current = docKey;

    const kept = keptState(docKey);
    editor.setState(
      kept ??
        EditorState.create({
          doc: savedText(docKey),
          extensions: [baseExtensions(docKey), wrap ? EditorView.lineWrapping : []],
        }),
    );
    report(editor.state, names.current.get(docKey) ?? "");
    editor.focus();

    if (!kept) {
      const description = LanguageDescription.matchFilename(languages, baseName(doc.path));
      if (description) {
        names.current.set(docKey, description.name);
        void description.load().then((support) => {
          if (view.current === editor && shown.current === docKey) {
            editor.dispatch({ effects: language.reconfigure(support) });
            keepState(docKey, editor.state);
            report(editor.state, description.name);
          }
        });
      }
    }
  }, [docKey, doc?.status, version]);

  // Jump to a line asked for by a search result.
  useEffect(() => {
    const editor = view.current;
    if (!editor || shown.current !== docKey) return;
    const line = takeReveal(docKey);
    if (line === undefined) return;
    const at = editor.state.doc.line(Math.min(Math.max(line, 1), editor.state.doc.lines));
    editor.dispatch({
      selection: { anchor: at.from },
      effects: EditorView.scrollIntoView(at.from, { y: "center" }),
    });
    editor.focus();
  }, [docKey, doc?.status, version, pending]);

  return <div class="editor" ref={parent} />;
}
