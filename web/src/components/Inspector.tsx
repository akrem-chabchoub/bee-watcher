import { useState, type ReactNode } from "react";
import { api, type EventQuery } from "../api";
import { usePoll } from "../hooks";
import { useStore, useView } from "../store";
import { ago, fmtTime, isZeroTime, num, short, statusLook } from "../format";
import { Fresh, PhaseBadge, ProvBadge, SevBadge, SourceTag, StatusBadge, useNow } from "./Badges";
import { Actions } from "./Actions";
import { Sparkline } from "./Sparkline";
import { Term } from "./Explain";
import { match as matchTerm } from "../glossary";
import { IssueCard } from "../views/ProblemsView";
import { OperationDetail } from "../views/OperationsView";
import type { BeeEvent, NodeState, Selection, Snapshot } from "../types";

type ITab = "overview" | "events" | "logs" | "raw";

export function Inspector() {
  const selection = useStore((s) => s.selection);
  const select = useStore((s) => s.select);
  const view = useView();
  const [rawTab, setTab] = useState<ITab>("overview");

  if (!selection) {
    return (
      <aside className="inspector idle">
        <div className="empty">
          <p>Select a node, link, component, event, issue or operation to inspect it.</p>
          <p className="muted small">Everything shown carries its source, freshness and provenance: observed (one source), derived (computed), inferred (a hypothesis), simulated.</p>
          {view && <ClusterSummary snap={view} />}
        </div>
      </aside>
    );
  }

  const key = JSON.stringify(selection);
  const tabs: ITab[] = selection.type === "issue" || selection.type === "operation" ? ["overview", "events"] : ["overview", "events", "logs", "raw"];
  const tab = tabs.includes(rawTab) ? rawTab : "overview";

  return (
    <aside className="inspector">
      <div className="insp-head">
        <span className="insp-type">{selection.type}</span>
        <h2>{title(selection)}</h2>
        <button className="btn ghost" onClick={() => select(null)} title="Close">
          ✕
        </button>
      </div>
      <div className="insp-tabs">
        {tabs.map((t) => (
          <button key={t} className={"tab" + (tab === t ? " on" : "")} onClick={() => setTab(t)}>
            {t}
          </button>
        ))}
      </div>
      <div className="insp-body" key={key + tab}>
        {tab === "overview" && <Overview sel={selection} snap={view} />}
        {tab === "events" && <RecentEvents q={eventQuery(selection)} filter={eventFilter(selection)} />}
        {tab === "logs" && <Logs sel={selection} />}
        {tab === "raw" && <Raw sel={selection} snap={view} />}
      </div>
    </aside>
  );
}

function title(s: Selection): string {
  switch (s.type) {
    case "node":
      return s.node;
    case "component":
      return `${s.node} / ${s.component}`;
    case "edge":
      return `${s.a} ↔ ${s.b}`;
    case "event":
      return `event #${s.seq}`;
    case "issue":
      return s.id;
    case "operation":
      return s.id;
    case "peer":
      return s.node ?? short(s.address, 16);
    case "corr":
      return `${s.kind} ${short(s.id, 16)}`;
  }
}

function eventQuery(s: Selection): EventQuery {
  switch (s.type) {
    case "node":
      return { node: s.node, newest: true, limit: 80, minSev: "debug" };
    case "component":
      return { node: s.node, component: s.component, newest: true, limit: 80, minSev: "debug" };
    case "edge":
      return { node: [s.a, s.b], kind: ["peer.", "pushsync.", "retrieval.", "puller."], newest: true, limit: 200 };
    case "peer":
      return { corr: s.address, newest: true, limit: 80 };
    case "corr":
      return { corr: s.id, newest: true, limit: 200 };
    case "issue":
      return { q: s.id, newest: true, limit: 50 };
    case "operation":
      return { q: s.id, newest: true, limit: 50 };
    case "event":
      return { newest: true, limit: 1 };
  }
}

function eventFilter(s: Selection): ((e: BeeEvent) => boolean) | undefined {
  if (s.type === "edge") {
    return (e) => (e.node === s.a && e.corr?.peerNode === s.b) || (e.node === s.b && e.corr?.peerNode === s.a);
  }
  return undefined;
}

