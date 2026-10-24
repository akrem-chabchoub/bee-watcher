import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { api } from "../api";
import { jobRunning, runAction, useControl } from "../control";
import { dur, fmtTime } from "../format";
import { useNow } from "../components/Badges";
import type { ControlNode, ControlSettings, ControlStatus, Job, JobLine, Profile, StepState } from "../types";
import { Term } from "../components/Explain";
import { ErrorNote, Skeleton } from "../components/Feedback";
import { toast } from "../ui";
import { friendly } from "../errors";

/** Repository of images built into the cluster's local registry. */
const localRepo = (st: ControlStatus) => `k3d-registry.localhost:${st.cluster.registryPort || "5000"}/ethersphere/bee`;

/** A pending confirmation: what will happen and what to run. */
interface Confirm {
  title: string;
  consequence: ReactNode;
  label: string;
  run: () => Promise<unknown>;
}

export function ClusterView() {
  const status = useControl((s) => s.status);
  const error = useControl((s) => s.error);
  const [confirm, setConfirm] = useState<Confirm | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [deployImage, setDeployImage] = useState<string | null>(null);

  if (!status) {
    return (
      <div className="view control">
        <div className="control-main">
          {error ? (
            <ErrorNote error={error} prefix="Cannot reach the control API" />
          ) : (
            <section className="card">
              <h2>Loading cluster status…</h2>
              <Skeleton lines={5} />
            </section>
          )}
        </div>
      </div>
    );
  }

  const busy = jobRunning(status.job);
  const disabled = !status.enabled;
  const locked = disabled || busy;

  /** Runs an action now, or after a confirmation when `consequence` is set. */
  const act = (name: string, body: Record<string, unknown> = {}, ask?: Omit<Confirm, "run">) => {
    const run = () =>
      runAction(name, body).then(
        () => setActionError(null),
        (e: unknown) => {
          setActionError(e instanceof Error ? e.message : String(e));
          toast("bad", `Could not start ${name}: ${friendly(e).text}`, e instanceof Error ? e.message : String(e));
        },
      );
    if (ask) setConfirm({ ...ask, run });
    else void run();
  };

  return (
    <div className="view control">
      <div className="control-main">
        {disabled && (
          <div className="control-disabled" role="status">
            ⊘ Cluster control is disabled: {status.disabledReason || "not available in this mode"}.
          </div>
        )}
        {error && <ErrorNote error={error} prefix="Status refresh failed" />}
        {actionError && (
          <div className="control-error">
            <ErrorNote error={actionError} prefix="Action not started" inline />{" "}
            <button className="btn ghost" onClick={() => setActionError(null)}>
              dismiss
            </button>
          </div>
        )}
        <Checklist status={status} locked={locked} act={act} />
        <NodesCard status={status} locked={locked} act={act} />
        <DeployCard status={status} locked={locked} act={act} image={deployImage} setImage={setDeployImage} />
        <BuildCard status={status} locked={locked} act={act} useImage={setDeployImage} />
        <ChecksCard status={status} locked={locked} act={act} />
        <SettingsCard settings={status.settings} disabled={disabled} />
      </div>
      <JobPanel status={status} />
      {confirm && <ConfirmDialog c={confirm} onClose={() => setConfirm(null)} />}
    </div>
  );
}

type Act = (name: string, body?: Record<string, unknown>, ask?: Omit<Confirm, "run">) => void;

// ---- Setup checklist ----

type CheckState = "ok" | "todo" | "bad" | "wait";

const checkIcon: Record<CheckState, string> = { ok: "✔", todo: "○", bad: "✕", wait: "…" };

