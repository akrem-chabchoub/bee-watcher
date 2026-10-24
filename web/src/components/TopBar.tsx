import { useStore, useView } from "../store";
import { fmtTime, num } from "../format";
import { useNow } from "./Badges";
import type { Tab } from "../store";
import { useUI } from "../ui";
import { QuitButton } from "./Feedback";

const tabs: { id: Tab; label: string; short?: string; key: string }[] = [
  { id: "topology", label: "Topology", short: "Map", key: "1" },
  { id: "node", label: "Inside a node", short: "Node", key: "2" },
  { id: "timeline", label: "Timeline", short: "Time", key: "3" },
  { id: "operations", label: "Operations", short: "Ops", key: "4" },
  { id: "problems", label: "Problems", short: "Issues", key: "5" },
  { id: "cluster", label: "Cluster", key: "6" },
  { id: "data", label: "Data", key: "7" },
];

export function TopBar() {
  const meta = useStore((s) => s.meta);
  const live = useStore((s) => s.live);
  const liveAt = useStore((s) => s.liveAt);
  const sse = useStore((s) => s.sse);
  const paused = useStore((s) => s.paused);
  const cursor = useStore((s) => s.cursor);
  const keyframe = useStore((s) => s.keyframe);
  const keyframeLoading = useStore((s) => s.keyframeLoading);
  const tab = useStore((s) => s.tab);
  const setTab = useStore((s) => s.setTab);
  const togglePause = useStore((s) => s.togglePause);
  const goLive = useStore((s) => s.goLive);
  const setSearchOpen = useStore((s) => s.setSearchOpen);
  const view = useView();
  const now = useNow();
  const setGlossaryOpen = useUI((s) => s.setGlossaryOpen);

  const mode = meta?.mode ?? live?.mode ?? "live";
  const age = liveAt ? now - liveAt : Infinity;
  const fresh = sse === "open" && age < 3000;

  let badge: { cls: string; text: string; title: string };
  if (cursor !== null) {
    badge = {
      cls: "mode-paused",
      text: `PAUSED at ${fmtTime(cursor)}`,
      title: keyframe ? `showing keyframe from ${fmtTime(keyframe.at)}` : "loading keyframe…",
    };
  } else if (paused) {
    badge = { cls: "mode-paused", text: `PAUSED at ${fmtTime(view?.at)}`, title: "view frozen; collection continues" };
  } else if (mode === "replay") {
    badge = { cls: "mode-replay", text: "REPLAY", title: "replaying a recording" };
  } else {
    badge = { cls: "mode-live", text: "LIVE", title: "following the cluster" };
  }

  const issues = (view?.issues ?? []).filter((i) => !i.resolvedAt);
  const c = live?.collector;
  const issuesBad = issues.filter((i) => i.sev === "error").length;

  return (
    <header className="topbar">
      <div className="brand" title="bee-scope: visual debugger for a local Bee cluster">
        <span className="hex" aria-hidden>
          ⬢
        </span>
        bee-scope
        <span className="target">{meta?.target ?? live?.target ?? ""}</span>
      </div>
      <span className={"mode " + badge.cls} title={badge.title}>
        {badge.text}
        {keyframeLoading && cursor !== null ? " …" : ""}
      </span>
      {mode === "sim" && (
        <span className="mode mode-sim" title="Every node, event and number is produced by the simulator. Nothing is real telemetry.">
          ⚙ SIMULATION
        </span>
      )}
      <button className="btn" onClick={togglePause} title="Freeze the view (collection continues). Space">
        {paused || cursor !== null ? "▶ Resume" : "❚❚ Pause"}
      </button>
      {cursor !== null && (
        <button className="btn" onClick={goLive}>
          ⏭ Live
        </button>
      )}
      <nav className="tabs" role="tablist">
        {tabs.map((t) => (
          <button
            key={t.id}
            role="tab"
            aria-selected={tab === t.id}
            className={"tab" + (tab === t.id ? " on" : "")}
            onClick={() => setTab(t.id)}
            title={`${t.label} (${t.key})`}
          >
            {t.short ? (
              <>
                <span className="tab-long">{t.label}</span>
                <span className="tab-short">{t.short}</span>
              </>
            ) : (
              t.label
            )}
            {t.id === "problems" && issues.length > 0 && (
              <span className={"count" + (issuesBad ? " bad" : "")}>{issues.length}</span>
            )}
          </button>
        ))}
      </nav>
      <div className="spacer" />
      <button className="btn search-btn" onClick={() => setSearchOpen(true)} title="Search nodes, peers, chunks, batches, traces (⌘K)">
        ⌕ Search <kbd>⌘K</kbd>
      </button>
      <button className="btn ghost icon-btn" onClick={() => setGlossaryOpen(true)} title="Glossary: what does this Bee term mean?" aria-label="Open glossary">
        ?
      </button>
      <div className={"conn " + (fresh ? "ok" : sse === "open" ? "warn" : "bad")} title={`stream: ${sse}`}>
        <span aria-hidden>{fresh ? "●" : sse === "open" ? "◐" : "✕"}</span>
        {sse === "open" ? (liveAt ? `snapshot ${age < 1500 ? "now" : Math.round(age / 1000) + " s ago"}` : "waiting…") : sse}
      </div>
      {c && (
        <div className="stats" title={`observations ${c.observations} · goroutines ${c.goroutines} · backlog ${c.obsBacklog} · subscribers ${c.subscribers}`}>
          <span>{num(c.eventsStored)} ev</span>
          <span>{c.heapMb.toFixed(0)} MB</span>
          {c.logDropped > 0 && <span className="warn-text">⚠ {num(c.logDropped)} log drops</span>}
        </div>
      )}
      <QuitButton />
    </header>
  );
}
