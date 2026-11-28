// A small pop-up menu, shown next to whatever opened it.

import { signal } from "@preact/signals";
import type { ComponentChildren } from "preact";
import { useEffect, useLayoutEffect, useRef } from "preact/hooks";

import { CheckIcon } from "./Icons.tsx";

export interface MenuItem {
  label: string;
  icon?: ComponentChildren;
  hint?: string;
  danger?: boolean;
  checked?: boolean;
  disabled?: boolean;
  onPick: () => void;
}

interface OpenMenu {
  /** The corner the menu grows from. */
  x: number;
  y: number;
  /** Grow upwards and to the left instead, to stay inside the window. */
  up: boolean;
  left: boolean;
  items: (MenuItem | null)[];
}

const open = signal<OpenMenu | null>(null);

/** Opens a menu under an element, or at the pointer for a context menu. */
export function openMenu(at: Element | MouseEvent, items: (MenuItem | null)[]): void {
  if (at instanceof MouseEvent) {
    at.preventDefault();
    open.value = {
      x: at.clientX,
      y: at.clientY,
      up: at.clientY > window.innerHeight * 0.6,
      left: at.clientX > window.innerWidth - 240,
      items,
    };
    return;
  }
  const box = at.getBoundingClientRect();
  const up = box.top > window.innerHeight * 0.6;
  const left = box.left > window.innerWidth - 240;
  open.value = {
    x: left ? box.right : box.left,
    y: up ? box.top - 6 : box.bottom + 6,
    up,
    left,
    items,
  };
}

export function closeMenu(): void {
  open.value = null;
}

export function MenuLayer() {
  const menu = open.value;
  const box = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!menu) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") closeMenu();
    };
    const onDown = (event: MouseEvent) => {
      if (!box.current?.contains(event.target as Node)) closeMenu();
    };
    window.addEventListener("keydown", onKey);
    window.addEventListener("mousedown", onDown, true);
    window.addEventListener("blur", closeMenu);
    return () => {
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("mousedown", onDown, true);
      window.removeEventListener("blur", closeMenu);
    };
  }, [menu]);

  useLayoutEffect(() => {
    box.current?.querySelector<HTMLElement>("button:not(:disabled)")?.focus();
  }, [menu]);

  if (!menu) return null;
  const style = {
    [menu.left ? "right" : "left"]: `${menu.left ? window.innerWidth - menu.x : menu.x}px`,
    [menu.up ? "bottom" : "top"]: `${menu.up ? window.innerHeight - menu.y : menu.y}px`,
  };
  return (
    <div class="menu" role="menu" ref={box} style={style}>
      {menu.items.map((item, i) =>
        item === null ? (
          <div key={i} class="menu-line" />
        ) : (
          <button
            key={i}
            role="menuitem"
            class={item.danger ? "menu-item danger" : "menu-item"}
            disabled={item.disabled}
            onClick={() => {
              closeMenu();
              item.onPick();
            }}
          >
            {item.icon !== undefined && <span class="menu-icon">{item.icon}</span>}
            <span class="menu-label">{item.label}</span>
            {item.hint && <span class="menu-hint">{item.hint}</span>}
            {item.checked !== undefined && (
              <span class="menu-check" style={{ opacity: item.checked ? 1 : 0 }}>
                <CheckIcon />
              </span>
            )}
          </button>
        ),
      )}
    </div>
  );
}