function Checklist({ status, locked, act }: { status: ControlStatus; locked: boolean; act: Act }) {
  const tools = status.tools ?? [];
  const missing = tools.filter((t) => !t.ok);
  const nodes = status.nodes ?? [];
  const readyNodes = nodes.filter((n) => n.ready > 0).length;
  const c = status.cluster;

  const rows: { key: string; title: ReactNode; state: CheckState; detail: ReactNode; fix?: ReactNode }[] = [
    {
      key: "tools",
      title: "Tools installed",
      state: missing.length ? "bad" : "ok",
      detail: missing.length ? (
        <ul className="tool-missing">
          {missing.map((t) => (
            <li key={t.name}>
              <b>{t.name}</b> missing{t.hint ? <> — {t.hint}</> : null}
            </li>
          ))}
        </ul>
      ) : (
        <span className="muted">{tools.map((t) => t.name).join(", ") || "—"}</span>
      ),
    },
    {
      key: "docker",
      title: "Docker running",
      state: status.docker.running ? "ok" : "todo",
      detail: status.docker.running ? <span className="muted">docker daemon is up</span> : <span className="muted">{status.docker.error || "Docker Desktop is not running"}</span>,
      fix: !status.docker.running && (
        <button className="btn primary" disabled={locked} onClick={() => act("docker-start")}>
          ▶ Start Docker
        </button>
      ),
    },
    {
      key: "cluster",
      title: (
        <>
          <Term id="k3d">k3d cluster</Term> “{c.name}”
        </>
      ),
      state: !status.docker.running ? "wait" : c.exists && c.running ? "ok" : "todo",
      detail: (
        <span className="muted">
          {!c.exists ? "not created" : c.running ? `running · context ${c.kubeContext} · namespace ${c.namespace}` : "created but stopped"}
          {c.exists && !c.registryRunning ? " · registry stopped" : ""}
          {c.error ? ` · ${c.error}` : ""}
        </span>
      ),
      fix:
        status.docker.running &&
        (!c.exists ? (
          <button className="btn primary" disabled={locked} onClick={() => act("cluster-create")}>
            ＋ Create cluster
          </button>
        ) : !c.running || !c.registryRunning ? (
          <button className="btn primary" disabled={locked} onClick={() => act("cluster-start")}>
            ▶ Start cluster
          </button>
        ) : null),
    },
    {
      key: "geth",
      title: <Term id="geth">geth (local chain)</Term>,
      state: !(c.exists && c.running) ? "wait" : status.geth.ready ? "ok" : status.geth.installed ? "wait" : "todo",
      detail: (
        <span className="muted">
          {status.geth.ready ? "geth-swap ready, contracts deployed" : status.geth.installed ? "installed, starting…" : "not installed (Create cluster installs it)"}
        </span>
      ),
    },
    {
      key: "nodes",
      title: "Bee nodes",
      state: !status.geth.ready ? "wait" : nodes.length === 0 ? "todo" : readyNodes === nodes.length ? "ok" : "wait",
      detail: (
        <span className="muted">
          {nodes.length === 0 ? "no nodes deployed" : `${readyNodes}/${nodes.length} ready`}
          {status.deployed?.image ? ` · ${status.deployed?.image}` : ""}
        </span>
      ),
      fix: status.geth.ready && nodes.length === 0 && (
        <a className="btn primary" href="#deploy">
          ↓ Deploy nodes
        </a>
      ),
    },
  ];

  return (
    <section className="card">
      <h2>Setup</h2>
      <ol className="checklist">
        {rows.map((r) => (
          <li key={r.key} className={"check-" + r.state}>
            <span className="check-icon" aria-hidden>
              {checkIcon[r.state]}
            </span>
            <div className="check-body">
              <div className="check-title">
                {r.title}{" "}
                <span className="check-state">{r.state === "ok" ? "done" : r.state === "todo" ? "to do" : r.state === "bad" ? "problem" : "waiting"}</span>
              </div>
              {r.detail}
            </div>
            <div className="check-fix">{r.fix}</div>
          </li>
        ))}
      </ol>
    </section>
  );
}

// ---- Nodes ----

function phaseClass(n: ControlNode): string {
  if (n.replicas === 0 || n.phase === "Stopped") return "idle";
  if (n.reason || n.phase === "Failed" || n.phase === "Missing") return "bad";
  if (n.phase === "Running" && n.ready > 0) return "ok";
  return "warn";
}

const phaseIcon: Record<string, string> = { ok: "●", warn: "◐", bad: "✕", idle: "■" };