function ClusterSummary({ snap }: { snap: Snapshot }) {
  const nodes = Object.values(snap.nodes);
  const c = snap.collector;
  return (
    <div className="kv small">
      <Row k="cluster">{snap.target}</Row>
      <Row k="nodes">
        {nodes.filter((n) => n.phase === "up").length} up of {nodes.length}
      </Row>
      <Row k="links">{(snap.edges ?? []).length}</Row>
      <Row k="network radius">{snap.networkRadius ?? "—"}</Row>
      <Row k="collector">
        {num(c.observations)} observations · {num(c.logLines)} log lines · {num(c.spans)} spans
      </Row>
      <Row k="history">
        {num(c.eventsStored)} events since {fmtTime(c.oldestEvent)} · {c.keyframes} keyframes
      </Row>
      <Row k="OTLP">{c.otlpListening || "off"}</Row>
    </div>
  );
}

/** Wraps a label in <Term> when it names a glossary entry. */
function Label({ text }: { text: string }) {
  const g = matchTerm(text);
  return g ? <Term id={g.id}>{text}</Term> : <>{text}</>;
}

function Row({ k, children }: { k: string; children: ReactNode }) {
  return (
    <div className="kv-row">
      <span className="k">
        <Label text={k} />
      </span>
      <span className="v">{children}</span>
    </div>
  );
}

function Section({ title, children, extra }: { title: string; children: ReactNode; extra?: ReactNode }) {
  return (
    <section className="insp-section">
      <h4>
        <Label text={title} /> {extra}
      </h4>
      {children}
    </section>
  );
}

function Overview({ sel, snap }: { sel: Selection; snap: Snapshot | null }) {
  switch (sel.type) {
    case "node": {
      const n = snap?.nodes[sel.node];
      return n ? <NodeOverview n={n} snap={snap!} /> : <Missing what={sel.node} />;
    }
    case "component": {
      const n = snap?.nodes[sel.node];
      return n ? <ComponentOverview n={n} id={sel.component} /> : <Missing what={sel.node} />;
    }
    case "edge":
      return <EdgeOverview a={sel.a} b={sel.b} snap={snap} />;
    case "event":
      return <EventOverview seq={sel.seq} />;
    case "issue": {
      const is = snap?.issues?.find((i) => i.id === sel.id);
      return is ? <IssueCard is={is} target={snap?.target ?? ""} /> : <Missing what={`issue ${sel.id} (it may have expired)`} />;
    }
    case "operation":
      return <OperationOverview id={sel.id} />;
    case "peer":
      return <PeerOverview address={sel.address} snap={snap} />;
    case "corr":
      return <CorrOverview kind={sel.kind} id={sel.id} />;
  }
}

function Missing({ what }: { what: string }) {
  return <div className="empty">{what} is not in the current view.</div>;
}

