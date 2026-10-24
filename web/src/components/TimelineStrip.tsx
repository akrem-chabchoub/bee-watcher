import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "../api";
import { fmtTime } from "../format";
import { useStore } from "../store";
import type { Bucket, HistoryWindow } from "../types";

const SEVS = ["error", "warn", "info", "debug"] as const;
const SEV_COLORS: Record<string, string> = { error: "#ff5d5d", warn: "#f5b301", info: "#5aa9ff", debug: "#5b6577" };

/**
 * The bottom strip: event density over the whole history window, the time
 * cursor and the brush that sets the Timeline view's range. Click or drag to
 * scrub; shift-drag to select a range.
 */
export function TimelineStrip() {
  const cursor = useStore((s) => s.cursor);
  const setCursor = useStore((s) => s.setCursor);
  const goLive = useStore((s) => s.goLive);
  const span = useStore((s) => s.timelineSpanMs);
  const setSpan = useStore((s) => s.setTimelineSpan);
  const gap = useStore((s) => s.gap);
  const keyframeError = useStore((s) => s.keyframeError);
  const [win, setWin] = useState<HistoryWindow | null>(null);
  const [buckets, setBuckets] = useState<Bucket[]>([]);
  const [width, setWidth] = useState(800);
  const [drag, setDrag] = useState<{ x0: number; x1: number; brush: boolean } | null>(null);
  const wrap = useRef<HTMLDivElement>(null);
  const canvas = useRef<HTMLCanvasElement>(null);

  // Range shown: the whole retained window, at least 2 minutes.
  const now = Date.now();
  const to = win && !isNaN(Date.parse(win.to)) ? Math.max(Date.parse(win.to), now - 1000) : now;
  const fromRaw = win && !isNaN(Date.parse(win.from)) && Date.parse(win.from) > 0 ? Date.parse(win.from) : to - 120_000;
  const from = Math.min(fromRaw, to - 120_000);

  useEffect(() => {
    const el = wrap.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setWidth(el.clientWidth));
    ro.observe(el);
    setWidth(el.clientWidth);
    return () => ro.disconnect();
  }, []);

  const load = useCallback(() => {
    const ac = new AbortController();
    api
      .window(ac.signal)
      .then((w) => {
        setWin(w);
        const t1 = Math.max(Date.parse(w.to) || Date.now(), Date.now() - 1000);
        const f = Date.parse(w.from);
        const t0 = Math.min(f > 0 ? f : t1 - 120_000, t1 - 120_000);
        const n = Math.max(20, Math.min(600, Math.floor(width / 3)));
        return api.density(t0, t1, n, {}, ac.signal);
      })
      .then(setBuckets)
      .catch(() => undefined);
    return ac;
  }, [width]);

  useEffect(() => {
    let ac = load();
    const t = setInterval(() => {
      ac.abort();
      ac = load();
    }, 4000);
    return () => {
      clearInterval(t);
      ac.abort();
    };
  }, [load]);

  const xToT = (x: number) => from + (x / width) * (to - from);
  const tToX = (t: number) => ((t - from) / (to - from)) * width;

  useEffect(() => {
    const c = canvas.current;
    if (!c) return;
    const dpr = window.devicePixelRatio || 1;
    const h = 44;
    c.width = width * dpr;
    c.height = h * dpr;
    c.style.width = width + "px";
    c.style.height = h + "px";
    const g = c.getContext("2d");
    if (!g) return;
    g.setTransform(dpr, 0, 0, dpr, 0, 0);
    g.clearRect(0, 0, width, h);
    let max = 1;
    for (const b of buckets) {
      let s = 0;
      for (const k of SEVS) s += b.count[k] ?? 0;
      max = Math.max(max, s);
    }
    const bw = buckets.length ? width / buckets.length : 0;
    buckets.forEach((b, i) => {
      let y = h;
      for (const k of SEVS) {
        const v = b.count[k] ?? 0;
        if (!v) continue;
        // sqrt scale keeps rare errors visible next to info bursts
        const bh = Math.max(1.5, (Math.sqrt(v) / Math.sqrt(max)) * (h - 4));
        g.fillStyle = SEV_COLORS[k];
        g.fillRect(i * bw, y - bh, Math.max(1, bw - 0.5), bh);
        y -= bh;
      }
    });
  }, [buckets, width]);

  const rel = (e: React.PointerEvent) => {
    const r = wrap.current!.getBoundingClientRect();
    return Math.max(0, Math.min(width, e.clientX - r.left));
  };

  const cursorX = cursor !== null ? tToX(cursor) : null;
  const viewEnd = cursor ?? to;
  const brushX0 = tToX(viewEnd - span);
  const brushX1 = tToX(viewEnd);

  return (
    <footer className="strip">
      <div className="strip-head">
        <span className="muted">History</span>
        <span>
          {fmtTime(from)} – {fmtTime(to)}
        </span>
        <span className="legend">
          {SEVS.map((k) => (
            <span key={k}>
              <i style={{ background: SEV_COLORS[k] }} />
              {k}
            </span>
          ))}
        </span>
        <span className="muted">click/drag to scrub · shift-drag to set the Timeline range</span>
        {gap && (
          <span className="warn-text" title="Events older than the retained history were dropped while the stream was disconnected">
            ⚠ stream gap: events {gap.requested + 1}–{gap.oldest - 1} were evicted
          </span>
        )}
        {keyframeError && <span className="warn-text">⚠ keyframe: {keyframeError}</span>}
        <div className="spacer" />
        <label className="muted">
          range{" "}
          <select value={span} onChange={(e) => setSpan(Number(e.target.value))}>
            {[60_000, 2 * 60_000, 5 * 60_000, 10 * 60_000, 30 * 60_000].map((v) => (
              <option key={v} value={v}>
                {v / 60_000} min
              </option>
            ))}
          </select>
        </label>
        {cursor !== null && (
          <button className="btn" onClick={goLive}>
            ⏭ Live
          </button>
        )}
      </div>
      <div
        className="strip-track"
        ref={wrap}
        onPointerDown={(e) => {
          (e.target as HTMLElement).setPointerCapture(e.pointerId);
          const x = rel(e);
          setDrag({ x0: x, x1: x, brush: e.shiftKey });
          if (!e.shiftKey) setCursor(xToT(x));
        }}
        onPointerMove={(e) => {
          if (!drag) return;
          const x = rel(e);
          setDrag({ ...drag, x1: x });
          if (!drag.brush) setCursor(xToT(x));
        }}
        onPointerUp={() => {
          if (drag?.brush && Math.abs(drag.x1 - drag.x0) > 4) {
            const a = xToT(Math.min(drag.x0, drag.x1));
            const b = xToT(Math.max(drag.x0, drag.x1));
            setSpan(Math.max(10_000, b - a));
            setCursor(b);
          }
          setDrag(null);
        }}
      >
        <canvas ref={canvas} />
        <div className="brush" style={{ left: Math.max(0, brushX0), width: Math.max(2, brushX1 - Math.max(0, brushX0)) }} title="Timeline view range" />
        {drag?.brush && <div className="brush sel" style={{ left: Math.min(drag.x0, drag.x1), width: Math.abs(drag.x1 - drag.x0) }} />}
        {cursorX !== null && (
          <div className="cursor" style={{ left: cursorX }}>
            <span>{fmtTime(cursor)}</span>
          </div>
        )}
      </div>
    </footer>
  );
}
