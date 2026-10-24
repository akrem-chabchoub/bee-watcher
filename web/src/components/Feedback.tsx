import { useState, type ReactNode } from "react";
import { friendly } from "../errors";
import { getFlag, setFlag, useUI } from "../ui";
import { useStore, type Tab } from "../store";
import { Term } from "./Explain";

/** Bottom-right toast stack; announced politely to screen readers. */
export function Toaster() {
  const toasts = useUI((s) => s.toasts);
  const dismiss = useUI((s) => s.dismiss);
  return (
    <div className="toasts" aria-live="polite" aria-atomic="false">
      {toasts.map((t) => (
        <div key={t.id} className={"toast toast-" + t.kind} role={t.kind === "bad" ? "alert" : "status"}>
          <span className="toast-icon" aria-hidden>
            {t.kind === "ok" ? "✔" : t.kind === "bad" ? "✕" : "ℹ"}
          </span>
          <div className="toast-body">
            <div>{t.text}</div>
            {t.detail && (
              <details>
                <summary>details</summary>
                <code className="wrap">{t.detail}</code>
              </details>
            )}
          </div>
          <button className="btn ghost" onClick={() => dismiss(t.id)} aria-label="Dismiss">
            ✕
          </button>
        </div>
      ))}
    </div>
  );
}

/** A human error sentence with the raw text one click away. */
export function ErrorNote({ error, prefix, inline }: { error: unknown; prefix?: string; inline?: boolean }) {
  const f = friendly(error);
  const showDetail = f.detail && f.detail !== f.text;
  const Tag = inline ? "span" : "div";
  return (
    <Tag className={"error-note" + (inline ? " inline" : "")} role="alert">
      <span aria-hidden>✕</span> {prefix ? <b>{prefix}: </b> : null}
      {f.text}
      {showDetail && (
        <details>
          <summary>details</summary>
          <code className="wrap">{f.detail}</code>
        </details>
      )}
    </Tag>
  );
}

/** Placeholder bars while data loads. */
export function Skeleton({ lines = 3, width }: { lines?: number; width?: string }) {
  return (
    <div className="skeleton" aria-busy="true" aria-label="loading">
      {Array.from({ length: lines }, (_, i) => (
        <div key={i} className="sk-line" style={{ width: width ?? `${90 - ((i * 17) % 40)}%` }} />
      ))}
    </div>
  );
}

const intros: Record<Tab, ReactNode> = {
  topology: (
    <>
      Every node on its <Term id="overlay">overlay</Term> position around the ring. Lines are peer links; moving dots are real messages (
      <Term id="flows">flows</Term>). Click anything to inspect it.
    </>
  ),
  node: (
    <>
      The subsystems inside one Bee node and how data moves between them. Pick a node, click a box for its signals and logs.
    </>
  ),
  timeline: <>Every event, per node, over time. Filter by kind, severity or provenance; click an event for details, double-click to rewind there.</>,
  operations: (
    <>
      Uploads (<Term id="tag">tags</Term>) and traced requests step by step. Use “Follow a chunk” to see where a chunk is stored.
    </>
  ),
  problems: (
    <>
      What bee-scope thinks is wrong, how sure it is (<Term id="issue-class">confirmed / suspicious / hypothesis</Term>), the evidence and what to try.
    </>
  ),
  cluster: (
    <>
      Run your local cluster without a terminal: start Docker, create the <Term id="k3d">k3d cluster</Term>, deploy nodes with{" "}
      <Term id="beekeeper">beekeeper</Term>, restart them and run checks.
    </>
  ),
  data: (
    <>
      Buy <Term id="postage-stamp">postage stamps</Term>, upload files to a node and download them by <Term id="reference">reference</Term>.
    </>
  ),
};

/** One-line explanation under the top bar for the current tab. */
export function TabIntro() {
  const tab = useStore((s) => s.tab);
  const setGlossaryOpen = useUI((s) => s.setGlossaryOpen);
  return (
    <div className="tab-intro">
      <span className="tab-intro-text">{intros[tab]}</span>
      <button className="tab-intro-tip" onClick={() => setGlossaryOpen(true)} title="Open the glossary">
        💡 Right-click any Bee term to get it explained
      </button>
    </div>
  );
}

