import { create } from "zustand";
import { api } from "./api";
import type { BeeEvent, FlowFrame, Meta, Registry, Selection, Snapshot } from "./types";

export type Tab = "topology" | "node" | "timeline" | "operations" | "problems";
export type SSEState = "connecting" | "open" | "closed";

const MAX_EVENTS = 5000;

interface State {
  meta: Meta | null;
  registry: Registry | null;
  live: Snapshot | null;
  liveAt: number; // wall ms when the last snapshot frame arrived
  frozen: Snapshot | null; // view while paused
  paused: boolean;
  cursor: number | null; // ms; null = follow live
  keyframe: Snapshot | null;
  keyframeLoading: boolean;
  keyframeError: string | null;
  events: BeeEvent[];
  lastSeq: number;
  sse: SSEState;
  gap: { requested: number; oldest: number } | null;
  selection: Selection | null;
  tab: Tab;
  internalsNode: string | null;
  searchOpen: boolean;
  timelineSpanMs: number; // width of the timeline view window

  setMeta(m: Meta): void;
  setRegistry(r: Registry): void;
  onSnapshot(s: Snapshot): void;
  onEvents(evs: BeeEvent[]): void;
  setSSE(s: SSEState): void;
  setGap(g: State["gap"]): void;
  select(s: Selection | null): void;
  setTab(t: Tab): void;
  setInternalsNode(n: string | null): void;
  setSearchOpen(o: boolean): void;
  togglePause(): void;
  goLive(): void;
  setCursor(ms: number): void;
  setTimelineSpan(ms: number): void;
}

const seen = new Set<number>();

let cursorTimer: ReturnType<typeof setTimeout> | undefined;
let cursorAbort: AbortController | undefined;

export const useStore = create<State>((set, get) => ({
  meta: null,
  registry: null,
  live: null,
  liveAt: 0,
  frozen: null,
  paused: false,
  cursor: null,
  keyframe: null,
  keyframeLoading: false,
  keyframeError: null,
  events: [],
  lastSeq: 0,
  sse: "connecting",
  gap: null,
  selection: null,
  tab: "topology",
  internalsNode: null,
  searchOpen: false,
  timelineSpanMs: 5 * 60_000,

  setMeta: (meta) => set({ meta }),
  setRegistry: (registry) => set({ registry }),
  onSnapshot: (s) => set({ live: s, liveAt: Date.now() }),
  onEvents: (evs) => {
    const fresh = evs.filter((e) => !seen.has(e.seq));
    if (!fresh.length) return;
    for (const e of fresh) seen.add(e.seq);
    let events = get().events.concat(fresh);
    // Replayed and live batches can interleave; keep seq order.
    if (fresh.length && events.length > fresh.length && fresh[0].seq < events[events.length - fresh.length - 1].seq) {
      events.sort((a, b) => a.seq - b.seq);
    }
    if (events.length > MAX_EVENTS) {
      const drop = events.slice(0, events.length - MAX_EVENTS);
      for (const e of drop) seen.delete(e.seq);
      events = events.slice(events.length - MAX_EVENTS);
    }
    set({ events, lastSeq: Math.max(get().lastSeq, events[events.length - 1].seq) });
  },
  setSSE: (sse) => set({ sse }),
  setGap: (gap) => set({ gap }),
  select: (selection) => {
    const patch: Partial<State> = { selection };
    if (selection && (selection.type === "node" || selection.type === "component")) {
      patch.internalsNode = selection.node;
    }
    set(patch);
  },
  setTab: (tab) => set({ tab }),
  setInternalsNode: (internalsNode) => set({ internalsNode }),
  setSearchOpen: (searchOpen) => set({ searchOpen }),
  togglePause: () => {
    const { paused, live, cursor } = get();
    if (cursor !== null) {
      get().goLive();
      return;
    }
    set(paused ? { paused: false, frozen: null } : { paused: true, frozen: live });
  },
  goLive: () => {
    clearTimeout(cursorTimer);
    cursorAbort?.abort();
    set({ cursor: null, keyframe: null, paused: false, frozen: null, keyframeLoading: false, keyframeError: null });
  },
  setCursor: (ms) => {
    set({ cursor: ms, keyframeLoading: true });
    clearTimeout(cursorTimer);
    cursorTimer = setTimeout(() => {
      cursorAbort?.abort();
      const ac = new AbortController();
      cursorAbort = ac;
      api
        .state(Math.round(ms), ac.signal)
        .then((keyframe) => {
          if (get().cursor === null) return;
          set({ keyframe, keyframeLoading: false, keyframeError: null });
        })
        .catch((err: unknown) => {
          if (ac.signal.aborted) return;
          set({ keyframeLoading: false, keyframeError: err instanceof Error ? err.message : String(err) });
        });
    }, 120);
  },
  setTimelineSpan: (timelineSpanMs) => set({ timelineSpanMs }),
}));

/** The snapshot every view renders: a keyframe, the frozen view or live. */
export function useView(): Snapshot | null {
  return useStore((s) => (s.cursor !== null ? (s.keyframe ?? s.frozen ?? s.live) : s.paused ? s.frozen : s.live));
}

/** The instant the view represents (ms). */
export function useViewTime(): number {
  const view = useView();
  const cursor = useStore((s) => s.cursor);
  if (cursor !== null) return cursor;
  return view ? Date.parse(view.at) : Date.now();
}

export function useIsLive(): boolean {
  return useStore((s) => s.cursor === null && !s.paused);
}

/**
 * Flow frames are kept outside React state: the topology canvas reads them
 * on every animation frame and nothing re-renders when they arrive.
 */
type FlowListener = (f: FlowFrame) => void;
const flowListeners = new Set<FlowListener>();
const flowHistory: FlowFrame[] = [];

export const flowBus = {
  push(f: FlowFrame) {
    flowHistory.push(f);
    if (flowHistory.length > 240) flowHistory.splice(0, flowHistory.length - 240);
    for (const l of flowListeners) l(f);
  },
  subscribe(l: FlowListener): () => void {
    flowListeners.add(l);
    return () => flowListeners.delete(l);
  },
  /** Frames received in the last `ms` milliseconds (by frame time). */
  recent(ms: number, now = Date.now()): FlowFrame[] {
    return flowHistory.filter((f) => now - Date.parse(f.at) <= ms);
  },
};
