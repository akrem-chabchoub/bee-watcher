import { useEffect, useMemo, useRef, useState } from "react";
import { api } from "../api";
import { flowBus, useIsLive, useStore, useView } from "../store";
import { num, phaseLook, protoColor, short } from "../format";
import type { Edge, Flow, NodeState, Snapshot } from "../types";
import { useReducedMotion } from "../hooks";

const C = {
  ok: "#3ddc97",
  warn: "#f5b301",
  bad: "#ff5d5d",
  active: "#5aa9ff",
  idle: "#7b8496",
  text: "#d7dce5",
  muted: "#8a93a6",
  edge: "#46536a",
  bg: "#11151c",
  sel: "#ffffff",
};

const phaseColor: Record<string, string> = { up: C.ok, gated: C.warn, unreachable: C.bad, gone: C.bad, unknown: C.idle };

interface P {
  x: number;
  y: number;
}
interface Particle {
  from: string;
  to: string;
  color: string;
  start: number;
  dur: number;
  err: boolean;
}

/** Angle on the Kademlia ring from the first 16 bits of an overlay. */
function overlayAngle(hex: string): number | null {
  const m = /^[0-9a-f]{4}/i.exec(hex.replace(/^ext:/, ""));
  if (!m) return null;
  return (parseInt(m[0], 16) / 65536) * Math.PI * 2 - Math.PI / 2;
}

// spreadAngles pushes ring neighbours apart until each pair is at least minSep
// radians apart, keeping their order: nodes with nearby overlays (common in a
// small cluster) stay recognisably close without drawing on top of each other.
function spreadAngles(items: { a: number }[], minSep: number) {
  const n = items.length;
  if (n < 2) return;
  const TAU = Math.PI * 2;
  items.sort((x, y) => x.a - y.a);
  for (let pass = 0; pass < 60; pass++) {
    let moved = false;
    for (let i = 0; i < n; i++) {
      const cur = items[i];
      const next = items[(i + 1) % n];
      let gap = next.a - cur.a;
      if (i === n - 1) gap += TAU;
      if (gap < minSep) {
        const push = (minSep - gap) / 2;
        cur.a -= push;
        next.a += push;
        moved = true;
      }
    }
    if (!moved) break;
  }
}

function layout(snap: Snapshot | null, w: number, h: number, extNames: string[]) {
  const pos = new Map<string, P>();
  const ext = new Map<string, P>();
  if (!snap) return { pos, ext, cx: w / 2, cy: h / 2, r: 100 };
  const cy = h / 2;
  const r = Math.max(60, Math.min(w, h) / 2 - 90);
  // Keep the ring clear of the legend (top-left, ~470 px) when there is room.
  const legendW = 470;
  let cx = w / 2;
  if (cx - r - 70 < legendW && w - legendW >= 2 * r + 140) cx = legendW + (w - legendW) / 2;
  const nodes = Object.values(snap.nodes).sort((a, b) => a.name.localeCompare(b.name, undefined, { numeric: true }));
  const placed = nodes.map((n, i) => {
    const oa = n.overlay ? overlayAngle(n.overlay) : null;
    return { name: n.name, a: oa ?? (i / Math.max(1, nodes.length)) * Math.PI * 2 - Math.PI / 2 };
  });
  spreadAngles(placed, Math.min((Math.PI * 2) / Math.max(1, placed.length), 0.45) * 0.8);
  for (const p of placed) pos.set(p.name, { x: cx + r * Math.cos(p.a), y: cy + r * Math.sin(p.a) });
  extNames.forEach((name, i) => {
    const a = overlayAngle(name) ?? (i / Math.max(1, extNames.length)) * Math.PI * 2;
    ext.set(name, { x: cx + (r + 55) * Math.cos(a), y: cy + (r + 55) * Math.sin(a) });
  });
  return { pos, ext, cx, cy, r };
}

