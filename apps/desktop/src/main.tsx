import "@fontsource-variable/geist/wght.css";
import "@fontsource-variable/geist-mono/wght.css";
import "@fontsource/ibm-plex-sans-kr/korean-400.css";
import "@fontsource/ibm-plex-sans-kr/korean-500.css";
import "./styles/tokens.css";
import "./styles/app.css";

import { effect } from "@preact/signals";
import { render } from "preact";

import { DialogLayer } from "./components/Dialogs.tsx";
import { MenuLayer } from "./components/Menu.tsx";
import { RightPanel } from "./components/RightPanel.tsx";
import { Sidebar } from "./components/Sidebar.tsx";
import { Workbench } from "./components/Workbench.tsx";
import { notice, prune, rightOpen, sessions, start } from "./lib/store.ts";

function App() {
  return (
    <div class="app">
      <Sidebar />
      <Workbench />
      {rightOpen.value && <RightPanel />}
      <MenuLayer />
      <DialogLayer />
      {notice.value && (
        <div class="notice" role="status">
          {notice.value}
        </div>
      )}
    </div>
  );
}

// The browser's own menu has nothing to offer here, except where text is
// being edited.
window.addEventListener("contextmenu", (event) => {
  const target = event.target as HTMLElement;
  if (!target.closest("input, textarea, .cm-content, .xterm, .md-body")) event.preventDefault();
});

effect(() => {
  // Read, so this runs again whenever the sessions change.
  void sessions.value;
  prune();
});

// The terminal measures its font once, so the fonts are loaded first.
const fonts = Promise.all([
  document.fonts.load("12.5px 'Geist Mono Variable'"),
  document.fonts.load("13px 'Geist Variable'"),
]).catch(() => undefined);

void Promise.race([fonts, new Promise((resolve) => setTimeout(resolve, 1500))]).then(() => {
  render(<App />, document.getElementById("app")!);
  void start();
});