function NodesCard({ status, locked, act }: { status: ControlStatus; locked: boolean; act: Act }) {
  const now = useNow();
  const nodes = status.nodes ?? [];
  const c = status.cluster;
  return (
    <section className="card">
      <div className="card-head">
        <h2>Nodes</h2>
        <div className="spacer" />
        <button
          className="btn"
          disabled={locked || nodes.length === 0}
          onClick={() =>
            act("restart-all", {}, {
              title: "Restart all Bee nodes",
              label: "Restart all",
              consequence: `Deletes all ${nodes.length} Bee pods. Kubernetes recreates them; every node is offline for a short time and reconnects to its peers. Data persists only if the node group has persistence enabled.`,
            })
          }
        >
          ↻ Restart all
        </button>
        <button
          className="btn danger"
          disabled={locked || nodes.length === 0}
          onClick={() =>
            act("bee-remove", {}, {
              title: "Remove all Bee nodes",
              label: "Remove all nodes",
              consequence:
                "Deletes every Bee node: statefulsets, services, ingress routes, keys, config maps and volumes. Overlays, stamps and stored data are lost. geth and the cluster stay; you can deploy again.",
            })
          }
        >
          Remove all nodes
        </button>
        <button
          className="btn danger"
          disabled={locked || !c.running}
          onClick={() =>
            act("cluster-stop", {}, {
              title: "Stop the cluster",
              label: "Stop cluster",
              consequence: `Stops k3d cluster "${c.name}" and the local registry. All nodes and geth go offline; the cluster and its data are kept and Start cluster brings it back.`,
            })
          }
        >
          ■ Stop cluster
        </button>
        <button
          className="btn danger"
          disabled={locked || !c.exists}
          onClick={() =>
            act("cluster-delete", {}, {
              title: "Delete the cluster",
              label: "Delete cluster",
              consequence: `Deletes k3d cluster "${c.name}" and the k3d-registry.localhost registry. Everything in it is destroyed: Bee nodes, geth chain state, stamps, uploaded data and any images pushed to the local registry. Create cluster starts from scratch.`,
            })
          }
        >
          ✕ Delete cluster
        </button>
      </div>
      {nodes.length === 0 ? (
        <div className="muted">No Bee nodes in namespace {c.namespace}.</div>
      ) : (
        <div className="table-scroll">
          <table className="nodes-table">
            <thead>
              <tr>
                <th>node</th>
                <th>group</th>
                <th>state</th>
                <th>ready</th>
                <th>restarts</th>
                <th>image</th>
                <th>age</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {nodes.map((n) => {
                const cls = phaseClass(n);
                const stopped = n.replicas === 0;
                return (
                  <tr key={n.name} className="static">
                    <td>
                      <b>{n.name}</b>
                    </td>
                    <td className="muted">{n.group}</td>
                    <td>
                      <span className={"state-tag " + cls}>
                        {phaseIcon[cls]} {stopped ? "Stopped" : n.phase}
                      </span>
                      {n.reason && <span className="bad-text"> {n.reason}</span>}
                    </td>
                    <td className="num">
                      {n.ready}/{n.replicas}
                    </td>
                    <td className="num">{n.restarts}</td>
                    <td className="mono image-cell" title={n.image}>
                      {n.image}
                    </td>
                    <td className="muted">{n.startedAt && !stopped ? dur(Math.max(0, now - Date.parse(n.startedAt))) : "—"}</td>
                    <td className="row-actions">
                      <button className="btn" disabled={locked || stopped} onClick={() => act("node-restart", { node: n.name })} title="Delete the pod; Kubernetes recreates it">
                        ↻ Restart
                      </button>
                      {stopped ? (
                        <button className="btn" disabled={locked} onClick={() => act("node-start", { node: n.name })}>
                          ▶ Start
                        </button>
                      ) : (
                        <button
                          className="btn"
                          disabled={locked}
                          onClick={() =>
                            act("node-stop", { node: n.name }, {
                              title: `Stop ${n.name}`,
                              label: "Stop node",
                              consequence: `Scales ${n.name} to 0 replicas. The node goes offline (its peers will see it disconnect) until you start it again.`,
                            })
                          }
                        >
                          ■ Stop
                        </button>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

// ---- Deploy ----

function DeployCard({
  status,
  locked,
  act,
  image,
  setImage,
}: {
  status: ControlStatus;
  locked: boolean;
  act: Act;
  image: string | null;
  setImage: (v: string) => void;
}) {
  const profiles = status.profiles ?? [];
  const s = status.settings;
  const deployedGroups = status.deployed?.groups ?? {};
  const [profileName, setProfileName] = useState<string>(() => s.profile || profiles[0]?.name || "");
  const profile: Profile | undefined = profiles.find((p) => p.name === profileName) ?? profiles[0];
  const groups = (profile?.groups ?? []).filter((g) => g.mode !== "bootnode");
  const bootnodes = (profile?.groups ?? []).filter((g) => g.mode === "bootnode").reduce((a, g) => a + (g.count || 1), 0);

  const initialCounts = (p: Profile | undefined): Record<string, number> => {
    const out: Record<string, number> = {};
    for (const g of p?.groups ?? []) {
      if (g.mode === "bootnode") continue;
      out[g.name] = s.counts?.[g.name] ?? deployedGroups[g.name] ?? g.count;
    }
    return out;
  };
  const [counts, setCounts] = useState<Record<string, number>>(() => initialCounts(profile));
  useEffect(() => {
    setCounts(initialCounts(profile));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [profile?.name]);

  const localImage = status.cluster.localImage || "";
  const img = image ?? (s.image || status.deployed?.image || profile?.image || localImage || "ethersphere/bee:latest");
  const presets = [...new Set([status.deployed?.image, localImage, "ethersphere/bee:latest", profile?.image].filter((x): x is string => !!x))];

  const nodes = status.nodes ?? [];
  // Nodes that this deploy would delete: group members above the new count,
  // and every member of a group the profile does not have.
  const removed = nodes
    .filter((n) => n.group !== "bootnode")
    .filter((n) => {
      const want = counts[n.group];
      if (want === undefined) return true;
      const idx = Number(n.name.slice(n.group.length + 1));
      return !Number.isNaN(idx) && idx >= want;
    })
    .map((n) => n.name);

  const total = bootnodes + Object.values(counts).reduce((a, b) => a + b, 0);
  const summary = [bootnodes ? `${bootnodes} bootnode` : "", ...groups.map((g) => `${counts[g.name] ?? 0} ${g.name}`)].filter(Boolean).join(", ");
  const ready = status.enabled && status.cluster.running && status.geth.ready;

  const deploy = () => {
    const body = { profile: profile?.name ?? "", counts, image: img };
    if (removed.length) {
      act("bee-deploy", body, {
        title: "Deploy and scale down",
        label: `Deploy, remove ${removed.length}`,
        consequence: (
          <>
            Applies {summary} with image <code>{img}</code>. This <b>deletes {removed.join(", ")}</b> including their keys, stamps and stored chunks.
          </>
        ),
      });
    } else act("bee-deploy", body);
  };

  return (
    <section className="card" id="deploy">
      <h2>Deploy Bee nodes</h2>
      <p className="muted small">
        Uses beekeeper (<code>create bee-cluster</code>) with a profile from your beekeeper config. Deploying again with different counts scales up or
        down; changing the image rolls the nodes onto it.
      </p>
      {profiles.length === 0 ? (
        <div className="warn-text">⚠ No usable beekeeper profiles found. Check the beekeeper config dir in Settings below.</div>
      ) : (
        <div className="form-grid">
          <label className="field">
            <span>
              <Term id="profile">Profile</Term>
            </span>
            <select value={profile?.name ?? ""} onChange={(e) => setProfileName(e.target.value)} disabled={locked}>
              {profiles.map((p) => (
                <option key={p.name} value={p.name}>
                  {p.name} ({p.groups.map((g) => `${g.count} ${g.name}`).join(", ")})
                </option>
              ))}
            </select>
          </label>
          <div className="field">
            Nodes
            <div className="steppers">
              {bootnodes > 0 && (
                <div className="stepper fixed" title="Bootnodes come from the profile and have fixed keys">
                  <span className="stepper-label">
                    <Term id="bootnode">bootnode</Term>
                  </span>
                  <span className="stepper-val">{bootnodes}</span>
                </div>
              )}
              {groups.map((g) => (
                <Stepper key={g.name} label={g.name} term={g.name === "light" ? "light-node" : g.name === "bee" ? "full-node" : undefined} value={counts[g.name] ?? 0} disabled={locked} onChange={(v) => setCounts({ ...counts, [g.name]: v })} />
              ))}
            </div>
          </div>
          <label className="field wide">
            <span>Image <span className="muted small">— which Bee version the nodes run</span></span>
            <input value={img} onChange={(e) => setImage(e.target.value)} disabled={locked} spellCheck={false} />
            <div className="chips">
              {presets.map((p) => (
                <button key={p} className={"chip" + (p === img ? " on" : "")} onClick={() => setImage(p)} disabled={locked} type="button">
                  {p}
                  {p === status.deployed?.image ? " (running)" : ""}
                </button>
              ))}
            </div>
          </label>
        </div>
      )}
      <div className="row">
        <button className="btn primary" disabled={locked || !ready || !profile || total === 0} onClick={deploy}>
          ⇪ {nodes.length ? "Apply" : "Deploy"} {summary || ""}
        </button>
        {!ready && status.enabled && <span className="hint">ⓘ Deploy needs a running cluster with geth ready (see Setup above).</span>}
        {locked && status.enabled && ready && <span className="hint">ⓘ Wait for the running job to finish.</span>}
        {removed.length > 0 && <span className="warn-text small">▲ will remove {removed.join(", ")}</span>}
      </div>
    </section>
  );
}

function Stepper({ label, value, onChange, disabled, term }: { label: string; value: number; onChange: (v: number) => void; disabled?: boolean; term?: string }) {
  const set = (v: number) => onChange(Math.max(0, Math.min(20, v)));
  return (
    <div className="stepper">
      <span className="stepper-label">{term ? <Term id={term}>{label}</Term> : label}</span>
      <button className="btn" type="button" disabled={disabled || value <= 0} onClick={() => set(value - 1)} aria-label={`fewer ${label} nodes`}>
        −
      </button>
      <input
        className="stepper-val"
        type="number"
        min={0}
        max={20}
        value={value}
        disabled={disabled}
        onChange={(e) => set(Number(e.target.value) || 0)}
        aria-label={`${label} node count`}
      />
      <button className="btn" type="button" disabled={disabled || value >= 20} onClick={() => set(value + 1)} aria-label={`more ${label} nodes`}>
        +
      </button>
    </div>
  );
}

// ---- Build image ----

function BuildCard({ status, locked, act, useImage }: { status: ControlStatus; locked: boolean; act: Act; useImage: (v: string) => void }) {
  const [tag, setTag] = useState("latest");
  const repo = status.settings.beeRepoDir;
  const job = status.job;
  const built = job && job.action === "image-build" && job.state === "succeeded" ? lastBuiltTag.get(job.id) : undefined;
  return (
    <section className="card">
      <h2>Build Bee image from local repo</h2>
      <p className="muted small">
        Runs <code>make docker-build</code> in {repo ? <code>{repo}</code> : <span className="warn-text">no bee repo set (Settings)</span>} and pushes the
        image to the cluster's local registry. Takes several minutes.
      </p>
      <div className="row">
        <label className="field inline">
          tag
          <input value={tag} onChange={(e) => setTag(e.target.value.trim())} disabled={locked} style={{ width: 140 }} />
        </label>
        <button
          className="btn"
          disabled={locked || !repo || !tag || !status.docker.running}
          onClick={() => {
            act("image-build", { tag });
            pendingTag = tag;
          }}
        >
          ⚒ Build and push
        </button>
        <span className="muted small mono">→ {localRepo(status)}:{tag || "…"}</span>
      </div>
      {built && (
        <div className="ok-text row">
          ✔ Built {localRepo(status)}:{built}.
          <button className="btn" onClick={() => useImage(`${localRepo(status)}:${built}`)}>
            Use it in Deploy
          </button>
        </div>
      )}
    </section>
  );
}

// Remembers which tag a build job was started with (the job object has no params).
const lastBuiltTag = new Map<string, string>();
let pendingTag: string | null = null;
useControl.subscribe((s, prev) => {
  if (pendingTag && s.focusJob && s.focusJob !== prev.focusJob) {
    lastBuiltTag.set(s.focusJob, pendingTag);
    pendingTag = null;
  }
});

// ---- Checks ----

function ChecksCard({ status, locked, act }: { status: ControlStatus; locked: boolean; act: Act }) {
  const checks = useMemo(() => {
    const all = status.checks ?? [];
    return [...all.filter((c) => c.startsWith("ci-")), ...all.filter((c) => !c.startsWith("ci-"))];
  }, [status.checks]);
  const [sel, setSel] = useState<string[]>([]);
  const [showAll, setShowAll] = useState(false);
  const visible = showAll ? checks : checks.filter((c) => c.startsWith("ci-"));
  const canRun = status.enabled && (status.nodes ?? []).some((n) => n.ready > 0);
  return (
    <section className="card">
      <h2>
        Run <Term id="beekeeper">beekeeper</Term> checks
      </h2>
      <p className="muted small">Runs <code>beekeeper check</code> against the deployed nodes. Checks generate real traffic, which shows up in Topology and Operations.</p>
      {checks.length === 0 ? (
        <div className="muted">No checks found in the beekeeper config.</div>
      ) : (
        <>
          <div className="chips">
            {visible.map((c) => (
              <button
                key={c}
                className={"chip" + (sel.includes(c) ? " on" : "")}
                aria-pressed={sel.includes(c)}
                disabled={locked}
                onClick={() => setSel((v) => (v.includes(c) ? v.filter((x) => x !== c) : [...v, c]))}
              >
                {sel.includes(c) ? "✔ " : ""}
                {c}
              </button>
            ))}
            {checks.length > visible.length || showAll ? (
              <button className="chip" onClick={() => setShowAll(!showAll)}>
                {showAll ? "only ci-*" : `+${checks.length - visible.length} more`}
              </button>
            ) : null}
          </div>
          <div className="row">
            <button className="btn primary" disabled={locked || !canRun || sel.length === 0} onClick={() => act("check-run", { checks: sel })}>
              ▶ Run {sel.length ? sel.length : ""} check{sel.length === 1 ? "" : "s"}
            </button>
            {sel.length > 0 && (
              <button className="btn ghost" onClick={() => setSel([])}>
                clear
              </button>
            )}
            {!canRun && status.enabled && <span className="muted small">Needs at least one ready node.</span>}
          </div>
        </>
      )}
    </section>
  );
}

// ---- Settings ----

const settingFields: { key: keyof ControlSettings; label: string; help: string }[] = [
  { key: "beelocalDir", label: "beelocal directory", help: "contains beelocal.sh and config/k3d.yaml" },
  { key: "beekeeperConfigDir", label: "beekeeper config directory", help: "the YAML files with clusters, node groups and checks" },
  { key: "beeRepoDir", label: "bee repository", help: "used by Build Bee image" },
  { key: "beekeeper", label: "beekeeper binary", help: "path to the beekeeper executable" },
];

function SettingsCard({ settings, disabled }: { settings: ControlSettings; disabled: boolean }) {
  const [open, setOpen] = useState(false);
  const [vals, setVals] = useState<Record<string, string>>({});
  const [msg, setMsg] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const missing = settingFields.filter((f) => !settings[f.key]);
  const value = (k: keyof ControlSettings) => vals[k] ?? String(settings[k] ?? "");
  const dirty = Object.keys(vals).some((k) => vals[k] !== String(settings[k as keyof ControlSettings] ?? ""));

  const save = () => {
    setSaving(true);
    api.control
      .saveSettings(Object.fromEntries(settingFields.map((f) => [f.key, value(f.key)])) as Partial<ControlSettings>)
      .then(() => {
        setVals({});
        setMsg("✔ saved");
        void useControl.getState().refresh();
      })
      .catch((e: unknown) => setMsg("✕ " + (e instanceof Error ? e.message : String(e))))
      .finally(() => setSaving(false));
  };

  return (
    <section className="card">
      <details open={open || missing.length > 0} onToggle={(e) => setOpen((e.target as HTMLDetailsElement).open)}>
        <summary>
          <h2 className="inline-h">Settings</h2>{" "}
          {missing.length > 0 ? <span className="warn-text small">▲ {missing.map((f) => f.label).join(", ")} not set</span> : <span className="muted small">paths used by the actions</span>}
        </summary>
        <div className="settings-grid">
          {settingFields.map((f) => (
            <label key={f.key} className="field">
              <span>
                {f.label}{" "}
                {settings.detected?.[f.key] && <span className="badge" title="found automatically at startup">auto-detected</span>}
              </span>
              <input value={value(f.key)} onChange={(e) => setVals({ ...vals, [f.key]: e.target.value })} disabled={disabled} spellCheck={false} placeholder="not found" />
              <span className="muted small">{f.help}</span>
            </label>
          ))}
        </div>
        <div className="row">
          <button className="btn primary" disabled={disabled || saving || !dirty} onClick={save}>
            {saving ? "Saving…" : "Save settings"}
          </button>
          {dirty && (
            <button className="btn ghost" onClick={() => setVals({})}>
              reset
            </button>
          )}
          {msg && <span className={msg.startsWith("✔") ? "ok-text" : "bad-text"}>{msg}</span>}
        </div>
      </details>
    </section>
  );
}

// ---- Job panel ----

const stepIcon: Record<StepState, string> = { pending: "○", running: "▶", done: "✔", failed: "✕", skipped: "⊘" };
const jobLook: Record<string, { icon: string; cls: string }> = {
  running: { icon: "▶", cls: "active" },
  succeeded: { icon: "✔", cls: "ok" },
  failed: { icon: "✕", cls: "bad" },
  canceled: { icon: "⊘", cls: "muted" },
};

function JobPanel({ status }: { status: ControlStatus }) {
  const focusJob = useControl((s) => s.focusJob);
  const setFocusJob = useControl((s) => s.setFocusJob);
  const [jobs, setJobs] = useState<Job[]>([]);
  const [showRecent, setShowRecent] = useState(false);
  const jobId = focusJob ?? status.job?.id ?? null;
  const current = status.job && status.job.id === jobId ? status.job : (jobs.find((j) => j.id === jobId) ?? null);

  useEffect(() => {
    if (!showRecent) return;
    const ac = new AbortController();
    api.control
      .jobs(ac.signal)
      .then(setJobs)
      .catch(() => {});
    return () => ac.abort();
  }, [showRecent, status.job?.id, status.job?.state]);

  return (
    <aside className="job-panel" aria-label="Job output">
      {jobId ? <JobView id={jobId} fallback={current} /> : <div className="muted job-empty">No job yet. Actions you start here show their progress and output in this panel.</div>}
      <details className="job-recent" open={showRecent} onToggle={(e) => setShowRecent((e.target as HTMLDetailsElement).open)}>
        <summary>Recent jobs</summary>
        <ul className="mini-events">
          {jobs.map((j) => (
            <li key={j.id} onClick={() => setFocusJob(j.id)} className={j.id === jobId ? "on" : ""}>
              <span className={"state-tag " + (jobLook[j.state]?.cls ?? "")}>
                {jobLook[j.state]?.icon} {j.state}
              </span>{" "}
              {j.title} <span className="muted">{fmtTime(j.startedAt)}</span>
            </li>
          ))}
          {jobs.length === 0 && <li className="muted">none</li>}
        </ul>
      </details>
    </aside>
  );
}

function JobView({ id, fallback }: { id: string; fallback: Job | null }) {
  const [job, setJob] = useState<Job | null>(fallback);
  const [lines, setLines] = useState<JobLine[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [canceling, setCanceling] = useState(false);
  const box = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  const now = useNow();

  useEffect(() => {
    setLines([]);
    setJob(fallback);
    setErr(null);
    stick.current = true;
    let alive = true;
    let next = 0;
    let t: ReturnType<typeof setTimeout> | undefined;
    const ac = new AbortController();
    const poll = async () => {
      try {
        const r = await api.control.log(id, next, ac.signal);
        if (!alive) return;
        setJob(r.job);
        setErr(null);
        const got = r.lines ?? [];
        if (got.length) setLines((prev) => prev.concat(got).slice(-5000));
        next = r.next;
        // Keep draining while lines arrive; stop once a finished job has no more.
        if (r.job.state !== "running" && got.length === 0) return;
        t = setTimeout(poll, got.length >= 500 ? 50 : 1000);
      } catch (e) {
        if (!alive || ac.signal.aborted) return;
        setErr(e instanceof Error ? e.message : String(e));
        t = setTimeout(poll, 2000);
      }
    };
    void poll();
    return () => {
      alive = false;
      ac.abort();
      if (t) clearTimeout(t);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  useEffect(() => {
    const el = box.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  }, [lines]);

  if (!job) return <div className="muted job-empty">{err ? `⚠ ${err}` : "Loading job…"}</div>;
  const look = jobLook[job.state] ?? { icon: "?", cls: "idle" };
  const end = job.endedAt && Date.parse(job.endedAt) > 0 ? Date.parse(job.endedAt) : now;
  const cancel = () => {
    setCanceling(true);
    api.control
      .cancel(job.id)
      .then(setJob)
      .catch((e: unknown) => setErr(e instanceof Error ? e.message : String(e)))
      .finally(() => setCanceling(false));
  };

  return (
    <div className="job">
      <div className="job-head">
        <span className={"state-tag " + look.cls}>
          {look.icon} {job.state}
        </span>
        <b className="job-title">{job.title}</b>
        <span className="muted small">{dur(Math.max(0, end - Date.parse(job.startedAt)))}</span>
        <div className="spacer" />
        {job.state === "running" && (
          <button className="btn danger" onClick={cancel} disabled={canceling}>
            {canceling ? "Canceling…" : "✕ Cancel"}
          </button>
        )}
      </div>
      {job.error && (
        <div className="job-error">
          <ErrorNote error={job.error} />
        </div>
      )}
      {err && <div className="warn-text small">⚠ log: {err}</div>}
      {(job.steps ?? []).length > 0 && (
        <ol className="job-steps">
          {(job.steps ?? []).map((s, i) => (
            <li key={i} className={"step-" + s.state}>
              <span aria-hidden>{stepIcon[s.state]}</span> {s.title} <span className="muted small">{s.state}</span>
            </li>
          ))}
        </ol>
      )}
      <div
        className="job-log"
        ref={box}
        onScroll={(e) => {
          const el = e.currentTarget;
          stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
        }}
      >
        {lines.length === 0 ? (
          <div className="muted">{job.state === "running" ? "waiting for output…" : "no output"}</div>
        ) : (
          lines.map((l) => (
            <div key={l.n} className={"ll ll-" + l.stream}>
              <span className="ll-t">{fmtTime(l.t)}</span> {l.text}
            </div>
          ))
        )}
      </div>
    </div>
  );
}

// ---- Confirm dialog ----

function ConfirmDialog({ c, onClose }: { c: Confirm; onClose: () => void }) {
  const [busy, setBusy] = useState(false);
  return (
    <div className="overlay" onMouseDown={onClose}>
      <div className="dialog" onMouseDown={(e) => e.stopPropagation()} role="dialog" aria-label={c.title}>
        <h3>{c.title}</h3>
        <p className="consequence">⚠ {c.consequence}</p>
        <div className="row">
          <button
            className="btn danger"
            disabled={busy}
            autoFocus
            onClick={() => {
              setBusy(true);
              void c.run().finally(onClose);
            }}
          >
            {busy ? "Starting…" : c.label}
          </button>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
        </div>
      </div>
    </div>
  );
}
