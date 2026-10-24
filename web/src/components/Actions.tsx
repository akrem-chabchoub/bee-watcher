import { useState } from "react";
import { api } from "../api";
import { useStore } from "../store";
import type { ActionResponse } from "../types";

interface ActionDef {
  name: string;
  label: string;
  consequence: (node: string) => string;
  fields?: { key: string; label: string; placeholder?: string; options?: string[]; initial?: string }[];
}

const ACTIONS: ActionDef[] = [
  {
    name: "loggers",
    label: "Set logger verbosity",
    consequence: (n) =>
      `Sends PUT /loggers/{expression}/{verbosity} to ${n}. This changes what the node logs until it restarts. "all" or "debug" on a busy logger can multiply log volume (and bee-scope's own load).`,
    fields: [
      { key: "expression", label: "Logger expression", placeholder: "node/pushsync", initial: "node/pushsync" },
      { key: "verbosity", label: "Verbosity", options: ["none", "error", "warning", "info", "debug", "all"], initial: "debug" },
    ],
  },
  {
    name: "status-peers",
    label: "Query /status/peers",
    consequence: (n) =>
      `Sends GET /status/peers to ${n}. The node asks every connected peer for its status. It shares a semaphore of 1 with /status/neighborhoods, so concurrent calls (beekeeper, dashboards) get 429 while it runs.`,
  },
  {
    name: "debugstore",
    label: "Read /debugstore",
    consequence: (n) => `Sends GET /debugstore to ${n}. It performs a full scan of the node's local store; on a large reserve this takes seconds of disk and CPU.`,
  },
  {
    name: "probe-retrieval",
    label: "Probe retrieval",
    consequence: (n) =>
      `Sends GET /chunks/{address} to ${n} with a fresh traceparent. If the chunk is not local, the node retrieves it from the network (real traffic, accounting debits). With tracing enabled, the trace appears in Operations.`,
    fields: [{ key: "chunk", label: "Chunk address", placeholder: "64 hex characters" }],
  },
];

export function Actions({ node }: { node: string }) {
  const meta = useStore((s) => s.meta);
  const [open, setOpen] = useState<ActionDef | null>(null);
  if (!meta?.actions) {
    return (
      <div className="muted small">
        Actions are disabled{meta?.mode && meta.mode !== "live" ? ` in ${meta.mode} mode` : ""}. bee-scope is read-only by default.
      </div>
    );
  }
  return (
    <div className="actions">
      {ACTIONS.map((a) => (
        <button key={a.name} className="btn" onClick={() => setOpen(a)}>
          {a.label}…
        </button>
      ))}
      {open && <ConfirmDialog def={open} node={node} onClose={() => setOpen(null)} />}
    </div>
  );
}

function ConfirmDialog({ def, node, onClose }: { def: ActionDef; node: string; onClose: () => void }) {
  const [vals, setVals] = useState<Record<string, string>>(() => Object.fromEntries((def.fields ?? []).map((f) => [f.key, f.initial ?? ""])));
  const [busy, setBusy] = useState(false);
  const [res, setRes] = useState<ActionResponse | null>(null);
  const run = () => {
    setBusy(true);
    api
      .action(def.name, { confirm: true, node, ...vals })
      .then(setRes)
      .catch((e: unknown) => setRes({ ok: false, status: 0, ms: 0, body: null, error: e instanceof Error ? e.message : String(e) }))
      .finally(() => setBusy(false));
  };
  return (
    <div className="overlay" onMouseDown={onClose}>
      <div className="dialog" onMouseDown={(e) => e.stopPropagation()} role="dialog" aria-label={def.label}>
        <h3>
          {def.label} on {node}
        </h3>
        <p className="consequence">⚠ {def.consequence(node)}</p>
        {(def.fields ?? []).map((f) => (
          <label key={f.key} className="field">
            {f.label}
            {f.options ? (
              <select value={vals[f.key]} onChange={(e) => setVals({ ...vals, [f.key]: e.target.value })}>
                {f.options.map((o) => (
                  <option key={o}>{o}</option>
                ))}
              </select>
            ) : (
              <input value={vals[f.key]} placeholder={f.placeholder} onChange={(e) => setVals({ ...vals, [f.key]: e.target.value })} />
            )}
          </label>
        ))}
        <div className="row">
          <button className="btn danger" disabled={busy} onClick={run}>
            {busy ? "Running…" : "I understand, run it"}
          </button>
          <button className="btn" onClick={onClose}>
            {res ? "Close" : "Cancel"}
          </button>
        </div>
        {res && (
          <div className={"action-result " + (res.ok ? "ok" : "bad")}>
            <div>
              {res.ok ? "✔" : "✕"} HTTP {res.status || "—"}
              {res.ms ? ` in ${res.ms.toFixed(0)} ms` : ""}
              {res.error ? ` — ${res.error}` : ""}
            </div>
            {res.body !== null && res.body !== undefined && <pre>{typeof res.body === "string" ? res.body : JSON.stringify(res.body, null, 2)}</pre>}
          </div>
        )}
      </div>
    </div>
  );
}
