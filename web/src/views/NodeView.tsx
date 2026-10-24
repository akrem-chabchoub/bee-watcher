import { useMemo } from "react";
import { useIsLive, useStore, useView } from "../store";
import { num, statusLook } from "../format";
import { Fresh, PhaseBadge, ProvBadge } from "../components/Badges";
import { useReducedMotion } from "../hooks";
import type { ComponentSpec, ComponentState } from "../types";

const W = 1200;
const H = 680;
const BW = 158;
const BH = 58;

const statusFill: Record<string, string> = {
  active: "#5aa9ff",
  idle: "#7b8496",
  degraded: "#f5b301",
  failed: "#ff5d5d",
  unobservable: "#5b6577",
  unknown: "#7b8496",
};

const pathColor: Record<string, string> = {
  upload: "#5aa9ff",
  download: "#3ddc97",
  sync: "#c58cff",
  topology: "#ffcc66",
  economy: "#ff9f6e",
};

function center(s: ComponentSpec) {
  return { x: 40 + s.x * (W - 80 - BW) + BW / 2, y: 30 + s.y * (H - 60 - BH) + BH / 2 };
}

/** Point where the segment from the box center towards (tx,ty) leaves the box. */
function border(c: { x: number; y: number }, tx: number, ty: number) {
  const dx = tx - c.x;
  const dy = ty - c.y;
  const sx = dx === 0 ? Infinity : BW / 2 / Math.abs(dx);
  const sy = dy === 0 ? Infinity : BH / 2 / Math.abs(dy);
  const s = Math.min(sx, sy);
  return { x: c.x + dx * s, y: c.y + dy * s };
}