function NodeOverview({ n, snap }: { n: NodeState; snap: Snapshot }) {
  const select = useStore((s) => s.select);
  const setTab = useStore((s) => s.setTab);
  const { data: detail, error } = usePoll((s) => api.node(n.name, s), [n.name], 5000);
  const now = useNow();
  const st = n.status;
  const r = n.redistribution;
  const issues = (snap.issues ?? []).filter((i) => !i.resolvedAt && (i.nodes ?? []).includes(n.name));
  return (
    <>
      <Section title="State">
        <div className="kv">
          <Row k="phase">
            <PhaseBadge phase={n.phase} /> since {fmtTime(n.phaseSince)} ({ago(n.phaseSince, now)})
          </Row>
          {n.phaseReason && <Row k="reason">{n.phaseReason}</Row>}
          <Row k="group / mode">
            {n.group ?? "—"} / {n.mode ?? "—"}
          </Row>
          <Row k="pod">{n.pod ?? "—"}</Row>
          <Row k="version">{n.version ?? "—"}</Row>
          <Row k="overlay">
            <span className="mono">{n.overlay ? short(n.overlay, 20) : "—"}</span>
          </Row>
          <Row k="log stream">
            {n.logStream}
            {n.logDropped > 0 && <span className="warn-text"> · ⚠ {n.logDropped} lines dropped</span>}
          </Row>
        </div>
      </Section>
      {issues.length > 0 && (
        <Section title={`Open issues (${issues.length})`}>
          <ul className="mini-events">
            {issues.map((i) => (
              <li key={i.id} onClick={() => select({ type: "issue", id: i.id })}>
                <SevBadge sev={i.sev} compact /> {i.title}
              </li>
            ))}
          </ul>
        </Section>
      )}
      {st && (
        <Section title="Storage & sync" extra={<SrcFresh n={n} ref_="GET /status" />}>
          <div className="kv">
            <Row k="storage radius">
              {st.storageRadius}
              {snap.networkRadius !== undefined && snap.networkRadius !== st.storageRadius && <span className="warn-text"> ▲ cluster {snap.networkRadius}</span>}
            </Row>
            <Row k="committed depth">{st.committedDepth}</Row>
            <Row k="reserve">
              {num(st.reserveSize)} ({num(st.reserveSizeWithinRadius)} within radius)
            </Row>
            <Row k="pullsync rate">{st.pullsyncRate > 0 ? `${num(st.pullsyncRate)} chunks/s (catching up)` : "0 (synced)"}</Row>
            <Row k="neighborhood">{st.neighborhoodSize} peers</Row>
            <Row k="warming up">{st.isWarmingUp ? "yes" : "no"}</Row>
            <Row k="reachable">{st.isReachable ? "yes" : "▲ no"}</Row>
          </div>
        </Section>
      )}
      <Section title="Kademlia" extra={<SrcFresh n={n} ref_="GET /topology" />}>
        <div className="kv">
          <Row k="connected">
            {n.connected} of {n.population} known · {n.externalPeers} outside cluster
          </Row>
          <Row k="depth">{n.depth}</Row>
          <Row k="unhealthy">{n.unhealthyPeers}</Row>
          <Row k="reachability">{n.reachability ?? "—"}</Row>
        </div>
        {detail?.peers && detail.peers.length > 0 && (
          <table className="peers">
            <thead>
              <tr>
                <th>bin</th>
                <th>peer</th>
                <th>latency</th>
                <th>health</th>
                <th>dir</th>
              </tr>
            </thead>
            <tbody>
              {detail.peers.map((p) => (
                <tr key={p.address} onClick={() => select(p.node ? { type: "node", node: p.node } : { type: "peer", address: p.address })}>
                  <td>{p.bin}</td>
                  <td>{p.node ? <b>{p.node}</b> : <span className="mono">{short(p.address, 10)}</span>}</td>
                  <td>{p.latencyMs.toFixed(0)} ms</td>
                  <td>{p.healthy ? <span className="ok-text">● ok</span> : <span className="warn-text">▲ unhealthy</span>}</td>
                  <td className="muted">{p.direction}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {error && <div className="warn-text small">node detail: {error}</div>}
      </Section>
      {detail && Object.keys(detail.series).length > 0 && (
        <Section title="Series (derived)">
          {Object.entries(detail.series)
            .filter(([, pts]) => pts.length > 1)
            .sort(([a], [b]) => a.localeCompare(b))
            .map(([name, pts]) => (
              <Sparkline key={name} label={name} pts={pts} />
            ))}
        </Section>
      )}
      <Section title="Components">
        <ul className="comp-list">
          {Object.values(n.components ?? {}).map((c) => (
            <li
              key={c.id}
              onClick={() => {
                select({ type: "component", node: n.name, component: c.id });
                setTab("node");
              }}
            >
              <span style={{ color: statusLook[c.status].color }}>{statusLook[c.status].icon}</span> {c.id}{" "}
              <span className="muted">{c.reason}</span>
            </li>
          ))}
        </ul>
      </Section>
      {r && (
        <Section title="Redistribution" extra={<SrcFresh n={n} ref_="GET /redistributionstate" />}>
          <div className="kv">
            <Row k="phase / round">
              {r.phase} / {r.round}
            </Row>
            <Row k="healthy">{r.isHealthy ? "yes" : "▲ no"}</Row>
            <Row k="frozen">{r.isFrozen ? `✕ yes (round ${r.lastFrozenRound})` : "no"}</Row>
            <Row k="fully synced">{r.isFullySynced ? "yes" : "no"}</Row>
            <Row k="funds">{r.hasSufficientFunds ? "sufficient" : "▲ insufficient"}</Row>
            <Row k="last won / played">
              {r.lastWonRound} / {r.lastPlayedRound}
            </Row>
          </div>
        </Section>
      )}
      {n.chainState && (
        <Section title="Chain" extra={<SrcFresh n={n} ref_="GET /chainstate" />}>
          <div className="kv">
            <Row k="block / tip">
              {n.chainState.block} / {n.chainState.chainTip}
              {n.chainState.chainTip - n.chainState.block > 5 && <span className="warn-text"> ▲ behind {n.chainState.chainTip - n.chainState.block}</span>}
            </Row>
            <Row k="price">{n.chainState.currentPrice}</Row>
          </div>
        </Section>
      )}
      {n.stamps && n.stamps.length > 0 && (
        <Section title="Stamps" extra={<SrcFresh n={n} ref_="GET /stamps" />}>
          <ul className="mini-events">
            {n.stamps.map((s) => (
              <li key={s.batchID} onClick={() => select({ type: "corr", kind: "batch", id: s.batchID })}>
                {s.usable ? <span className="ok-text">● usable</span> : <span className="warn-text">▲ not usable</span>} <span className="mono">{short(s.batchID, 10)}</span> depth{" "}
                {s.depth} · {(s.utilizationRatio * 100).toFixed(0)}% used · ttl {s.batchTTL}s {s.label && <span className="muted">({s.label})</span>}
              </li>
            ))}
          </ul>
        </Section>
      )}
      <Section title="Sources">
        <table className="sources">
          <thead>
            <tr>
              <th>source</th>
              <th>last ok</th>
              <th>latency</th>
              <th>every</th>
            </tr>
          </thead>
          <tbody>
            {Object.values(n.sources ?? {})
              .sort((a, b) => a.ref.localeCompare(b.ref))
              .map((s) => (
                <tr key={s.ref} className={s.stale ? "stale" : ""} title={s.lastErr ? `last error: ${s.lastErr}` : ""}>
                  <td>
                    {s.stale ? "⏸ " : ""}
                    {s.ref}
                    {s.lastErr && <span className="warn-text"> ✕</span>}
                  </td>
                  <td>{isZeroTime(s.lastOk) ? "never" : ago(s.lastOk, now)}</td>
                  <td>{s.latencyMs.toFixed(0)} ms</td>
                  <td>{s.intervalS}s</td>
                </tr>
              ))}
          </tbody>
        </table>
      </Section>
      <Section title="Actions">
        <Actions node={n.name} />
      </Section>
    </>
  );
}

function SrcFresh({ n, ref_ }: { n: NodeState; ref_: string }) {
  const s = n.sources?.[ref_];
  if (!s) return <span className="muted small">no {ref_}</span>;
  return (
    <span className="small muted">
      {ref_} · <Fresh at={s.lastOk} stale={s.stale} />
    </span>
  );
}

function ComponentOverview({ n, id }: { n: NodeState; id: string }) {
  const registry = useStore((s) => s.registry);
  const spec = registry?.specs.find((s) => s.id === id);
  const cs = n.components?.[id];
  const flows = (registry?.flows ?? []).filter((f) => f.from === id || f.to === id);
  return (
    <>
      <Section title="State" extra={cs && <ProvBadge prov={cs.prov} />}>
        {cs ? (
          <div className="kv">
            <Row k="status">
              <StatusBadge status={cs.status} />
            </Row>
            <Row k="reason">{cs.reason ?? "—"}</Row>
            <Row k="activity">{num(cs.activity)} /s</Row>
            <Row k="errors">{num(cs.errorRate)} /s</Row>
            {cs.queue !== undefined && cs.queue !== null && <Row k="queue">{num(cs.queue)}</Row>}
            <Row k="log lines">{num(cs.logRate)} /s</Row>
            <Row k="fed by">{cs.observable?.length ? cs.observable.join(", ") : <span className="warn-text">⊘ nothing (unobservable)</span>}</Row>
            <Row k="freshness">
              <Fresh at={cs.freshAt} stale={cs.stale} />
            </Row>
          </div>
        ) : (
          <div className="muted">No state for this component.</div>
        )}
      </Section>
      {cs?.signals && cs.signals.length > 0 && (
        <Section title="Signals">
          <table className="signals">
            <tbody>
              {cs.signals.map((s, i) => (
                <tr key={i} className={s.bad ? "bad" : ""}>
                  <td title={s.name}>
                    {s.bad ? "✕ " : ""}
                    {s.label}
                  </td>
                  <td className="num">
                    {num(s.value)}
                    {s.unit}
                  </td>
                  <td className="muted">{s.kind}</td>
                  <td>
                    <ProvBadge prov={s.prov} compact />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </Section>
      )}
      {spec && (
        <Section title="About">
          <p>{spec.description}</p>
          <div className="kv small">
            <Row k="package">
              <span className="mono">{spec.pkg}</span>
            </Row>
            {spec.metrics?.length ? <Row k="metrics">{spec.metrics.join(", ")}</Row> : null}
            {spec.loggers?.length ? <Row k="loggers">{spec.loggers.map((l) => "node/" + l).join(", ")}</Row> : null}
            {spec.spans?.length ? <Row k="spans">{spec.spans.join(", ")}</Row> : null}
            {spec.endpoints?.length ? <Row k="endpoints">{spec.endpoints.join(", ")}</Row> : null}
          </div>
          {spec.blind?.length ? (
            <>
              <h5>Blind spots</h5>
              <ul className="blind">
                {spec.blind.map((b, i) => (
                  <li key={i}>⊘ {b}</li>
                ))}
              </ul>
            </>
          ) : null}
        </Section>
      )}
      {flows.length > 0 && (
        <Section title="Flows">
          <ul className="mini-events">
            {flows.map((f) => (
              <li key={f.from + f.to}>
                {f.from} → {f.to} <span className="muted">{f.label}</span>{" "}
                {f.signal ? <b>{num(n.flowRates?.[f.from + ">" + f.to] ?? 0)}/s</b> : <span className="muted">(no signal)</span>}
              </li>
            ))}
          </ul>
        </Section>
      )}
      <Section title="Recent events">
        <RecentEvents q={{ node: n.name, component: id, newest: true, limit: 20, minSev: "debug" }} compact />
      </Section>
    </>
  );
}

function EdgeOverview({ a, b, snap }: { a: string; b: string; snap: Snapshot | null }) {
  const e = snap?.edges?.find((x) => (x.a === a && x.b === b) || (x.a === b && x.b === a));
  const select = useStore((s) => s.select);
  if (!e) return <Missing what={`link ${a} ↔ ${b}`} />;
  return (
    <Section title="Link" extra={<ProvBadge prov={e.prov} />}>
      <div className="kv">
        <Row k="ends">
          <a href="#" onClick={(ev) => (ev.preventDefault(), select({ type: "node", node: e.a }))}>
            {e.a}
          </a>{" "}
          ↔{" "}
          <a href="#" onClick={(ev) => (ev.preventDefault(), select({ type: "node", node: e.b }))}>
            {e.b}
          </a>
        </Row>
        <Row k="bin">{e.bin}</Row>
        <Row k="latency">{e.latencyMs.toFixed(0)} ms (max of both sides, EWMA)</Row>
        <Row k="health">{e.healthy ? "● healthy" : "▲ unhealthy on at least one side"}</Row>
        <Row k="seen by">
          {e.seenByA ? `✔ ${e.a}` : `✕ ${e.a}`} · {e.seenByB ? `✔ ${e.b}` : `✕ ${e.b}`}
        </Row>
        <Row k="freshness">
          <Fresh at={e.freshAt} />
        </Row>
        <Row k="source">
          <SourceTag source={{ adapter: "api", ref: "GET /topology" }} />
        </Row>
      </div>
      <h5>Peer events between the two</h5>
      <RecentEvents q={eventQuery({ type: "edge", a, b })} filter={eventFilter({ type: "edge", a, b })} compact />
    </Section>
  );
}

function EventOverview({ seq }: { seq: number }) {
  const cached = useStore((s) => s.events.find((e) => e.seq === seq));
  const { data, error } = usePoll((s) => (cached ? Promise.resolve(cached) : api.event(seq, s)), [seq, !!cached]);
  const select = useStore((s) => s.select);
  const setCursor = useStore((s) => s.setCursor);
  const e = cached ?? data;
  if (!e) return error ? <div className="warn-text">{error}</div> : <div className="muted">Loading…</div>;
  const c = e.corr;
  return (
    <>
      <Section title="Event" extra={<ProvBadge prov={e.prov} />}>
        <p className="summary">{e.summary}</p>
        <div className="kv">
          <Row k="kind">
            <span className="mono">{e.kind}</span>
          </Row>
          <Row k="severity">
            <SevBadge sev={e.sev} />
          </Row>
          <Row k="observed at">
            {fmtTime(e.t, true)}{" "}
            <a href="#" onClick={(ev) => (ev.preventDefault(), setCursor(Date.parse(e.t)))}>
              move cursor here
            </a>
          </Row>
          <Row k="ingested">{fmtTime(e.ingestedAt, true)}</Row>
          {e.node && (
            <Row k="node">
              <a href="#" onClick={(ev) => (ev.preventDefault(), select({ type: "node", node: e.node! }))}>
                {e.node}
              </a>
            </Row>
          )}
          {e.component && e.node && (
            <Row k="component">
              <a href="#" onClick={(ev) => (ev.preventDefault(), select({ type: "component", node: e.node!, component: e.component! }))}>
                {e.component}
              </a>
            </Row>
          )}
          <Row k="source">
            <SourceTag source={e.source} />
          </Row>
        </div>
      </Section>
      {c && (
        <Section title="Correlation">
          <div className="kv">
            {c.peer && (
              <Row k="peer">
                <a href="#" onClick={(ev) => (ev.preventDefault(), select(c.peerNode ? { type: "node", node: c.peerNode } : { type: "peer", address: c.peer! }))}>
                  {c.peerNode ?? short(c.peer, 16)}
                </a>
              </Row>
            )}
            {c.chunk && (
              <Row k="chunk">
                <a href="#" onClick={(ev) => (ev.preventDefault(), select({ type: "corr", kind: "chunk", id: c.chunk! }))}>
                  {short(c.chunk, 16)}
                </a>
              </Row>
            )}
            {c.batchId && (
              <Row k="batch">
                <a href="#" onClick={(ev) => (ev.preventDefault(), select({ type: "corr", kind: "batch", id: c.batchId! }))}>
                  {short(c.batchId, 16)}
                </a>
              </Row>
            )}
            {c.traceId && (
              <Row k="trace">
                <a href="#" onClick={(ev) => (ev.preventDefault(), select({ type: "operation", id: "trace:" + c.traceId }))}>
                  {short(c.traceId, 16)}
                </a>
              </Row>
            )}
            {c.tagId ? <Row k="tag">{c.tagId}</Row> : null}
          </div>
        </Section>
      )}
      {e.attrs && Object.keys(e.attrs).length > 0 && (
        <Section title="Attributes">
          <div className="kv small">
            {Object.entries(e.attrs).map(([k, v]) => (
              <Row key={k} k={k}>
                {k === "operation" ? (
                  <a href="#" onClick={(ev) => (ev.preventDefault(), select({ type: "operation", id: v }))}>
                    {v}
                  </a>
                ) : k === "issue" ? (
                  <a href="#" onClick={(ev) => (ev.preventDefault(), select({ type: "issue", id: v }))}>
                    {v}
                  </a>
                ) : (
                  <span className="mono wrap">{v}</span>
                )}
              </Row>
            ))}
          </div>
        </Section>
      )}
      {e.rawRef ? (
        <Section title="Raw payload">
          <RawRef id={e.rawRef} />
        </Section>
      ) : null}
    </>
  );
}

function OperationOverview({ id }: { id: string }) {
  const { data, error } = usePoll((s) => api.operation(id, s), [id], 3000);
  if (error) return <div className="warn-text">{error}</div>;
  if (!data) return <div className="muted">Loading…</div>;
  return <OperationDetail op={data} />;
}

function PeerOverview({ address, snap }: { address: string; snap: Snapshot | null }) {
  const select = useStore((s) => s.select);
  const node = snap ? Object.values(snap.nodes).find((n) => n.overlay === address || n.overlay?.startsWith(address)) : undefined;
  return (
    <Section title="Peer">
      <div className="kv">
        <Row k="overlay">
          <span className="mono wrap">{address}</span>
        </Row>
        <Row k="cluster node">
          {node ? (
            <a href="#" onClick={(ev) => (ev.preventDefault(), select({ type: "node", node: node.name }))}>
              {node.name}
            </a>
          ) : (
            "outside this cluster (or not resolved yet)"
          )}
        </Row>
      </div>
      <h5>Events naming this peer</h5>
      <RecentEvents q={{ corr: address, newest: true, limit: 50 }} compact />
    </Section>
  );
}

function CorrOverview({ kind, id }: { kind: string; id: string }) {
  const { data } = usePoll((s) => (kind === "chunk" ? api.chunk(id, s) : Promise.resolve(null)), [kind, id]);
  return (
    <>
      <Section title={kind}>
        <div className="kv">
          <Row k="id">
            <span className="mono wrap">{id}</span>
          </Row>
        </div>
        {kind === "chunk" && <div className="corr-label approximate">approximate: matched by address in logs and events</div>}
      </Section>
      <Section title="Events">
        <RecentEvents q={{ corr: id, newest: true, limit: 100 }} compact />
      </Section>
      {data?.logs && data.logs.length > 0 && (
        <Section title="Log lines">
          <ul className="loglines">
            {data.logs.map((l) => (
              <li key={l.id} className={"lvl-" + l.line.level}>
                <span className="mono">{fmtTime(l.line.time, true)}</span> {l.node} <span className="muted">{l.line.logger}</span> {l.line.msg}
              </li>
            ))}
          </ul>
        </Section>
      )}
    </>
  );
}

export function RecentEvents({ q, filter, compact }: { q: EventQuery; filter?: (e: BeeEvent) => boolean; compact?: boolean }) {
  const select = useStore((s) => s.select);
  const { data, error, loading } = usePoll((s) => api.events(q, s), [JSON.stringify(q)], 3000);
  const evs = (data ?? []).filter(filter ?? (() => true)).reverse();
  if (error) return <div className="warn-text small">{error}</div>;
  if (loading && !data) return <div className="muted small">Loading…</div>;
  if (!evs.length) return <div className="muted small">No events.</div>;
  return (
    <ul className={"mini-events" + (compact ? " compact" : "")}>
      {evs.map((e) => (
        <li key={e.seq} onClick={() => select({ type: "event", seq: e.seq })} title={`${e.kind} · ${e.source.adapter} ${e.source.ref} · ${e.prov}`}>
          <SevBadge sev={e.sev} compact /> <span className="mono">{fmtTime(e.t)}</span> {!compact && e.node && <b>{e.node}</b>} {e.summary}{" "}
          <ProvBadge prov={e.prov} compact />
        </li>
      ))}
    </ul>
  );
}

function Logs({ sel }: { sel: Selection }) {
  const [q, setQ] = useState("");
  const [level, setLevel] = useState("debug");
  const p: Parameters<typeof api.logs>[0] = { limit: 300, level, q: q || undefined };
  if (sel.type === "node") p.node = sel.node;
  else if (sel.type === "component") {
    p.node = sel.node;
    p.component = sel.component;
  } else if (sel.type === "peer") p.q = q || sel.address;
  else if (sel.type === "corr") p.q = q || sel.id;
  else if (sel.type === "edge") p.node = sel.a;
  const { data, error } = usePoll((s) => api.logs(p, s), [JSON.stringify(p)], 3000);
  const lines = [...(data ?? [])].reverse();
  return (
    <div>
      <div className="row">
        <input placeholder="filter text" value={q} onChange={(e) => setQ(e.target.value)} />
        <select value={level} onChange={(e) => setLevel(e.target.value)}>
          {["debug", "info", "warning", "error"].map((l) => (
            <option key={l}>{l}</option>
          ))}
        </select>
      </div>
      <div className="muted small">Source: pod log stream (kube API) · observed · newest first</div>
      {error && <div className="warn-text small">{error}</div>}
      {!error && lines.length === 0 && <div className="muted small">No log lines (log streaming may be off for this node).</div>}
      <ul className="loglines">
        {lines.map((l) => (
          <li key={l.id} className={"lvl-" + l.line.level} title={l.line.raw}>
            <span className="mono">{fmtTime(l.line.time, true)}</span> <span className={"lvl lvl-" + l.line.level}>{l.line.level}</span>{" "}
            {sel.type !== "node" && sel.type !== "component" && <b>{l.node} </b>}
            <span className="muted">{l.line.logger}</span> {l.line.msg}
            {l.line.error && <span className="warn-text"> error={l.line.error}</span>}
            {l.line.fields && (
              <span className="fields">
                {Object.entries(l.line.fields)
                  .slice(0, 6)
                  .map(([k, v]) => ` ${k}=${v.length > 20 ? v.slice(0, 20) + "…" : v}`)
                  .join("")}
              </span>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}

function Raw({ sel, snap }: { sel: Selection; snap: Snapshot | null }) {
  if (sel.type === "node" || sel.type === "component") {
    return <NodeRaw node={sel.node} snap={snap} comp={sel.type === "component" ? sel.component : undefined} />;
  }
  if (sel.type === "edge") {
    const e = snap?.edges?.find((x) => (x.a === sel.a && x.b === sel.b) || (x.a === sel.b && x.b === sel.a));
    return <pre className="raw">{JSON.stringify(e ?? null, null, 2)}</pre>;
  }
  if (sel.type === "event") return <EventRaw seq={sel.seq} />;
  return <div className="muted">No raw payload for this selection.</div>;
}

function EventRaw({ seq }: { seq: number }) {
  const cached = useStore((s) => s.events.find((e) => e.seq === seq));
  return <pre className="raw">{JSON.stringify(cached ?? `event ${seq} is not buffered; see the overview tab`, null, 2)}</pre>;
}

function NodeRaw({ node, snap, comp }: { node: string; snap: Snapshot | null; comp?: string }) {
  const { data } = usePoll((s) => api.node(node, s), [node]);
  const [open, setOpen] = useState<string | null>(null);
  const state = comp ? snap?.nodes[node]?.components?.[comp] : snap?.nodes[node];
  return (
    <div>
      <details>
        <summary>{comp ? "Component state" : "Node state"} (from the snapshot)</summary>
        <pre className="raw">{JSON.stringify(state ?? null, null, 2)}</pre>
      </details>
      <h5>Last raw API responses</h5>
      {!data && <div className="muted small">Loading…</div>}
      {data && Object.keys(data.rawRefs).length === 0 && <div className="muted small">None retained.</div>}
      {data &&
        Object.entries(data.rawRefs)
          .sort(([a], [b]) => a.localeCompare(b))
          .map(([ref, id]) => (
            <details key={ref} open={open === ref} onToggle={(e) => (e.currentTarget.open ? setOpen(ref) : open === ref && setOpen(null))}>
              <summary>
                {ref} <span className="muted small">#{id}</span>
              </summary>
              {open === ref && <RawRef id={id} />}
            </details>
          ))}
    </div>
  );
}

function RawRef({ id }: { id: number }) {
  const { data, error } = usePoll((s) => api.raw(id, s), [id]);
  if (error) return <div className="warn-text small">{error} (raw payloads are evicted after a while)</div>;
  if (data === null) return <div className="muted small">Loading…</div>;
  let pretty = data;
  try {
    pretty = JSON.stringify(JSON.parse(data), null, 2);
  } catch {
    /* not JSON */
  }
  return <pre className="raw">{pretty.length > 200_000 ? pretty.slice(0, 200_000) + "\n…truncated" : pretty}</pre>;
}

