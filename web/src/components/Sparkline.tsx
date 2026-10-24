import type { Pt } from "../types";
import { num } from "../format";

export function Sparkline({ pts, label, w = 150, h = 28 }: { pts: Pt[]; label: string; w?: number; h?: number }) {
  if (!pts.length) return null;
  const xs = pts.map((p) => p[0]);
  const ys = pts.map((p) => p[1]);
  const x0 = Math.min(...xs);
  const x1 = Math.max(...xs);
  const y0 = Math.min(...ys);
  const y1 = Math.max(...ys);
  const sx = (x: number) => (x1 === x0 ? w : ((x - x0) / (x1 - x0)) * w);
  const sy = (y: number) => (y1 === y0 ? h / 2 : h - 2 - ((y - y0) / (y1 - y0)) * (h - 4));
  const d = pts.map((p, i) => `${i ? "L" : "M"}${sx(p[0]).toFixed(1)},${sy(p[1]).toFixed(1)}`).join("");
  return (
    <div className="spark" title={`${label}: min ${num(y0)} max ${num(y1)} over ${Math.round((x1 - x0) / 1000)} s`}>
      <span className="spark-label">{label}</span>
      <svg width={w} height={h}>
        <path d={d} fill="none" stroke="#5aa9ff" strokeWidth={1.3} />
      </svg>
      <span className="spark-val">{num(ys[ys.length - 1])}</span>
    </div>
  );
}