export function NodeView() {
  const view = useView();
  const registry = useStore((s) => s.registry);
  const internalsNode = useStore((s) => s.internalsNode);
  const setInternalsNode = useStore((s) => s.setInternalsNode);
  const selection = useStore((s) => s.selection);
  const select = useStore((s) => s.select);
  const live = useIsLive();
  const reduced = useReducedMotion();

  const names = useMemo(
    () => (view ? Object.keys(view.nodes).sort((a, b) => a.localeCompare(b, undefined, { numeric: true })) : []),
    [view],
  );
  const nodeName = internalsNode && view?.nodes[internalsNode] ? internalsNode : names[0];
  const node = nodeName ? view?.nodes[nodeName] : undefined;

  if (!registry) return <div className="view empty">Loading the component registry…</div>;
  if (!view || !node) return <div className="view empty">No nodes discovered yet.</div>;

  const specs = registry.specs;
  const byId = new Map(specs.map((s) => [s.id, s]));
  const selComp = selection?.type === "component" && selection.node === node.name ? selection.component : null;
  const animate = live && !reduced;

  return (
    <div className="view nodeview">
      <div className="nodeview-head">
        <label>
          Node{" "}
          <select value={nodeName} onChange={(e) => setInternalsNode(e.target.value)}>
            {names.map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </select>
        </label>
        <PhaseBadge phase={node.phase} />
        <span className="muted">{node.group}</span>
        {node.version && <span className="muted">v{node.version}</span>}
        {node.status && (
          <span>
            radius {node.status.storageRadius} · depth {node.depth} · {node.connected} peers · pullsync {num(node.status.pullsyncRate)}/s
          </span>
        )}
        <span className={"logstream " + node.logStream} title="Pod log stream through the kube API">
          logs: {node.logStream === "following" ? "● following" : node.logStream === "error" ? "✕ error" : "○ off"}
        </span>
        <button className="btn" onClick={() => select({ type: "node", node: node.name })}>
          Inspect node
        </button>
      </div>
      <div className="internals-scroll">
        <svg viewBox={`0 0 ${W} ${H}`} className="internals" role="img" aria-label={`Subsystems of ${node.name}`}>
          <defs>
            <pattern id="hatch" width="8" height="8" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
              <rect width="8" height="8" fill="#161b24" />
              <line x1="0" y1="0" x2="0" y2="8" stroke="#3a4252" strokeWidth="3" />
            </pattern>
            {Object.entries(pathColor).map(([k, c]) => (
              <marker key={k} id={`arrow-${k}`} viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
                <path d="M0,0 L10,5 L0,10 z" fill={c} />
              </marker>
            ))}
          </defs>
          {registry.flows.map((f) => {
            const a = byId.get(f.from);
            const b = byId.get(f.to);
            if (!a || !b) return null;
            const ca = center(a);
            const cb = center(b);
            // offset parallel flows (a→b and b→a) so both stay visible
            const reverse = registry.flows.some((g) => g.from === f.to && g.to === f.from);
            const nx = -(cb.y - ca.y);
            const ny = cb.x - ca.x;
            const nl = Math.hypot(nx, ny) || 1;
            const off = reverse ? 6 : 0;
            const p1 = border(ca, cb.x, cb.y);
            const p2 = border(cb, ca.x, ca.y);
            const x1 = p1.x + (nx / nl) * off;
            const y1 = p1.y + (ny / nl) * off;
            const x2 = p2.x + (nx / nl) * off;
            const y2 = p2.y + (ny / nl) * off;
            const rate = node.flowRates?.[f.from + ">" + f.to] ?? 0;
            const col = pathColor[f.path] ?? "#8a93a6";
            const hasSignal = !!f.signal;
            const moving = animate && rate > 0.01;
            const speed = Math.max(0.25, 3 / Math.log2(2 + rate)); // seconds per dash cycle
            return (
              <g key={f.from + ">" + f.to} className="flow">
                <title>
                  {`${f.label} (${f.path})\n${hasSignal ? `signal ${f.signal}: ${num(rate)}/s (derived from metrics)` : "no metric signal: this relation is never animated"}`}
                </title>
                <line
                  x1={x1}
                  y1={y1}
                  x2={x2}
                  y2={y2}
                  stroke={col}
                  strokeOpacity={rate > 0.01 ? 0.95 : hasSignal ? 0.45 : 0.22}
                  strokeWidth={rate > 0.01 ? 2.2 : 1.3}
                  strokeDasharray={hasSignal ? (moving ? "6 6" : undefined) : "2 5"}
                  markerEnd={`url(#arrow-${f.path})`}
                  style={moving ? { animation: `dash ${speed}s linear infinite` } : undefined}
                />
                {rate > 0.01 && (
                  <text x={(x1 + x2) / 2} y={(y1 + y2) / 2 - 4} className="flow-rate" fill={col}>
                    {num(rate, 1)}/s
                  </text>
                )}
              </g>
            );
          })}
          {specs.map((s) => {
            const cs: ComponentState | undefined = node.components?.[s.id];
            const status = cs?.status ?? "unknown";
            const look = statusLook[status];
            const c = center(s);
            const x = c.x - BW / 2;
            const y = c.y - BH / 2;
            const unobs = status === "unobservable";
            const isSel = selComp === s.id;
            return (
              <g
                key={s.id}
                className="comp"
                onClick={() => select({ type: "component", node: node.name, component: s.id })}
                role="button"
                aria-label={`${s.label}: ${look.label}`}
              >
                <title>{`${s.label}: ${look.label}${cs?.reason ? " — " + cs.reason : ""}`}</title>
                <rect
                  x={x}
                  y={y}
                  width={BW}
                  height={BH}
                  rx={7}
                  fill={unobs ? "url(#hatch)" : "#161b24"}
                  stroke={isSel ? "#fff" : statusFill[status]}
                  strokeWidth={isSel ? 3 : status === "failed" || status === "degraded" ? 2.5 : 1.5}
                  strokeDasharray={cs?.stale ? "5 4" : undefined}
                />
                {status === "active" && animate && <rect x={x} y={y} width={BW} height={BH} rx={7} className="glow" />}
                <text x={x + 10} y={y + 20} className="comp-label">
                  {s.label}
                </text>
                <text x={x + 10} y={y + 38} className="comp-status" fill={statusFill[status]}>
                  {look.icon} {look.label}
                  {cs?.stale ? " · stale" : ""}
                </text>
                <text x={x + 10} y={y + 51} className="comp-sub">
                  {unobs
                    ? "no live source"
                    : [
                        cs && cs.activity > 0 ? `${num(cs.activity, 1)} ops/s` : null,
                        cs && cs.errorRate > 0 ? `${num(cs.errorRate, 2)} err/s` : null,
                        cs?.queue !== undefined && cs?.queue !== null ? `q ${num(cs.queue, 0)}` : null,
                        cs && cs.logRate > 0 ? `${num(cs.logRate, 1)} log/s` : null,
                      ]
                        .filter(Boolean)
                        .join(" · ") || (cs?.observable?.length ? cs.observable.join("+") : "")}
                </text>
              </g>
            );
          })}
        </svg>
      </div>
      <div className="nodeview-legend">
        {Object.entries(statusLook).map(([k, l]) => (
          <span key={k}>
            <span style={{ color: l.color }}>{l.icon}</span> {l.label}
          </span>
        ))}
        <span>
          <span className="hatch-sample" /> hatched = no source
        </span>
        <span>┅ dashed box = stale</span>
        {Object.entries(pathColor).map(([k, c]) => (
          <span key={k}>
            <span style={{ color: c }}>→</span> {k}
          </span>
        ))}
        <span className="muted">dotted arrow = relation without a metric signal (never animated)</span>
        <span className="muted">
          components <ProvBadge prov={node.components?.api?.prov ?? "derived"} compact /> · metrics <Fresh at={node.sources?.["GET /metrics"]?.lastOk} stale={node.sources?.["GET /metrics"]?.stale} />
        </span>
      </div>
    </div>
  );
}
