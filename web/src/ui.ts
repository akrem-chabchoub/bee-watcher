import { create } from "zustand";

export type Level = "kid" | "engineer";
export type ToastKind = "ok" | "bad" | "info";

export interface Toast {
  id: number;
  kind: ToastKind;
  text: string;
  detail?: string;
}

interface UIState {
  /** Explain popover: glossary entry and anchor point (viewport px). */
  explain: { id: string; x: number; y: number } | null;
  glossaryOpen: boolean;
  level: Level;
  toasts: Toast[];
  quit: boolean;
  openExplain(id: string, x: number, y: number): void;
  closeExplain(): void;
  setGlossaryOpen(o: boolean): void;
  setLevel(l: Level): void;
  toast(kind: ToastKind, text: string, detail?: string): void;
  dismiss(id: number): void;
  setQuit(): void;
}

function loadLevel(): Level {
  try {
    return localStorage.getItem("bee-scope.level") === "engineer" ? "engineer" : "kid";
  } catch {
    return "kid";
  }
}

let toastSeq = 0;

export const useUI = create<UIState>((set, get) => ({
  explain: null,
  glossaryOpen: false,
  level: loadLevel(),
  toasts: [],
  quit: false,
  openExplain: (id, x, y) => set({ explain: { id, x, y } }),
  closeExplain: () => set({ explain: null }),
  setGlossaryOpen: (glossaryOpen) => set({ glossaryOpen }),
  setLevel: (level) => {
    try {
      localStorage.setItem("bee-scope.level", level);
    } catch {
      /* storage unavailable */
    }
    set({ level });
  },
  toast: (kind, text, detail) => {
    const id = ++toastSeq;
    set({ toasts: [...get().toasts.slice(-4), { id, kind, text, detail }] });
    setTimeout(() => get().dismiss(id), kind === "bad" ? 9000 : 5000);
  },
  dismiss: (id) => set({ toasts: get().toasts.filter((t) => t.id !== id) }),
  setQuit: () => set({ quit: true }),
}));

export const toast = (kind: ToastKind, text: string, detail?: string) => useUI.getState().toast(kind, text, detail);

/** Reads a per-browser flag (first-run cards etc.). */
export function getFlag(key: string): boolean {
  try {
    return localStorage.getItem("bee-scope." + key) === "1";
  } catch {
    return false;
  }
}

export function setFlag(key: string, on: boolean) {
  try {
    if (on) localStorage.setItem("bee-scope." + key, "1");
    else localStorage.removeItem("bee-scope." + key);
  } catch {
    /* storage unavailable */
  }
}
