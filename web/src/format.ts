import type { ComponentStatus, IssueClass, Phase, Provenance, Severity } from "./types";

// Visual language: every state has a color, an icon and a label, so the UI
// never relies on color alone.

export interface Look {
  color: string; // CSS variable
  icon: string;
  label: string;
}

export const phaseLook: Record<Phase, Look> = {
  up: { color: "var(--ok)", icon: "●", label: "up" },
  gated: { color: "var(--warn)", icon: "◐", label: "gated (503)" },
  unreachable: { color: "var(--bad)", icon: "✕", label: "unreachable" },
  gone: { color: "var(--bad)", icon: "∅", label: "gone" },
  unknown: { color: "var(--idle)", icon: "?", label: "unknown" },
};

export const statusLook: Record<ComponentStatus, Look> = {
  active: { color: "var(--active)", icon: "▶", label: "active" },
  idle: { color: "var(--idle)", icon: "○", label: "idle" },
  degraded: { color: "var(--warn)", icon: "▲", label: "degraded" },
  failed: { color: "var(--bad)", icon: "✕", label: "failed" },
  unobservable: { color: "var(--muted)", icon: "⊘", label: "unobservable" },
  unknown: { color: "var(--idle)", icon: "?", label: "unknown" },
};

export const sevLook: Record<Severity, Look> = {
  debug: { color: "var(--idle)", icon: "·", label: "debug" },
  info: { color: "var(--active)", icon: "●", label: "info" },
  warn: { color: "var(--warn)", icon: "▲", label: "warn" },
  error: { color: "var(--bad)", icon: "■", label: "error" },
};

export const classLook: Record<IssueClass, Look> = {
  confirmed: { color: "var(--bad)", icon: "✔", label: "confirmed" },
  suspicious: { color: "var(--warn)", icon: "?", label: "suspicious" },
  hypothesis: { color: "var(--idle)", icon: "~", label: "hypothesis" },
};

export const provLook: Record<Provenance, Look> = {
  observed: { color: "var(--ok)", icon: "◉", label: "observed" },
  derived: { color: "var(--active)", icon: "∆", label: "derived" },
  inferred: { color: "var(--warn)", icon: "≈", label: "inferred" },
  simulated: { color: "var(--sim)", icon: "⚙", label: "simulated" },
};

export const protoColor: Record<string, string> = {
  pushsync: "#5aa9ff",
  retrieval: "#3ddc97",
  pullsync: "#c58cff",
  hive: "#ffcc66",
};

export const sevRank: Record<Severity, number> = { debug: 0, info: 1, warn: 2, error: 3 };

export function fmtTime(t: string | number | undefined | null, withMs = false): string {
  if (t === undefined || t === null || t === "") return "—";
  const d = typeof t === "number" ? new Date(t) : new Date(t);
  if (isNaN(d.getTime()) || d.getFullYear() < 2000) return "—";
  const s = d.toLocaleTimeString([], { hour12: false });
  return withMs ? `${s}.${String(d.getMilliseconds()).padStart(3, "0")}` : s;
}

export function ago(t: string | number | undefined | null, now = Date.now()): string {
  if (t === undefined || t === null || t === "") return "never";
  const ms = now - (typeof t === "number" ? t : Date.parse(t));
  if (isNaN(ms) || ms > 1e12) return "never";
  return dur(Math.max(0, ms)) + " ago";
}

export function dur(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)} ms`;
  const s = ms / 1000;
  if (s < 60) return `${s < 10 ? s.toFixed(1) : Math.round(s)} s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${Math.round(s % 60)}s`;
  return `${Math.floor(m / 60)}h ${m % 60}m`;
}

export function num(v: number | undefined | null, digits = 2): string {
  if (v === undefined || v === null || isNaN(v)) return "—";
  if (Math.abs(v) >= 1e6) return (v / 1e6).toFixed(1) + "M";
  if (Math.abs(v) >= 1e4) return (v / 1e3).toFixed(1) + "k";
  if (Number.isInteger(v)) return String(v);
  return v.toFixed(digits);
}

export function short(a: string | undefined | null, n = 8): string {
  if (!a) return "";
  return a.length > n ? a.slice(0, n) + "…" : a;
}

export function isZeroTime(t: string | undefined | null): boolean {
  if (!t) return true;
  const ms = Date.parse(t);
  return isNaN(ms) || new Date(ms).getFullYear() < 2000;
}

export function parseTime(t: string | undefined | null): number {
  if (!t) return NaN;
  return Date.parse(t);
}
