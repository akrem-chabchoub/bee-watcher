import { useState } from "react";
import { api } from "../api";
import { usePoll } from "../hooks";
import { useStore } from "../store";
import { dur, fmtTime, num, short } from "../format";
import { ProvBadge, SevBadge } from "../components/Badges";
import type { ChunkLookup, Operation, Stage } from "../types";

const stateLook: Record<string, { icon: string; cls: string }> = {
  done: { icon: "✔", cls: "ok" },
  running: { icon: "▶", cls: "active" },
  pending: { icon: "…", cls: "idle" },
  stalled: { icon: "⏸", cls: "warn" },
  failed: { icon: "✕", cls: "bad" },
  unobservable: { icon: "⊘", cls: "muted" },
};

function StateTag({ state }: { state: string }) {
  const l = stateLook[state] ?? { icon: "?", cls: "idle" };
  return (
    <span className={"state-tag " + l.cls}>
      {l.icon} {state}
    </span>
  );
}

export function OperationsView() {
  const { data, error } = usePoll((s) => api.operations(s), [], 2000);
  const selection = useStore((s) => s.selection);
  const select = useStore((s) => s.select);
  const [filter, setFilter] = useState<"all" | "upload-tag" | "trace">("all");
  const ops = (data ?? []).filter((o) => filter === "all" || o.kind === filter);
  const selId = selection?.type === "operation" ? selection.id : null;
  const current = (selId && (data ?? []).find((o) => o.id === selId)) || ops[0];

  return (
    <div className="view ops">
      <div className="ops-list">
        <div className="filters">
          {(["all", "upload-tag", "trace"] as const).map((k) => (
            <button key={k} className={"chip" + (filter === k ? " on" : "")} onClick={() => setFilter(k)}>
              {k}
            </button>
          ))}
        </div>
        {error && <div className="warn-text">⚠ {error}</div>}
        {ops.length === 0 && (
          <div className="empty">
            No operations yet. Upload tags appear from <code>GET /tags</code>; traces need Bee tracing pointed at the OTLP receiver.
          </div>
        )}
        <ul>
          {ops.map((o) => (
            <li key={o.id} className={o.id === current?.id ? "on" : ""} onClick={() => select({ type: "operation", id: o.id })}>
              <div className="op-title">
                <StateTag state={o.state} /> {o.title}
              </div>
              <div className="muted">
                {o.kind} · {fmtTime(o.updated)} · {o.correlation} correlation <ProvBadge prov={o.prov} compact />
              </div>
            </li>
          ))}
        </ul>
        <ChunkBox />
      </div>
      <div className="ops-detail">{current ? <OperationDetail op={current} /> : <div className="empty">Select an operation.</div>}</div>
    </div>
  );
}

export function OperationDetail({ op }: { op: Operation }) {
  return (
    <div>
      <h2>
        <StateTag state={op.state} /> {op.title}
      </h2>
      <div className="muted">
        {op.kind} · started {fmtTime(op.start)} · updated {fmtTime(op.updated)} · nodes {(op.nodes ?? []).join(", ") || "—"} · <ProvBadge prov={op.prov} />{" "}
        <span className={"corr-label " + op.correlation}>{op.correlation === "observed" ? "observed correlation" : "approximate correlation"}</span>
      </div>
      {op.note && <p className="note">{op.note}</p>}
      {op.kind === "trace" ? <Waterfall op={op} /> : <Pipeline stages={op.stages ?? []} />}
    </div>
  );
}

function Pipeline({ stages }: { stages: Stage[] }) {
  return (
    <ol className="pipeline">
      {stages.map((s, i) => {
        const pct = s.total ? Math.min(100, (100 * (s.count ?? 0)) / s.total) : s.state === "done" ? 100 : undefined;
        const took = s.start && s.end ? Date.parse(s.end) - Date.parse(s.start) : undefined;
        return (
          <li key={i} className={"stage " + (stateLook[s.state]?.cls ?? "")}>
            <div className="stage-head">
              <StateTag state={s.state} /> <b>{s.name}</b>
              {s.component && <span className="muted"> · {s.component}</span>}
              {took !== undefined && took >= 0 && <span className="muted"> · {dur(took)}</span>}
            </div>
            {pct !== undefined && (
              <div className="bar" title={`${s.count ?? 0} of ${s.total ?? "?"}`}>
                <div style={{ width: pct + "%" }} />
                <span>
                  {num(s.count ?? 0)}
                  {s.total ? ` / ${num(s.total)}` : ""}
                </span>
              </div>
            )}
            {s.state === "unobservable" && <div className="bar hatched" />}
            {s.detail && <div className="muted small">{s.detail}</div>}
          </li>
        );
      })}
    </ol>
  );
}