/** Dismissible first-run guide. */
export function GettingStarted() {
  const [hidden, setHidden] = useState(() => getFlag("onboarded"));
  const setTab = useStore((s) => s.setTab);
  const setGlossaryOpen = useUI((s) => s.setGlossaryOpen);
  if (hidden) return null;
  const close = () => {
    setFlag("onboarded", true);
    setHidden(true);
  };
  return (
    <section className="getting-started" aria-label="Getting started">
      <div className="gs-head">
        <h2>Getting started</h2>
        <button className="btn ghost" onClick={close} aria-label="Dismiss getting started">
          ✕
        </button>
      </div>
      <ol className="gs-steps">
        <li>
          <span className="gs-num">1</span>
          <div>
            <b>Set up the cluster</b>
            <p className="muted small">Start Docker, create the local cluster and deploy Bee nodes.</p>
            <button className="btn primary" onClick={() => setTab("cluster")}>
              Open Cluster
            </button>
          </div>
        </li>
        <li>
          <span className="gs-num">2</span>
          <div>
            <b>Upload data</b>
            <p className="muted small">
              Buy a <Term id="postage-stamp">postage stamp</Term>, then drop a file on a node.
            </p>
            <button className="btn" onClick={() => setTab("data")}>
              Open Data
            </button>
          </div>
        </li>
        <li>
          <span className="gs-num">3</span>
          <div>
            <b>Watch it flow</b>
            <p className="muted small">See chunks travel in Topology and follow the upload in Operations.</p>
            <button className="btn" onClick={() => (setTab("topology"), close())}>
              Open Topology
            </button>
          </div>
        </li>
      </ol>
      <p className="muted small gs-foot">
        New to Swarm? <button className="linklike" onClick={() => setGlossaryOpen(true)}>Open the glossary</button> or right-click any term.
      </p>
    </section>
  );
}

/** Top-bar ⏻ button: stops the bee-scope process (the cluster keeps running). */
export function QuitButton() {
  const [ask, setAsk] = useState(false);
  const [busy, setBusy] = useState(false);
  const setQuit = useUI((s) => s.setQuit);
  const quit = () => {
    setBusy(true);
    fetch("/api/quit", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ confirm: true }) })
      .then(async (r) => {
        if (!r.ok) throw new Error(await r.text());
        setQuit();
      })
      .catch((e: unknown) => {
        setBusy(false);
        setAsk(false);
        useUI.getState().toast("bad", "Could not quit bee-scope", e instanceof Error ? e.message : String(e));
      });
  };
  return (
    <>
      <button className="btn ghost icon-btn" onClick={() => setAsk(true)} title="Quit bee-scope" aria-label="Quit bee-scope">
        ⏻
      </button>
      {ask && (
        <div className="overlay" onMouseDown={() => setAsk(false)}>
          <div className="dialog" role="dialog" aria-label="Quit bee-scope" onMouseDown={(e) => e.stopPropagation()}>
            <h3>Quit bee-scope?</h3>
            <p className="consequence">Stops bee-scope. Your cluster keeps running. Open BeeScope.app again to come back.</p>
            <div className="row">
              <button className="btn danger" autoFocus disabled={busy} onClick={quit}>
                {busy ? "Quitting…" : "⏻ Quit"}
              </button>
              <button className="btn" onClick={() => setAsk(false)}>
                Cancel
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  );
}

export function QuitScreen() {
  return (
    <div className="quit-screen" role="status">
      <div className="quit-card">
        <div className="quit-hex" aria-hidden>
          ⬢
        </div>
        <h1>bee-scope stopped</h1>
        <p>You can close this tab. Your cluster keeps running.</p>
        <p className="muted small">Open BeeScope.app again to come back.</p>
      </div>
    </div>
  );
}
