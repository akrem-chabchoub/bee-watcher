import { useEffect, useMemo, useRef, useState } from "react";
import { api } from "../api";
import { useIsLive, useStore, useView } from "../store";
import { fmtTime, sevLook, sevRank } from "../format";
import { ProvBadge, SevBadge, useNow } from "../components/Badges";
import type { BeeEvent, Provenance, Severity } from "../types";

const SEV_COLOR: Record<Severity, string> = { debug: "#5b6577", info: "#5aa9ff", warn: "#f5b301", error: "#ff5d5d" };
const PROVS: Provenance[] = ["observed", "derived", "inferred", "simulated"];
const LABEL_W = 110;

function Mark({ sev, x, y, sel }: { sev: Severity; x: number; y: number; sel: boolean }) {
  const c = SEV_COLOR[sev];
  const stroke = sel ? "#fff" : "none";
  switch (sev) {
    case "error":
      return <rect x={x - 4.5} y={y - 4.5} width={9} height={9} fill={c} stroke={stroke} strokeWidth={2} />;
    case "warn":
      return <path d={`M${x},${y - 5.5} L${x + 5.5},${y + 4.5} L${x - 5.5},${y + 4.5} Z`} fill={c} stroke={stroke} strokeWidth={2} />;
    case "info":
      return <circle cx={x} cy={y} r={3.5} fill={c} stroke={stroke} strokeWidth={2} />;
    default:
      return <circle cx={x} cy={y} r={2} fill={c} stroke={stroke} strokeWidth={2} />;
  }
}