function Waterfall({ op }: { op: Operation }) {
  const stages = op.stages ?? [];
  const t0 = Date.parse(op.start);
  const t1 = Math.max(Date.parse(op.updated), ...stages.map((s) => (s.end ? Date.parse(s.end) : t0)));
  const total = Math.max(1, t1 - t0);
  const depth = new Map<string, number>();
  const byId = new Map(stages.filter((s) => s.spanId).map((s) => [s.spanId!, s]));
  const d = (s: Stage, guard = 0): number => {
    if (!s.parent || !byId.has(s.parent) || guard > 30) return 0;
    if (depth.has(s.spanId!)) return depth.get(s.spanId!)!;
    const v = d(byId.get(s.parent)!, guard + 1) + 1;
    depth.set(s.spanId!, v);
    return v;
  };
  const nodes = [...new Set(stages.map((s) => s.node || "?"))];
  const nodeColor = (n: string) => ["#5aa9ff", "#3ddc97", "#c58cff", "#ffcc66", "#ff9f6e", "#6fd3e0", "#e48ad6"][nodes.indexOf(n) % 7];
  return (
    <div className="waterfall">
      <div className="muted small">
        Spans sharing trace ID {short(op.id.replace("trace:", ""), 16)} — <b>observed correlation</b> via the <code>tracing-span-context-v2</code> p2p header. Total{" "}
        {dur(total)}.
      </div>
      <div className="wf-legend">
        {nodes.map((n) => (
          <span key={n}>
            <i style={{ background: nodeColor(n) }} /> {n === "?" ? "unresolved node" : n}
          </span>
        ))}
      </div>
      {stages.map((s, i) => {
        const a = s.start ? Date.parse(s.start) - t0 : 0;
        const b = s.end ? Date.parse(s.end) - t0 : a;
        return (
          <div key={s.spanId ?? i} className="wf-row" title={Object.entries(s.attrs ?? {}).map(([k, v]) => `${k}=${v}`).join("\n")}>
            <div className="wf-name" style={{ paddingLeft: 6 + d(s) * 12 }}>
              {s.state === "failed" ? "✕ " : ""}
              {s.name} <span className="muted">{s.node ?? ""}</span>
            </div>
            <div className="wf-track">
              <div
                className={"wf-bar" + (s.state === "failed" ? " failed" : "")}
                style={{ left: (100 * a) / total + "%", width: Math.max(0.4, (100 * (b - a)) / total) + "%", background: nodeColor(s.node || "?") }}
              />
              <span className="wf-dur">{dur(b - a)}</span>
            </div>
          </div>
        );
      })}
    </div>
  );
}

function ChunkBox() {
  const [addr, setAddr] = useState("");
  const [res, setRes] = useState<ChunkLookup | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const select = useStore((s) => s.select);
  const go = () => {
    const a = addr.trim().toLowerCase();
    if (!a) return;
    setErr(null);
    api
      .chunk(a)
      .then(setRes)
      .catch((e: unknown) => setErr(e instanceof Error ? e.message : String(e)));
  };
  return (
    <div className="chunkbox">
      <h3>Follow a chunk</h3>
      <div className="row">
        <input placeholder="chunk address (hex)" value={addr} onChange={(e) => setAddr(e.target.value)} onKeyDown={(e) => e.key === "Enter" && go()} />
        <button className="btn" onClick={go}>
          Look up
        </button>
      </div>
      {err && <div className="warn-text">⚠ {err}</div>}
      {res && (
        <div>
          <div className="corr-label approximate">approximate: matched by address in logs and events, not by a trace</div>
          <ul className="mini-events">
            {(res.events ?? []).map((e) => (
              <li key={e.seq} onClick={() => select({ type: "event", seq: e.seq })}>
                <SevBadge sev={e.sev} compact /> <span className="mono">{fmtTime(e.t, true)}</span> {e.node} {e.summary}
              </li>
            ))}
            {(res.logs ?? []).map((l) => (
              <li key={"l" + l.id} className="mono small">
                {fmtTime(l.line.time, true)} {l.node} [{l.line.logger}] {l.line.msg}
              </li>
            ))}
            {!(res.events ?? []).length && !(res.logs ?? []).length && <li className="muted">Nothing mentions this address in the retained history.</li>}
          </ul>
        </div>
      )}
    </div>
  );
}
