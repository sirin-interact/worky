// A session's terminal. It exists only while its tab is in front: the daemon
// keeps the output, so a terminal that is not on screen costs nothing here.

import { FitAddon } from "@xterm/addon-fit";
import { Unicode11Addon } from "@xterm/addon-unicode11";
import { WebglAddon } from "@xterm/addon-webgl";
import { Terminal, type ITheme } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import type { Frame, Session } from "@worky/protocol";
import { useEffect, useRef } from "preact/hooks";

import { RequestError } from "../lib/daemon.ts";
import { clientOf, markSeen, theme, type Host } from "../lib/store.ts";
import { debounce } from "../lib/util.ts";

const EMPTY = new Uint8Array(0);
/** How much history to ask for when a terminal is opened. */
const BACKLOG = 2 * 1024 * 1024;

const encoder = new TextEncoder();

function css(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

function palette(): ITheme {
  const light = theme.value === "light";
  const ansi = light
    ? ["#1b1b1f", "#c4314b", "#1a7f4b", "#9a5200", "#1a66dd", "#7b3fd1", "#0b7285", "#55555e",
       "#62626b", "#d23a55", "#1f9358", "#b06000", "#2f7bf0", "#8f52e6", "#0e8ba0", "#0e0e11"]
    : ["#3a3d43", "#ff7a7f", "#3dd68c", "#ffb340", "#6fb2ff", "#b8a1ff", "#5fd4e0", "#d5d8dd",
       "#878d99", "#ff9a9e", "#6ee6a8", "#ffc870", "#93c5ff", "#cdbcff", "#8be3ec", "#ffffff"];
  const names = ["black", "red", "green", "yellow", "blue", "magenta", "cyan", "white"] as const;
  const colors: Record<string, string> = {};
  names.forEach((name, i) => {
    colors[name] = ansi[i]!;
    colors["bright" + name[0]!.toUpperCase() + name.slice(1)] = ansi[i + 8]!;
  });
  return {
    ...colors,
    background: css("--term"),
    foreground: css("--text"),
    cursor: css("--text"),
    cursorAccent: css("--term"),
    selectionBackground: css("--accent") + "59",
    scrollbarSliderBackground: css("--chip"),
    scrollbarSliderHoverBackground: css("--chip"),
    scrollbarSliderActiveBackground: css("--chip"),
  };
}

interface Props {
  host: Host;
  session: Session;
}

export function TerminalView({ host, session }: Props) {
  const frame = useRef<HTMLDivElement>(null);
  const mount = useRef<HTMLDivElement>(null);
  // The newest props, for callbacks that outlive a render.
  const latest = useRef({ host, session });
  latest.current = { host, session };
  const control = useRef<{ layout: () => void; attach: () => void } | null>(null);

  useEffect(() => {
    const term = new Terminal({
      allowProposedApi: true,
      cols: session.cols,
      rows: session.rows,
      fontFamily: "'Geist Mono Variable', 'Geist Mono', ui-monospace, Menlo, Consolas, 'IBM Plex Sans KR', monospace",
      fontSize: 12.5,
      lineHeight: 1.25,
      scrollback: 10000,
      cursorBlink: true,
      theme: palette(),
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.loadAddon(new Unicode11Addon());
    term.unicode.activeVersion = "11";
    term.open(mount.current!);
    try {
      const webgl = new WebglAddon();
      webgl.onContextLoss(() => webgl.dispose());
      term.loadAddon(webgl);
    } catch {
      // The DOM renderer is slower and works everywhere.
    }

    const client = clientOf(host.config.id);
    let channel = -1;
    let disposed = false;
    // Offsets into the session's output: just past what the terminal has
    // parsed, and the ends of the pieces it has been given but not parsed.
    let processed = -1;
    const inflight: number[] = [];

    const isOwner = () => latest.current.session.sizeOwner === latest.current.host.clientId;

    /** Shrinks the terminal to fit when its size is somebody else's. */
    const layout = () => {
      const box = frame.current;
      const screen = mount.current?.querySelector<HTMLElement>(".xterm-screen");
      if (!box || !screen || !mount.current) return;
      const scale = Math.min(1, box.clientWidth / screen.offsetWidth, box.clientHeight / screen.offsetHeight);
      mount.current.style.transform = scale < 1 ? `scale(${scale})` : "";
      mount.current.style.width = scale < 1 ? `${screen.offsetWidth}px` : "";
    };

    const apply = (cols: number, rows: number) => {
      if (term.cols !== cols || term.rows !== rows) term.resize(cols, rows);
      layout();
    };

    const onFrame = (f: Frame) => {
      if (disposed) return;
      switch (f.type) {
        case "reset":
          // The terminal parses what it is given a little later. A reset or a
          // resize waits its turn behind the output that came before it.
          inflight.length = 0;
          term.write(EMPTY, () => {
            term.reset();
            apply(f.cols, f.rows);
            processed = f.offset;
          });
          break;
        case "state":
          term.write(f.payload);
          break;
        case "output":
          inflight.push(f.offset + f.payload.length);
          term.write(f.payload, () => {
            const end = inflight.shift();
            if (end !== undefined) processed = end;
          });
          break;
        case "resize":
          term.write(EMPTY, () => apply(f.cols, f.rows));
          break;
      }
    };

    let attachedTo = "";
    const attach = () => {
      const connection = client.hello?.clientId ?? "";
      if (disposed || client.state !== "open" || attachedTo === connection) return;
      attachedTo = connection;
      channel = -1; // a channel of an earlier connection means nothing now
      const sessionId = latest.current.session.id;
      // After a lost connection the stream goes on from where it stopped.
      const params = processed >= 0 ? { sessionId, fromOffset: processed } : { sessionId, backlog: BACKLOG };
      inflight.length = 0;
      client.attach(params, onFrame).then(
        (result) => {
          if (disposed) {
            client.detach(result.channel);
            return;
          }
          channel = result.channel;
          // Nobody is using the terminal at another size, so it may as
          // well fit this window.
          if (latest.current.session.sizeOwner === null) takeSize();
        },
        (error: RequestError) => {
          attachedTo = "";
          if (error.code !== "disconnected") term.write(`\r\n\x1b[31m${error.message}\x1b[0m\r\n`);
        },
      );
    };

    /** Makes the session the size of this view, on behalf of the user. */
    const takeSize = () => {
      if (channel < 0 || latest.current.session.status !== "running") return;
      // What fits is measured without the scaling a borrowed size needs.
      mount.current!.style.transform = "";
      mount.current!.style.width = "";
      const want = fit.proposeDimensions();
      layout();
      if (!want || !Number.isFinite(want.cols) || want.cols < 2 || want.rows < 1) return;
      const s = latest.current.session;
      if (isOwner() && s.cols === want.cols && s.rows === want.rows) return;
      client.request("terminal.resize", { channel, cols: want.cols, rows: want.rows }).catch(() => {});
    };

    // The terminal reports typed input and its own answers to queries
    // through the same event. A query is seen by the handlers below just
    // before the terminal answers it, within one synchronous parse, so
    // anything reported until that parse has finished is an answer.
    let answering = false;
    const query = () => {
      if (!answering) {
        answering = true;
        queueMicrotask(() => (answering = false));
      }
      return false; // let the terminal handle it as usual
    };
    const parser = term.parser;
    const hooks = [
      parser.registerCsiHandler({ final: "c" }, query),
      parser.registerCsiHandler({ prefix: ">", final: "c" }, query),
      parser.registerCsiHandler({ prefix: "=", final: "c" }, query),
      parser.registerCsiHandler({ final: "n" }, query),
      parser.registerCsiHandler({ prefix: "?", final: "n" }, query),
      parser.registerCsiHandler({ intermediates: "$", final: "p" }, query),
      parser.registerCsiHandler({ prefix: "?", intermediates: "$", final: "p" }, query),
      parser.registerCsiHandler({ final: "t" }, query),
      parser.registerCsiHandler({ prefix: "?", final: "u" }, query),
      parser.registerCsiHandler({ prefix: ">", final: "q" }, query),
      parser.registerDcsHandler({ intermediates: "$", final: "q" }, query),
      parser.registerDcsHandler({ intermediates: "+", final: "q" }, query),
      ...[4, 10, 11, 12].map((code) => parser.registerOscHandler(code, query)),
    ];

    const send = (bytes: Uint8Array) => {
      if (channel < 0) return;
      if (answering) {
        client.reply(channel, inflight[0] ?? Math.max(processed, 0), bytes);
        return;
      }
      if (!isOwner()) takeSize();
      client.input(channel, bytes);
    };
    term.onData((data) => send(encoder.encode(data)));
    term.onBinary((data) => send(Uint8Array.from(data, (ch) => ch.charCodeAt(0))));

    term.attachCustomKeyEventHandler((event) => {
      if (event.type !== "keydown") return true;
      const key = event.key.toLowerCase();
      const mod = event.ctrlKey && !event.altKey && !event.metaKey;
      // Copy when there is a selection: Ctrl+C with nothing selected still
      // interrupts the program.
      if (mod && key === "c" && (event.shiftKey || term.hasSelection())) {
        void navigator.clipboard.writeText(term.getSelection());
        term.clearSelection();
        return false;
      }
      // Leave paste to the browser, which hands the text to the terminal.
      if (mod && key === "v") return false;
      return true;
    });

    const onResize = debounce(() => {
      if (disposed) return;
      if (isOwner()) takeSize();
      else layout();
    }, 60);
    const observer = new ResizeObserver(onResize);
    observer.observe(frame.current!);

    const onFocus = () => markSeen(latest.current.session.id);
    window.addEventListener("focus", onFocus);
    const stopTheme = theme.subscribe(() => {
      // The colours come from the style sheet, which follows the theme.
      requestAnimationFrame(() => {
        if (!disposed) term.options.theme = palette();
      });
    });

    control.current = { layout, attach };
    attach();
    term.focus();
    markSeen(session.id);

    return () => {
      disposed = true;
      control.current = null;
      onResize.cancel();
      observer.disconnect();
      window.removeEventListener("focus", onFocus);
      stopTheme();
      hooks.forEach((h) => h.dispose());
      if (channel >= 0) client.detach(channel);
      term.dispose();
    };
  }, [host.config.id, session.id]);

  // A new connection has no channels: attach again and carry on.
  useEffect(() => {
    if (host.state === "open") control.current?.attach();
  }, [host.clientId]);

  // The user is looking at this session, so what it does is seen at once.
  useEffect(() => {
    if (document.hasFocus()) markSeen(session.id);
  }, [session.activityAt]);

  const borrowed = session.sizeOwner !== null && session.sizeOwner !== host.clientId;
  return (
    <div class="terminal">
      <div class="terminal-frame" ref={frame}>
        <div class="terminal-mount" ref={mount} />
      </div>
      {session.status === "exited" && (
        <div class="terminal-note">
          프로세스가 끝났습니다{session.exitCode === null ? "" : ` · 종료 코드 ${session.exitCode}`}
        </div>
      )}
      {session.status === "running" && borrowed && (
        <div class="terminal-note">다른 창의 크기에 맞춰 표시 중 · 입력하면 이 창 크기로 바뀝니다</div>
      )}
    </div>
  );
}
