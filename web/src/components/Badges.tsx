import { useEffect, useState } from "react";
import { ago, classLook, isZeroTime, phaseLook, provLook, sevLook, statusLook, type Look } from "../format";
import type { ComponentStatus, IssueClass, Phase, Provenance, Severity, Source } from "../types";

function Badge({ look, title, compact, term }: { look: Look; title?: string; compact?: boolean; term?: string }) {
  return (
    <span className="badge" style={{ ["--c" as string]: look.color }} title={title ?? look.label} data-term={term}>
      <span className="badge-icon" aria-hidden>
        {look.icon}
      </span>
      {!compact && <span>{look.label}</span>}
    </span>
  );
}

export const ProvBadge = ({ prov, compact }: { prov: Provenance; compact?: boolean }) => (
  <Badge look={provLook[prov] ?? provLook.observed} compact={compact} title={`provenance: ${prov} (right-click to explain)`} term="provenance" />
);
export const SevBadge = ({ sev, compact }: { sev: Severity; compact?: boolean }) => (
  <Badge look={sevLook[sev] ?? sevLook.info} compact={compact} title={`severity: ${sev}`} />
);
export const StatusBadge = ({ status }: { status: ComponentStatus }) => <Badge look={statusLook[status] ?? statusLook.unknown} />;
const phaseTerm: Record<string, string> = { up: "phase-up", gated: "phase-gated", unreachable: "phase-unreachable" };
export const PhaseBadge = ({ phase }: { phase: Phase }) => <Badge look={phaseLook[phase] ?? phaseLook.unknown} term={phaseTerm[phase]} />;
export const ClassBadge = ({ cls }: { cls: IssueClass }) => <Badge look={classLook[cls] ?? classLook.hypothesis} term="issue-class" />;

export function SourceTag({ source }: { source: Source }) {
  return (
    <span className="source-tag" title={`source: ${source.adapter} · ${source.ref}`}>
      {source.adapter}
      {source.ref ? <> · {source.ref}</> : null}
    </span>
  );
}

/** Re-renders every second so "x s ago" labels stay current. */
export function useNow(intervalMs = 1000): number {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(t);
  }, [intervalMs]);
  return now;
}

export function Fresh({ at, stale, now }: { at: string | undefined; stale?: boolean; now?: number }) {
  const n = useNow();
  const zero = isZeroTime(at);
  return (
    <span className={"fresh" + (stale ? " stale" : "")} title={zero ? "no data yet" : `fresh at ${at}`}>
      {stale ? "⏸ stale · " : ""}
      {zero ? "no data" : ago(at, now ?? n)}
    </span>
  );
}