function nodeActivity(n: NodeState): number {
  let a = 0;
  for (const c of Object.values(n.components ?? {})) a += c.activity || 0;
  return a;
}

function distToSeg(p: P, a: P, b: P): number {
  const dx = b.x - a.x;
  const dy = b.y - a.y;
  const l2 = dx * dx + dy * dy || 1;
  const t = Math.max(0, Math.min(1, ((p.x - a.x) * dx + (p.y - a.y) * dy) / l2));
  return Math.hypot(p.x - (a.x + t * dx), p.y - (a.y + t * dy));
}

export function TopologyView() {
  const view = useView();
  const live = useIsLive();
  const selection = useStore((s) => s.selection);
  const select = useStore((s) => s.select);
  const reduced = useReducedMotion();
  const wrap = useRef<HTMLDivElement>(null);
  const canvas = useRef<HTMLCanvasElement>(null);
  const [size, setSize] = useState({ w: 800, h: 600 });
  const [hover, setHover] = useState<{ x: number; y: number; text: string[] } | null>(null);
  const particles = useRef<Particle[]>([]);
  const extSeen = useRef<Map<string, number>>(new Map()); // ext peer -> last seen ms
  const edgeCounts = useRef<Map<string, { n: number; err: number; proto: string }>>(new Map());
  const [extVersion, setExtVersion] = useState(0);
  // When the view is not live, flows come from history around the view time.
  const pastCounts = useRef<Map<string, { n: number; err: number; proto: string }> | null>(null);
  const [legendOpen, setLegendOpen] = useState(() => {
    try {
      const v = localStorage.getItem("bee-scope.legend");
      if (v !== null) return v !== "0";
    } catch {
      /* storage unavailable */
    }
    // The legend covers most of a phone-sized ring, so start it closed there.
    return window.innerWidth >= 700;
  });

  useEffect(() => {
    const el = wrap.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setSize({ w: el.clientWidth, h: el.clientHeight }));
    ro.observe(el);
    setSize({ w: el.clientWidth, h: el.clientHeight });
    return () => ro.disconnect();
  }, []);

  // Flow frames → particles (never React state).
  const liveRef = useRef(live);
  liveRef.current = live;
  const reducedRef = useRef(reduced);
  reducedRef.current = reduced;
  useEffect(() => {
    const onFrame = (f: { flows: Flow[] }) => {
      const now = performance.now();
      let newExt = false;
      for (const fl of f.flows) {
        for (const end of [fl.from, fl.to]) {
          if (end.startsWith("ext:")) {
            if (!extSeen.current.has(end)) newExt = true;
            extSeen.current.set(end, Date.now());
          }
        }
        const key = fl.from + ">" + fl.to;
        const ec = edgeCounts.current.get(key) ?? { n: 0, err: 0, proto: fl.protocol };
        ec.n += fl.count;
        ec.err += fl.errors;
        ec.proto = fl.protocol;
        edgeCounts.current.set(key, ec);
        if (!liveRef.current || reducedRef.current) continue;
        const ok = Math.max(0, fl.count - fl.errors);
        const spawn = (k: number, err: boolean) => {
          for (let i = 0; i < Math.min(k, 6); i++) {
            particles.current.push({
              from: fl.from,
              to: fl.to,
              color: err ? C.bad : (protoColor[fl.protocol] ?? C.active),
              start: now + Math.random() * 450,
              dur: 900 + Math.random() * 300,
              err,
            });
          }
        };
        spawn(ok, false);
        spawn(fl.errors, true);
      }
      if (particles.current.length > 1500) particles.current.splice(0, particles.current.length - 1500);
      if (newExt) setExtVersion((v) => v + 1);
    };
    const unsub = flowBus.subscribe(onFrame);
    // Decay static edge counts (shown with reduced motion and in tooltips).
    const t = setInterval(() => {
      for (const [k, v] of edgeCounts.current) {
        v.n *= 0.7;
        v.err *= 0.7;
        if (v.n < 0.05) edgeCounts.current.delete(k);
      }
      const cutoff = Date.now() - 120_000;
      let removed = false;
      for (const [k, t2] of extSeen.current) {
        if (t2 < cutoff) {
          extSeen.current.delete(k);
          removed = true;
        }
      }
      if (removed) setExtVersion((v) => v + 1);
    }, 1000);
    return () => {
      unsub();
      clearInterval(t);
    };
  }, []);

  const viewAt = view ? Date.parse(view.at) : 0;
  useEffect(() => {
    if (live || !viewAt) {
      pastCounts.current = null;
      return;
    }
    const ac = new AbortController();
    api
      .flows(viewAt - 15_000, viewAt, ac.signal)
      .then((frames) => {
        const m = new Map<string, { n: number; err: number; proto: string }>();
        for (const f of frames)
          for (const fl of f.flows) {
            const k = fl.from + ">" + fl.to;
            const v = m.get(k) ?? { n: 0, err: 0, proto: fl.protocol };
            v.n += fl.count;
            v.err += fl.errors;
            m.set(k, v);
          }
        pastCounts.current = m;
      })
      .catch(() => undefined);
    return () => ac.abort();
  }, [live, viewAt]);

  const extNames = useMemo(() => [...extSeen.current.keys()].sort(), [extVersion]); // eslint-disable-line react-hooks/exhaustive-deps
  const geo = useMemo(() => layout(view, size.w, size.h, extNames), [view, size.w, size.h, extNames]);
  const state = useRef({ view, geo, selection });
  state.current = { view, geo, selection };

  // Render loop.
  useEffect(() => {
    let raf = 0;
    const draw = () => {
      raf = requestAnimationFrame(draw);
      const c = canvas.current;
      if (!c) return;
      const dpr = window.devicePixelRatio || 1;
      if (c.width !== size.w * dpr || c.height !== size.h * dpr) {
        c.width = size.w * dpr;
        c.height = size.h * dpr;
        c.style.width = size.w + "px";
        c.style.height = size.h + "px";
      }
      const g = c.getContext("2d");
      if (!g) return;
      g.setTransform(dpr, 0, 0, dpr, 0, 0);
      g.clearRect(0, 0, size.w, size.h);
      const { view: snap, geo: L, selection: sel } = state.current;
      if (!snap) {
        g.fillStyle = C.muted;
        g.font = "14px system-ui";
        g.textAlign = "center";
        g.fillText("Waiting for the first snapshot…", size.w / 2, size.h / 2);
        return;
      }
      const t = performance.now();
      const viewMs = Date.parse(snap.at);

      // ring
      g.strokeStyle = "rgba(140,150,170,0.18)";
      g.setLineDash([2, 6]);
      g.beginPath();
      g.arc(L.cx, L.cy, L.r, 0, Math.PI * 2);
      g.stroke();
      g.setLineDash([]);
      g.fillStyle = "rgba(140,150,170,0.45)";
      g.font = "10px system-ui";
      g.textAlign = "center";
      g.fillText("0x0000", L.cx, L.cy - L.r - 8);
      g.fillText("0x8000", L.cx, L.cy + L.r + 16);

      // edges
      for (const e of snap.edges ?? []) {
        const a = L.pos.get(e.a);
        const b = L.pos.get(e.b);
        if (!a || !b) continue;
        const stale = viewMs - Date.parse(e.freshAt) > 15_000;
        const oneSided = e.seenByA !== e.seenByB;
        const isSel = sel?.type === "edge" && ((sel.a === e.a && sel.b === e.b) || (sel.a === e.b && sel.b === e.a));
        g.lineWidth = isSel ? 3 : e.latencyMs > 300 ? 2 : 1.2;
        g.strokeStyle = isSel ? C.sel : !e.healthy ? C.warn : e.latencyMs > 300 ? "#8a7a4a" : C.edge;
        g.setLineDash(oneSided || stale ? [5, 5] : e.prov === "simulated" ? [1, 0] : []);
        g.beginPath();
        g.moveTo(a.x, a.y);
        g.lineTo(b.x, b.y);
        g.stroke();
        g.setLineDash([]);
        if (!e.healthy) {
          const mx = (a.x + b.x) / 2;
          const my = (a.y + b.y) / 2;
          g.fillStyle = C.warn;
          g.font = "11px system-ui";
          g.fillText("▲", mx, my + 4);
        }
      }

      // static counts (reduced motion) or when not live
      if (reducedRef.current || !liveRef.current) {
        g.font = "10px system-ui";
        const counts = !liveRef.current && pastCounts.current ? pastCounts.current : edgeCounts.current;
        for (const [k, v] of counts) {
          if (v.n < 0.5) continue;
          const [f, to] = k.split(">");
          const a = L.pos.get(f) ?? L.ext.get(f);
          const b = L.pos.get(to) ?? L.ext.get(to);
          if (!a || !b) continue;
          g.fillStyle = v.err >= 0.5 ? C.bad : (protoColor[v.proto] ?? C.active);
          g.fillText(`${v.proto} ${Math.round(v.n)}${v.err >= 0.5 ? ` (${Math.round(v.err)}✕)` : ""} →`, a.x + (b.x - a.x) * 0.35, a.y + (b.y - a.y) * 0.35 - 4);
        }
      }

      // external peers
      for (const [name, p] of L.ext) {
        g.fillStyle = "rgba(140,150,170,0.6)";
        g.beginPath();
        g.arc(p.x, p.y, 3.5, 0, Math.PI * 2);
        g.fill();
        g.fillStyle = C.muted;
        g.font = "9px ui-monospace, monospace";
        g.fillText(name.replace("ext:", ""), p.x, p.y - 7);
      }

      // particles
      const alive: Particle[] = [];
      for (const pt of particles.current) {
        const k = (t - pt.start) / pt.dur;
        if (k > 1) continue;
        alive.push(pt);
        if (k < 0) continue;
        const a = L.pos.get(pt.from) ?? L.ext.get(pt.from);
        const b = L.pos.get(pt.to) ?? L.ext.get(pt.to);
        if (!a || !b) continue;
        const x = a.x + (b.x - a.x) * k;
        const y = a.y + (b.y - a.y) * k;
        g.fillStyle = pt.color;
        if (pt.err) {
          g.fillRect(x - 3, y - 3, 6, 6); // errors are squares, not just red
        } else {
          g.beginPath();
          g.arc(x, y, 3, 0, Math.PI * 2);
          g.fill();
        }
      }
      particles.current = alive;

      // nodes
      for (const n of Object.values(snap.nodes)) {
        const p = L.pos.get(n.name);
        if (!p) continue;
        const col = phaseColor[n.phase] ?? C.idle;
        const act = nodeActivity(n);
        const rad = n.group === "bootnode" ? 15 : 13;
        if (act > 0.05 && n.phase === "up" && !reducedRef.current && liveRef.current) {
          const k = (t / 1200) % 1;
          g.strokeStyle = `rgba(90,169,255,${0.5 * (1 - k)})`;
          g.lineWidth = 2;
          g.beginPath();
          g.arc(p.x, p.y, rad + 4 + k * 14 * Math.min(1, 0.3 + Math.log10(1 + act) / 2), 0, Math.PI * 2);
          g.stroke();
        }
        const isSel = (sel?.type === "node" || sel?.type === "component") && sel.node === n.name;
        g.fillStyle = C.bg;
        g.strokeStyle = col;
        g.lineWidth = isSel ? 4 : 2.5;
        g.beginPath();
        if (n.group === "bootnode") {
          for (let i = 0; i < 6; i++) {
            const a = (Math.PI / 3) * i - Math.PI / 6;
            const fn = i === 0 ? g.moveTo.bind(g) : g.lineTo.bind(g);
            fn(p.x + rad * Math.cos(a), p.y + rad * Math.sin(a));
          }
          g.closePath();
        } else {
          g.arc(p.x, p.y, rad, 0, Math.PI * 2);
        }
        g.fill();
        if (n.phase === "unknown" || n.phase === "gone") g.setLineDash([3, 3]);
        g.stroke();
        g.setLineDash([]);
        if (isSel) {
          g.strokeStyle = C.sel;
          g.lineWidth = 1;
          g.beginPath();
          g.arc(p.x, p.y, rad + 6, 0, Math.PI * 2);
          g.stroke();
        }
        g.fillStyle = col;
        g.font = "bold 12px system-ui";
        g.textAlign = "center";
        g.fillText(phaseLook[n.phase]?.icon ?? "?", p.x, p.y + 4);
        g.fillStyle = C.text;
        g.font = "12px system-ui";
        g.fillText(n.name, p.x, p.y + rad + 15);
        g.fillStyle = C.muted;
        g.font = "10px system-ui";
        const sub = n.phase === "up" ? `r${n.status?.storageRadius ?? "?"} · ${n.connected} peers` : (phaseLook[n.phase]?.label ?? n.phase);
        g.fillText(sub, p.x, p.y + rad + 27);
        if (n.issueCount > 0) {
          const bx = p.x + rad - 1;
          const by = p.y - rad + 1;
          g.fillStyle = n.worst === "error" ? C.bad : n.worst === "warn" ? C.warn : C.idle;
          g.beginPath();
          g.arc(bx, by, 7, 0, Math.PI * 2);
          g.fill();
          g.fillStyle = "#000";
          g.font = "bold 9px system-ui";
          g.fillText(String(n.issueCount), bx, by + 3);
        }
      }
    };
    raf = requestAnimationFrame(draw);
    return () => cancelAnimationFrame(raf);
  }, [size.w, size.h]);

  const hit = (x: number, y: number): { node?: NodeState; edge?: Edge; ext?: string } => {
    const { view: snap, geo: L } = state.current;
    if (!snap) return {};
    for (const n of Object.values(snap.nodes)) {
      const p = L.pos.get(n.name);
      if (p && Math.hypot(p.x - x, p.y - y) < 22) return { node: n };
    }
    for (const [name, p] of L.ext) if (Math.hypot(p.x - x, p.y - y) < 10) return { ext: name };
    for (const e of snap.edges ?? []) {
      const a = L.pos.get(e.a);
      const b = L.pos.get(e.b);
      if (a && b && distToSeg({ x, y }, a, b) < 7) return { edge: e };
    }
    return {};
  };

  const pt = (ev: React.MouseEvent) => {
    const r = canvas.current!.getBoundingClientRect();
    return { x: ev.clientX - r.left, y: ev.clientY - r.top };
  };

  const nodes = view ? Object.values(view.nodes) : [];
  const counts = { up: 0, other: 0 };
  for (const n of nodes) n.phase === "up" ? counts.up++ : counts.other++;

  return (
    <div className="view topo" ref={wrap}>
      <canvas
        ref={canvas}
        className={hover ? "over" : ""}
        onMouseMove={(ev) => {
          const { x, y } = pt(ev);
          const h = hit(x, y);
          if (h.node) {
            const n = h.node;
            setHover({
              x,
              y,
              text: [
                `${n.name} · ${phaseLook[n.phase]?.label ?? n.phase}`,
                n.overlay ? `overlay ${short(n.overlay, 12)}` : "overlay unknown",
                n.status ? `radius ${n.status.storageRadius} · depth ${n.depth} · reserve ${num(n.status.reserveSizeWithinRadius)}` : "no /status yet",
                `${n.connected} connected (${n.externalPeers} outside cluster) · ${n.unhealthyPeers} unhealthy`,
                n.issueCount ? `${n.issueCount} open issue(s)` : "no open issues",
              ],
            });
          } else if (h.edge) {
            const e = h.edge;
            const fwd = edgeCounts.current.get(e.a + ">" + e.b);
            const back = edgeCounts.current.get(e.b + ">" + e.a);
            setHover({
              x,
              y,
              text: [
                `${e.a} ↔ ${e.b}`,
                `bin ${e.bin} · latency ${e.latencyMs.toFixed(0)} ms · ${e.healthy ? "healthy" : "▲ unhealthy"}`,
                e.seenByA && e.seenByB ? "seen by both sides" : `one-sided: only ${e.seenByA ? e.a : e.b} lists the other`,
                fwd || back ? `recent flows: ${fwd ? `${e.a}→${e.b} ${Math.round(fwd.n)}` : ""} ${back ? `${e.b}→${e.a} ${Math.round(back.n)}` : ""}` : "no per-peer flows observed recently",
                `source GET /topology · ${e.prov}`,
              ],
            });
          } else if (h.ext) {
            setHover({ x, y, text: [`${h.ext}`, "peer outside this cluster (seen in flows)"] });
          } else setHover(null);
        }}
        onMouseLeave={() => setHover(null)}
        onClick={(ev) => {
          const { x, y } = pt(ev);
          const h = hit(x, y);
          if (h.node) select({ type: "node", node: h.node.name });
          else if (h.edge) select({ type: "edge", a: h.edge.a, b: h.edge.b });
          else if (h.ext) select({ type: "peer", address: h.ext.replace("ext:", "") });
        }}
      />
      {hover && (
        <div className="tooltip" style={{ left: Math.max(4, Math.min(hover.x + 14, size.w - 300)), top: hover.y + 14 }}>
          {hover.text.map((l, i) => (
            <div key={i} className={i === 0 ? "tt-title" : ""}>
              {l}
            </div>
          ))}
          <div className="tt-hint">click to inspect</div>
        </div>
      )}
      {!selection && nodes.length > 0 && <div className="topo-hint">Click a node or link to inspect it</div>}
      <div className={"topo-legend" + (legendOpen ? "" : " closed")}>
        <button
          className="legend-toggle"
          onClick={() => {
            setLegendOpen(!legendOpen);
            try {
              localStorage.setItem("bee-scope.legend", legendOpen ? "0" : "1");
            } catch {
              /* storage unavailable */
            }
          }}
          title={legendOpen ? "hide legend" : "show legend"}
        >
          {legendOpen ? "▾" : "▸ legend"}
        </button>
        <div>
          <b>
            {counts.up}/{nodes.length}
          </b>{" "}
          nodes up · {(view?.edges ?? []).length} links
          {view?.networkRadius !== undefined && <> · cluster radius {view.networkRadius}</>}
        </div>
        <div>
          <span style={{ color: C.ok }}>●</span> up <span style={{ color: C.warn }}>◐</span> gated <span style={{ color: C.bad }}>✕</span> unreachable{" "}
          <span style={{ color: C.idle }}>?</span> unknown · ⬡ bootnode
        </div>
        <div>
          — link from /topology · <span className="dash">- -</span> one-sided or stale · <span style={{ color: C.warn }}>▲</span> unhealthy
        </div>
        <div>
          {Object.entries(protoColor).map(([k, v]) => (
            <span key={k} style={{ marginRight: 8 }}>
              <span style={{ color: v }}>●</span> {k}
            </span>
          ))}
          <span style={{ color: C.bad }}>■</span> failed
        </div>
        <div className="muted">
          {reduced
            ? "Reduced motion: flows are shown as counts."
            : live
              ? "Dots travel only for observed per-peer events (logs/spans). Pulses are metric activity."
              : "Not live: per-edge flows of the 15 s before the view time, as counts."}
        </div>
      </div>
    </div>
  );
}
