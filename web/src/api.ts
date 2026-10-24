import type {
  ActionResponse,
  BeeEvent,
  Bucket,
  ChunkLookup,
  FlowFrame,
  HistoryWindow,
  LogRecord,
  Meta,
  NodeDetail,
  Operation,
  Registry,
  SearchResult,
  Snapshot,
} from "./types";

export class HttpError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

async function get<T>(path: string, signal?: AbortSignal): Promise<T> {
  const r = await fetch(path, { signal, headers: { Accept: "application/json" } });
  if (!r.ok) {
    const text = await r.text().catch(() => "");
    throw new HttpError(r.status, text.trim() || r.statusText);
  }
  return (await r.json()) as T;
}

type Params = Record<string, string | number | boolean | undefined | null | string[]>;

function qs(p: Params): string {
  const u = new URLSearchParams();
  for (const [k, v] of Object.entries(p)) {
    if (v === undefined || v === null || v === "" || v === false) continue;
    if (Array.isArray(v)) {
      if (v.length) u.set(k, v.join(","));
    } else {
      u.set(k, v === true ? "1" : String(v));
    }
  }
  const s = u.toString();
  return s ? "?" + s : "";
}

export interface EventQuery {
  from?: string | number;
  to?: string | number;
  after?: number;
  before?: number;
  node?: string[] | string;
  component?: string[] | string;
  kind?: string[] | string;
  minSev?: string;
  prov?: string[];
  q?: string;
  corr?: string;
  limit?: number;
  newest?: boolean;
}

export const api = {
  meta: (s?: AbortSignal) => get<Meta>("/api/meta", s),
  snapshot: (s?: AbortSignal) => get<Snapshot>("/api/snapshot", s),
  components: (s?: AbortSignal) => get<Registry>("/api/components", s),
  window: (s?: AbortSignal) => get<HistoryWindow>("/api/window", s),
  state: (at: number, s?: AbortSignal) => get<Snapshot>("/api/state" + qs({ at }), s),
  events: (q: EventQuery, s?: AbortSignal) => get<BeeEvent[] | null>("/api/events" + qs(q as Params), s).then((x) => x ?? []),
  event: (seq: number, s?: AbortSignal) => get<BeeEvent>(`/api/event/${seq}`, s),
  density: (from: number, to: number, n: number, q: EventQuery = {}, s?: AbortSignal) =>
    get<Bucket[] | null>("/api/density" + qs({ ...(q as Params), from, to, n }), s).then((x) => x ?? []),
  flows: (from: number, to: number, s?: AbortSignal) => get<FlowFrame[] | null>("/api/flows" + qs({ from, to }), s).then((x) => x ?? []),
  raw: async (id: number, s?: AbortSignal): Promise<string> => {
    const r = await fetch(`/api/raw/${id}`, { signal: s });
    if (!r.ok) throw new HttpError(r.status, (await r.text()).trim());
    return r.text();
  },
  node: (name: string, s?: AbortSignal) => get<NodeDetail>(`/api/node/${encodeURIComponent(name)}`, s),
  logs: (p: { node?: string; component?: string; q?: string; level?: string; from?: number; to?: number; limit?: number }, s?: AbortSignal) =>
    get<LogRecord[] | null>("/api/logs" + qs(p as Params), s).then((x) => x ?? []),
  chunk: (addr: string, s?: AbortSignal) => get<ChunkLookup>(`/api/chunk/${encodeURIComponent(addr)}`, s),
  operations: (s?: AbortSignal) => get<Operation[] | null>("/api/operations", s).then((x) => x ?? []),
  operation: (id: string, s?: AbortSignal) => get<Operation>(`/api/operations/${encodeURIComponent(id)}`, s),
  search: (q: string, s?: AbortSignal) => get<SearchResult[] | null>("/api/search" + qs({ q }), s).then((x) => x ?? []),
  action: async (name: string, body: Record<string, unknown>): Promise<ActionResponse> => {
    const r = await fetch(`/api/actions/${name}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    const text = await r.text();
    let parsed: unknown = text;
    try {
      parsed = JSON.parse(text);
    } catch {
      /* plain text */
    }
    if (!r.ok) {
      const msg = typeof parsed === "object" && parsed && "error" in parsed ? String((parsed as { error: unknown }).error) : text;
      return { ok: false, status: r.status, ms: 0, body: parsed, error: msg };
    }
    return parsed as ActionResponse;
  },
};