export function TimelineView() {
  const view = useView();
  const live = useIsLive();
  const cursor = useStore((s) => s.cursor);
  const span = useStore((s) => s.timelineSpanMs);
  const buffered = useStore((s) => s.events);
  const registry = useStore((s) => s.registry);
  const selection = useStore((s) => s.selection);
  const select = useStore((s) => s.select);
  const setCursor = useStore((s) => s.setCursor);
  const now = useNow(1000);

  const [kind, setKind] = useState("");
  const [minSev, setMinSev] = useState<Severity>("info");
  const [component, setComponent] = useState("");
  const [nodeFilter, setNodeFilter] = useState<string[]>([]);
  const [provs, setProvs] = useState<Provenance[]>([]);
  const [text, setText] = useState("");
  const [fetched, setFetched] = useState<BeeEvent[] | null>(null);
  const [width, setWidth] = useState(900);
  const wrap = useRef<HTMLDivElement>(null);

  const end = cursor ?? (live ? now : view ? Date.parse(view.at) : now);
  const start = end - span;

  useEffect(() => {
    const el = wrap.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setWidth(el.clientWidth));
    ro.observe(el);
    setWidth(el.clientWidth);
    return () => ro.disconnect();
  }, []);

  // In the past (or when the buffer does not reach back far enough) ask the
  // server for the exact window.
  const bufferStart = buffered.length ? Date.parse(buffered[0].t) : Infinity;
  const needFetch = !live || start < bufferStart;
  useEffect(() => {
    if (!needFetch) {
      setFetched(null);
      return;
    }
    const ac = new AbortController();
    const t = setTimeout(() => {
      api
        .events({ from: Math.round(start), to: Math.round(end), limit: 5000, newest: true }, ac.signal)
        .then(setFetched)
        .catch(() => undefined);
    }, 200);
    return () => {
      clearTimeout(t);
      ac.abort();
    };
    // refetch when the window moves by more than a second
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [needFetch, Math.round(start / 1000), Math.round(end / 1000)]);

  const source = needFetch && fetched ? mergeEvents(fetched, buffered) : buffered;

  const filtered = useMemo(() => {
    const k = kind
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);
    const q = text.trim().toLowerCase();
    return source.filter((e) => {
      const t = Date.parse(e.t);
      if (t < start || t > end) return false;
      if (sevRank[e.sev] < sevRank[minSev]) return false;
      if (k.length && !k.some((p) => e.kind.startsWith(p))) return false;
      if (component && e.component !== component) return false;
      if (nodeFilter.length && !nodeFilter.includes(e.node ?? "") && !(e.corr?.peerNode && nodeFilter.includes(e.corr.peerNode))) return false;
      if (provs.length && !provs.includes(e.prov)) return false;
      if (q && !(e.summary.toLowerCase().includes(q) || e.kind.includes(q) || (e.node ?? "").includes(q))) return false;
      return true;
    });
  }, [source, start, end, kind, minSev, component, nodeFilter, provs, text]);

  const nodes = useMemo(() => {
    const s = new Set<string>(view ? Object.keys(view.nodes) : []);
    for (const e of filtered) if (e.node) s.add(e.node);
    let list = [...s].sort((a, b) => a.localeCompare(b, undefined, { numeric: true }));
    if (nodeFilter.length) list = list.filter((n) => nodeFilter.includes(n));
    return ["(cluster)", ...list];
  }, [view, filtered, nodeFilter]);

  const laneH = 26;
  const plotW = Math.max(100, width - LABEL_W - 10);
  const x = (t: number) => LABEL_W + ((t - start) / span) * plotW;
  const laneOf = (e: BeeEvent) => Math.max(0, nodes.indexOf(e.node ? e.node : "(cluster)"));
  const selSeq = selection?.type === "event" ? selection.seq : null;
  const ticks = useMemo(() => {
    // Smallest step that keeps labels at least 70 px apart.
    const steps = [15_000, 30_000, 60_000, 120_000, 300_000, 600_000];
    const step = steps.find((s) => (span / s) * 70 <= plotW) ?? steps[steps.length - 1];
    const out: number[] = [];
    for (let t = Math.ceil(start / step) * step; t <= end; t += step) if (x(t) <= width - 30) out.push(t);
    return out;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [start, end, span, plotW, width]);

  const allNodes = view ? Object.keys(view.nodes).sort((a, b) => a.localeCompare(b, undefined, { numeric: true })) : [];
  const list = filtered.slice(-400).reverse();

  return (
    <div className="view timeline">
      <div className="filters">
        <input placeholder="kind prefix (e.g. peer.,retrieval.)" value={kind} onChange={(e) => setKind(e.target.value)} />
        <input placeholder="text" value={text} onChange={(e) => setText(e.target.value)} />
        <label>
          min severity{" "}
          <select value={minSev} onChange={(e) => setMinSev(e.target.value as Severity)}>
            {(["debug", "info", "warn", "error"] as Severity[]).map((s) => (
              <option key={s} value={s}>
                {sevLook[s].icon} {s}
              </option>
            ))}
          </select>
        </label>
        <label>
          component{" "}
          <select value={component} onChange={(e) => setComponent(e.target.value)}>
            <option value="">all</option>
            {(registry?.specs ?? []).map((s) => (
              <option key={s.id} value={s.id}>
                {s.label}
              </option>
            ))}
          </select>
        </label>
        <div className="chips">
          {PROVS.map((p) => (
            <button
              key={p}
              className={"chip" + (provs.includes(p) ? " on" : "")}
              onClick={() => setProvs((v) => (v.includes(p) ? v.filter((x) => x !== p) : [...v, p]))}
              title={`show only ${p} events (toggle)`}
            >
              <ProvBadge prov={p} />
            </button>
          ))}
        </div>
        <div className="chips">
          {allNodes.map((n) => (
            <button
              key={n}
              className={"chip" + (nodeFilter.includes(n) ? " on" : "")}
              onClick={() => setNodeFilter((v) => (v.includes(n) ? v.filter((x) => x !== n) : [...v, n]))}
              title="filter / compare nodes on a shared axis"
            >
              {n}
            </button>
          ))}
          {nodeFilter.length > 0 && (
            <button className="chip" onClick={() => setNodeFilter([])}>
              ✕ all nodes
            </button>
          )}
        </div>
        <span className="muted">
          {filtered.length} events · {fmtTime(start)}–{fmtTime(end)}
          {needFetch && !fetched ? " · loading…" : ""}
        </span>
      </div>
      <div className="lanes" ref={wrap}>
        <svg width={width} height={nodes.length * laneH + 24}>
          {ticks.map((t) => (
            <g key={t}>
              <line x1={x(t)} x2={x(t)} y1={0} y2={nodes.length * laneH} stroke="#232a36" />
              <text x={Math.min(x(t), width - 28)} y={nodes.length * laneH + 16} className="tick" textAnchor="middle">
                {fmtTime(t)}
              </text>
            </g>
          ))}
          {nodes.map((n, i) => (
            <g key={n}>
              <rect x={0} y={i * laneH} width={width} height={laneH} fill={i % 2 ? "#141922" : "#11151c"} />
              <text
                x={8}
                y={i * laneH + 17}
                className="lane-label"
                onClick={() => n !== "(cluster)" && select({ type: "node", node: n })}
                style={{ cursor: n === "(cluster)" ? "default" : "pointer" }}
              >
                {n}
              </text>
            </g>
          ))}
          {filtered.map((e) => (
            <g
              key={e.seq}
              onClick={() => select({ type: "event", seq: e.seq })}
              style={{ cursor: "pointer" }}
              opacity={e.prov === "inferred" ? 0.75 : 1}
            >
              <title>{`${fmtTime(e.t, true)} ${e.node ?? ""} ${e.kind}\n${e.summary}\n${e.prov} · ${e.source.adapter} ${e.source.ref}`}</title>
              <Mark sev={e.sev} x={x(Date.parse(e.t))} y={laneOf(e) * laneH + laneH / 2} sel={e.seq === selSeq} />
            </g>
          ))}
          {cursor !== null && <line x1={x(cursor)} x2={x(cursor)} y1={0} y2={nodes.length * laneH} stroke="#fff" strokeDasharray="3 3" />}
        </svg>
      </div>
      <div className="event-list">
        <table>
          <thead>
            <tr>
              <th>time</th>
              <th>sev</th>
              <th>node</th>
              <th>kind</th>
              <th>summary</th>
              <th>prov</th>
              <th>source</th>
            </tr>
          </thead>
          <tbody>
            {list.map((e) => (
              <tr
                key={e.seq}
                className={e.seq === selSeq ? "on" : ""}
                onClick={() => select({ type: "event", seq: e.seq })}
                onDoubleClick={() => setCursor(Date.parse(e.t))}
                title="click to inspect · double-click to move the time cursor here"
              >
                <td className="mono">{fmtTime(e.t, true)}</td>
                <td>
                  <SevBadge sev={e.sev} compact />
                </td>
                <td>{e.node ?? "—"}</td>
                <td className="mono">{e.kind}</td>
                <td className="summary">{e.summary}</td>
                <td>
                  <ProvBadge prov={e.prov} compact />
                </td>
                <td className="muted">{e.source.adapter}</td>
              </tr>
            ))}
          </tbody>
        </table>
        {list.length === 0 && <div className="empty">No events in this window match the filters.</div>}
      </div>
    </div>
  );
}

function mergeEvents(a: BeeEvent[], b: BeeEvent[]): BeeEvent[] {
  const m = new Map<number, BeeEvent>();
  for (const e of a) m.set(e.seq, e);
  for (const e of b) m.set(e.seq, e);
  return [...m.values()].sort((x, y) => x.seq - y.seq);
}
