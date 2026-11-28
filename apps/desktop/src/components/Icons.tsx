// The app's icons, drawn as strokes in the current text colour.

import type { ComponentChildren } from "preact";

import type { AgentKind, Status } from "../lib/agents.ts";
import { STATUS_LABEL } from "../lib/agents.ts";

interface IconProps {
  size?: number;
  box?: number;
  width?: number;
  children: ComponentChildren;
  class?: string;
}

function Stroke({ size = 14, box = 14, width = 1.4, children, class: cls }: IconProps) {
  return (
    <svg
      class={cls}
      width={size}
      height={size}
      viewBox={`0 0 ${box} ${box}`}
      fill="none"
      stroke="currentColor"
      stroke-width={width}
      stroke-linecap="round"
      stroke-linejoin="round"
      aria-hidden="true"
    >
      {children}
    </svg>
  );
}

export const SearchIcon = () => (
  <Stroke size={13}>
    <circle cx="6" cy="6" r="3.8" />
    <path d="m9 9 3 3" />
  </Stroke>
);

export const PlusIcon = () => (
  <Stroke>
    <path d="M7 2.5v9M2.5 7h9" />
  </Stroke>
);

export const CloseIcon = ({ size = 12 }: { size?: number }) => (
  <Stroke size={size} box={12} width={1.3}>
    <path d="M3 3l6 6M9 3l-6 6" />
  </Stroke>
);

export const Chevron = ({ open }: { open: boolean }) => (
  <Stroke size={12} box={12} width={1.5} class={open ? "chevron open" : "chevron"}>
    <path d="M4.5 2.5 8 6l-3.5 3.5" />
  </Stroke>
);

export const FileIcon = () => (
  <Stroke width={1.2}>
    <path d="M3.5 1.8h4.6l2.4 2.4v8H3.5z" />
    <path d="M8 1.8v2.6h2.5" />
  </Stroke>
);

export const PanelIcon = () => (
  <Stroke size={16} box={16} width={1.3}>
    <rect x="2" y="3" width="12" height="10" rx="1.5" />
    <path d="M10.5 3v10" />
  </Stroke>
);

export const FoldIcon = () => (
  <Stroke size={16} box={16} width={1.3}>
    <path d="M5 3.5 8 6l3-2.5M5 12.5 8 10l3 2.5M3 8h10" />
  </Stroke>
);

export const ServerIcon = () => (
  <Stroke size={12}>
    <rect x="2" y="2" width="10" height="4" rx="1.2" />
    <rect x="2" y="8" width="10" height="4" rx="1.2" />
  </Stroke>
);

export const LaptopIcon = () => (
  <Stroke size={12}>
    <path d="M3 3h8v6H3z" />
    <path d="M1.5 11.5h11" />
  </Stroke>
);

export const BranchIcon = () => (
  <Stroke size={12} box={12} width={1.2}>
    <circle cx="3" cy="2.5" r="1.3" />
    <circle cx="3" cy="9.5" r="1.3" />
    <circle cx="9" cy="4" r="1.3" />
    <path d="M3 3.8v4.4M9 5.3c0 2-6 1.2-6 2.9" />
  </Stroke>
);

export const FilterIcon = () => (
  <Stroke size={12}>
    <path d="M2 3.5h10M4 7h6M6 10.5h2" />
  </Stroke>
);

export const ThemeIcon = () => (
  <svg width="13" height="13" viewBox="0 0 14 14" fill="none" aria-hidden="true">
    <circle cx="7" cy="7" r="5.2" stroke="currentColor" stroke-width="1.3" />
    <path d="M7 1.8a5.2 5.2 0 0 1 0 10.4z" fill="currentColor" />
  </svg>
);

export const CheckIcon = () => (
  <Stroke size={12} box={12} width={1.6}>
    <path d="m2.5 6.3 2.3 2.3 4.7-5" />
  </Stroke>
);

export const MoreIcon = () => (
  <svg width="14" height="14" viewBox="0 0 14 14" fill="currentColor" aria-hidden="true">
    <circle cx="3" cy="7" r="1.1" />
    <circle cx="7" cy="7" r="1.1" />
    <circle cx="11" cy="7" r="1.1" />
  </svg>
);

