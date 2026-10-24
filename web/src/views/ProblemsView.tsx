import { useState } from "react";
import { api } from "../api";
import { useStore, useView } from "../store";
import { ago, fmtTime } from "../format";
import { ClassBadge, ProvBadge, SevBadge, SourceTag, useNow } from "../components/Badges";
import type { Issue } from "../types";
import { Term } from "../components/Explain";

/** Replaces embedded Bee / kube JSON error bodies ({"code":503,"message":"…"}) with their message. */
function tidyEvidence(t: string): string {
  return t.replace(/\{"(?:kind|code|message)"[\s\S]*?"message":"((?:[^"\\]|\\.)*)"[\s\S]*?\}(?=\s|$)/g, (_, m: string) => `“${m.replace(/\\(.)/g, "$1")}”`);
}

export function issueMarkdown(is: Issue, target: string): string {
  const lines = [
    `### ${is.title}`,
    ``,
    `- **Class / severity:** ${is.class} / ${is.sev}${is.simulated ? " (SIMULATED — not real telemetry)" : ""}`,
    `- **Rule:** ${is.rule}`,
    `- **Where:** ${is.where} (cluster ${target})`,
    `- **Since:** ${is.startedAt}${is.resolvedAt ? ` — resolved ${is.resolvedAt}` : " — still open"}`,
    `- **Nodes:** ${(is.nodes ?? []).join(", ") || "—"}; **components:** ${(is.components ?? []).join(", ") || "—"}`,
    ``,
    `**What:** ${is.what}`,
    ``,
    `**Evidence:**`,
    ...(is.evidence ?? []).map((e) => `- [${e.prov}, ${e.source.adapter} ${e.source.ref}, ${e.at}${e.node ? `, ${e.node}` : ""}] ${e.text}`),
  ];
  if (is.hypothesis) lines.push(``, `**Hypothesis:** ${is.hypothesis}`);
  if (is.nextSteps?.length) lines.push(``, `**Next steps:**`, ...is.nextSteps.map((s) => `1. ${s}`));
  return lines.join("\n");
}

export function jumpToIssue(is: Issue) {
  const st = useStore.getState();
  const start = Date.parse(is.startedAt);
  st.setTimelineSpan(5 * 60_000);
  st.setCursor(Math.min(start + 2.5 * 60_000, Date.now()));
  st.setTab("timeline");
}

export function openEvidenceEvent(seq: number) {
  const st = useStore.getState();
  st.select({ type: "event", seq });
  api
    .event(seq)
    .then((e) => useStore.getState().setCursor(Date.parse(e.t)))
    .catch(() => undefined);
}

export function IssueCard({ is, target }: { is: Issue; target: string }) {
  const select = useStore((s) => s.select);
  const selection = useStore((s) => s.selection);
  const [copied, setCopied] = useState(false);
  const now = useNow();
  const resolved = !!is.resolvedAt;
  const sel = selection?.type === "issue" && selection.id === is.id;
  return (
    <article className={"issue sev-" + is.sev + (resolved ? " resolved" : "") + (sel ? " on" : "") + " class-" + is.class}>
      <header onClick={() => select({ type: "issue", id: is.id })}>
        <ClassBadge cls={is.class} />
        <SevBadge sev={is.sev} />
        {resolved && <span className="badge">✔ resolved {ago(is.resolvedAt, now)}</span>}
        {is.simulated && <span className="badge sim">⚙ simulated</span>}
        <h3>{is.title}</h3>
      </header>
      <dl>
        <dt>What</dt>
        <dd>{is.what}</dd>
        <dt>Where</dt>
        <dd>
          {is.where}
          {(is.components ?? []).length > 0 && <span className="muted"> · {(is.components ?? []).join(", ")}</span>}
        </dd>
        <dt>Since</dt>
        <dd>
          {fmtTime(is.startedAt)} ({ago(is.startedAt, now)}) · last seen {fmtTime(is.lastSeenAt)}
        </dd>
        <dt>Evidence</dt>
        <dd>
          <ul className="evidence">
            {(is.evidence ?? []).map((e, i) => (
              <li key={i}>
                <ProvBadge prov={e.prov} compact /> <SourceTag source={e.source} />{" "}
                {e.eventSeq ? (
                  <a href="#" onClick={(ev) => (ev.preventDefault(), openEvidenceEvent(e.eventSeq!))} title="open the event and move the time cursor">
                    {e.text}
                  </a>
                ) : (
                  <span className="ev-text">{tidyEvidence(e.text)}</span>
                )}{" "}
                <span className="muted">{fmtTime(e.at)}</span>
              </li>
            ))}
          </ul>
        </dd>
        {is.hypothesis && (
          <>
            <dt>Hypothesis</dt>
            <dd className="hypo">≈ {is.hypothesis}</dd>
          </>
        )}
        {(is.nextSteps ?? []).length > 0 && (
          <>
            <dt>Next steps</dt>
            <dd>
              <ol className="steps">
                {(is.nextSteps ?? []).map((s, i) => (
                  <li key={i}>
                    <code>{s}</code>
                  </li>
                ))}
              </ol>
            </dd>
          </>
        )}
      </dl>
      <footer>
        <button className="btn" onClick={() => jumpToIssue(is)}>
          ⤳ Jump to timeline
        </button>
        <button
          className="btn"
          onClick={() => {
            navigator.clipboard?.writeText(issueMarkdown(is, target)).then(
              () => {
                setCopied(true);
                setTimeout(() => setCopied(false), 1500);
              },
              () => undefined,
            );
          }}
        >
          {copied ? "✔ Copied" : "⎘ Copy diagnostic summary"}
        </button>
        {(is.nodes ?? []).map((n) => (
          <button key={n} className="btn ghost" onClick={() => select({ type: "node", node: n })}>
            ⬢ {n}
          </button>
        ))}
      </footer>
    </article>
  );
}

export function ProblemsView() {
  const view = useView();
  const [showResolved, setShowResolved] = useState(true);
  const issues = view?.issues ?? [];
  const open = issues.filter((i) => !i.resolvedAt);
  const resolved = issues.filter((i) => i.resolvedAt);
  const target = view?.target ?? "";
  const copyAll = () => {
    const md = [`## bee-scope diagnostic summary (${target}, ${view?.at})`, "", ...open.map((i) => issueMarkdown(i, target) + "\n")].join("\n");
    navigator.clipboard?.writeText(md).catch(() => undefined);
  };
  return (
    <div className="view problems">
      <div className="filters">
        <b>{open.length} open</b>
        <span className="muted">{resolved.length} resolved in the last 15 min</span>
        <label>
          <input type="checkbox" checked={showResolved} onChange={(e) => setShowResolved(e.target.checked)} /> show resolved
        </label>
        <span className="muted">
          <Term id="issue-class">✔ confirmed</Term> = directly observed · ? suspicious = likely, check the evidence · ~ hypothesis = statistical hint
        </span>
        <div className="spacer" />
        {open.length > 0 && (
          <button className="btn" onClick={copyAll}>
            ⎘ Copy all open issues
          </button>
        )}
      </div>
      {issues.length === 0 && <div className="empty big">✔ No problems detected. All ten rules are passing.</div>}
      <div className="issue-list">
        {open.map((is) => (
          <IssueCard key={is.id} is={is} target={target} />
        ))}
        {showResolved && resolved.map((is) => <IssueCard key={is.id + is.resolvedAt} is={is} target={target} />)}
      </div>
    </div>
  );
}
