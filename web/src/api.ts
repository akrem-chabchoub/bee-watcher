import type {
  ContextList,
  ProbeResult,
  ActionResponse,
  BeeEvent,
  ControlSettings,
  ControlStatus,
  Job,
  JobLog,
  Upload,
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

/** Parses a JSON error body ({"error": "..."}) or falls back to the text. */
async function errorOf(r: Response): Promise<HttpError> {
  const text = await r.text().catch(() => "");
  let msg = text.trim() || r.statusText;
  try {
    const j = JSON.parse(text) as { error?: unknown };
    if (j && typeof j === "object" && j.error) msg = String(j.error);
  } catch {
    /* plain text */
  }
  return new HttpError(r.status, msg);
}

async function send<T>(method: "POST" | "PUT", path: string, body: Record<string, unknown>, signal?: AbortSignal): Promise<T> {
  const r = await fetch(path, {
    method,
    signal,
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    body: JSON.stringify({ confirm: true, ...body }),
  });
  if (!r.ok) throw await errorOf(r);
  const text = await r.text();
  return (text ? JSON.parse(text) : null) as T;
}

const enc = encodeURIComponent;

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
  control: {
    status: (s?: AbortSignal) => get<ControlStatus>("/api/control/status", s),
    settings: (s?: AbortSignal) => get<ControlSettings>("/api/control/settings", s),
    contexts: (s?: AbortSignal) => get<ContextList>("/api/control/contexts", s),
    setContext: (context: string, namespace: string) => send<{ current: { context: string; namespace: string } }>("POST", "/api/control/context", { context, namespace }),
    saveSettings: (v: Partial<ControlSettings>) => send<ControlSettings>("PUT", "/api/control/settings", v as Record<string, unknown>),
    run: (name: string, body: Record<string, unknown> = {}) => send<{ job: Job }>("POST", `/api/control/actions/${enc(name)}`, body).then((x) => x.job),
    jobs: (s?: AbortSignal) => get<Job[] | null>("/api/control/jobs", s).then((x) => x ?? []),
    log: (id: string, after: number, s?: AbortSignal) => get<JobLog>(`/api/control/jobs/${enc(id)}/log` + qs({ after }), s),
    cancel: (id: string) => send<{ job: Job }>("POST", `/api/control/jobs/${enc(id)}/cancel`, {}).then((x) => x.job),
  },
  data: {
    /** Read-only passthrough; resolves Bee's JSON body. */
    get: async <T>(node: string, path: string, s?: AbortSignal): Promise<T> => {
      const r = await fetch(`/api/data/${enc(node)}/get` + qs({ path }), { signal: s, headers: { Accept: "application/json" } });
      if (!r.ok) throw await errorOf(r);
      return (await r.json()) as T;
    },
    buyStamp: (node: string, b: { amount: string; depth: number; label: string; immutable: boolean }) =>
      send<{ batchID: string; txHash: string }>("POST", `/api/data/${enc(node)}/stamps`, b),
    topup: (node: string, id: string, amount: string) => send<unknown>("POST", `/api/data/${enc(node)}/stamps/${enc(id)}/topup`, { amount }),
    dilute: (node: string, id: string, depth: number) => send<unknown>("POST", `/api/data/${enc(node)}/stamps/${enc(id)}/dilute`, { depth }),
    uploads: (s?: AbortSignal) => get<Upload[] | null>("/api/data/uploads", s).then((x) => x ?? []),
    uploadURL: (node: string, p: { batch: string; name: string; pin: boolean; encrypt: boolean; deferred: boolean; redundancy: number }) =>
      `/api/data/${enc(node)}/upload?` +
      new URLSearchParams({
        batch: p.batch,
        name: p.name,
        pin: p.pin ? "1" : "0",
        encrypt: p.encrypt ? "1" : "0",
        deferred: p.deferred ? "1" : "0",
        redundancy: String(p.redundancy),
      }).toString(),
    downloadURL: (node: string, ref: string, path = "", name = "") => `/api/data/${enc(node)}/download/${enc(ref)}` + qs({ path, name }),
    /** Retrieves through the node and discards the content. */
    probe: (node: string, ref: string, path = "", s?: AbortSignal) => get<ProbeResult>(`/api/data/${enc(node)}/probe/${enc(ref)}` + qs({ path }), s),
  },
};