export const RefreshIcon = () => (
  <Stroke size={13}>
    <path d="M11.5 7a4.5 4.5 0 1 1-1.4-3.3M11.6 2.2v2.4H9.2" />
  </Stroke>
);

export const UpIcon = () => (
  <Stroke size={13}>
    <path d="M7 11.5v-9M3.5 6 7 2.5 10.5 6" />
  </Stroke>
);

export const ExplorerIcon = () => (
  <Stroke size={16} box={16} width={1.3}>
    <path d="M2 4.6c0-.6.4-1.1 1-1.1h3l1.5 1.6H13c.6 0 1 .5 1 1.1v5.7c0 .6-.4 1.1-1 1.1H3c-.6 0-1-.5-1-1.1z" />
  </Stroke>
);

export const FindIcon = () => (
  <Stroke size={16} box={16} width={1.3}>
    <path d="M7 11.5a4.5 4.5 0 1 0 0-9 4.5 4.5 0 0 0 0 9zM10.3 10.3 13.5 13.5" />
  </Stroke>
);

export const SourceIcon = () => (
  <Stroke size={16} box={16} width={1.3}>
    <path d="M4.5 5.2v5.6M11.5 7.2c0 2.6-7 1.4-7 3.6M4.5 5.2a1.6 1.6 0 1 0 0-3.2 1.6 1.6 0 0 0 0 3.2zM4.5 14a1.6 1.6 0 1 0 0-3.2 1.6 1.6 0 0 0 0 3.2zM11.5 7.2a1.6 1.6 0 1 0 0-3.2 1.6 1.6 0 0 0 0 3.2z" />
  </Stroke>
);

/** The mark of an agent, or of a plain terminal. */
export function AgentMark({ kind, size = 13 }: { kind: AgentKind; size?: number }) {
  return (
    <svg
      class={kind.tinted ? "agent-mark tinted" : "agent-mark"}
      width={size}
      height={size}
      viewBox="0 0 14 14"
      fill="none"
      stroke="currentColor"
      stroke-width="1.6"
      stroke-linecap="round"
      stroke-linejoin="round"
      role="img"
      aria-label={kind.name}
    >
      <path d={kind.icon} />
    </svg>
  );
}

/** The glyph for what a workspace or a session is doing. */
export function StatusGlyph({ status, size = 14 }: { status: Status; size?: number }) {
  const label = STATUS_LABEL[status];
  if (status === "wait") {
    return (
      <svg class="glyph wait" width={size} height={size} viewBox="0 0 14 14" role="img" aria-label={label}>
        <circle cx="7" cy="7" r="6" fill="var(--wait)" />
        <path d="M7 3.8v3.9" stroke="var(--on-wait)" stroke-width="1.6" stroke-linecap="round" />
        <circle cx="7" cy="10" r=".9" fill="var(--on-wait)" />
      </svg>
    );
  }
  if (status === "run") {
    return (
      <svg class="glyph run" width={size} height={size} viewBox="0 0 14 14" fill="none" role="img" aria-label={label}>
        <circle cx="7" cy="7" r="5" stroke="var(--accent)" stroke-opacity=".3" stroke-width="1.8" />
        <path class="spin" d="M7 2a5 5 0 0 1 5 5" stroke="var(--accent)" stroke-width="1.8" stroke-linecap="round" />
      </svg>
    );
  }
  if (status === "done") {
    return (
      <svg
        class="glyph"
        width={size}
        height={size}
        viewBox="0 0 14 14"
        fill="none"
        stroke="var(--done)"
        stroke-width="1.4"
        stroke-linecap="round"
        stroke-linejoin="round"
        role="img"
        aria-label={label}
      >
        <circle cx="7" cy="7" r="5.2" />
        <path d="m4.6 7.2 1.7 1.7 3.1-3.5" />
      </svg>
    );
  }
  return (
    <svg class="glyph" width={size} height={size} viewBox="0 0 14 14" role="img" aria-label={label}>
      <circle cx="7" cy="7" r="2.2" fill="var(--faint)" />
    </svg>
  );
}
