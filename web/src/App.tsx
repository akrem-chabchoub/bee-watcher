import { useEffect, useRef } from "react";
import { startStream } from "./stream";
import { useStore } from "./store";
import { TopBar } from "./components/TopBar";
import { SearchPalette } from "./components/SearchPalette";
import { TimelineStrip } from "./components/TimelineStrip";
import { Inspector } from "./components/Inspector";
import { TopologyView } from "./views/TopologyView";
import { NodeView } from "./views/NodeView";
import { TimelineView } from "./views/TimelineView";
import { OperationsView } from "./views/OperationsView";
import { ProblemsView } from "./views/ProblemsView";
import { ClusterView } from "./views/ClusterView";
import { DataView } from "./views/DataView";
import { startControlPoll, useControl } from "./control";
import type { Tab } from "./store";
import { ExplainLayer, GlossaryBrowser } from "./components/Explain";
import { GettingStarted, QuitScreen, TabIntro, Toaster } from "./components/Feedback";
import { useUI } from "./ui";

const keyTabs: Record<string, Tab> = { "1": "topology", "2": "node", "3": "timeline", "4": "operations", "5": "problems", "6": "cluster", "7": "data" };

export function App() {
  const tab = useStore((s) => s.tab);
  const meta = useStore((s) => s.meta);
  const liveMode = useStore((s) => s.live?.mode);
  const sim = meta?.mode === "sim" || liveMode === "sim";

  const quit = useUI((s) => s.quit);
  const stops = useRef<(() => void)[]>([]);
  useEffect(() => {
    const a = startStream();
    const b = startControlPoll();
    stops.current = [a, b];
    return () => (a(), b());
  }, []);
  // After Quit the server is gone: stop the stream and polls so nothing
  // reconnects or reports errors.
  useEffect(() => {
    if (quit) for (const f of stops.current) f();
  }, [quit]);

  // Live mode with no running cluster or no nodes: open the Cluster tab once.
  useEffect(() => {
    let done = false;
    return useControl.subscribe((c) => {
      if (done || !c.status) return;
      done = true;
      const st = c.status;
      const nodeless = !st.cluster.running || (st.nodes ?? []).length === 0;
      if (st.enabled && nodeless && useStore.getState().tab === "topology") useStore.getState().setTab("cluster");
    });
  }, []);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const st = useStore.getState();
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        st.setSearchOpen(!st.searchOpen);
        return;
      }
      const t = e.target as HTMLElement;
      if (useUI.getState().explain || useUI.getState().glossaryOpen) return;
      if (t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.tagName === "SELECT" || t.isContentEditable)) return;
      if (e.metaKey || e.ctrlKey || e.altKey) return;
      if (keyTabs[e.key]) st.setTab(keyTabs[e.key]);
      else if (e.key === " ") {
        e.preventDefault();
        st.togglePause();
      } else if (e.key === "Escape") {
        if (st.searchOpen) st.setSearchOpen(false);
        else st.select(null);
      } else if (e.key === "l" || e.key === "L") st.goLive();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  if (quit) return <QuitScreen />;

  return (
    <div className={"app" + (sim ? " sim" : "")}>
      {sim && (
        <div className="sim-banner" role="status">
          ⚙ SIMULATION — every node, event and number on this page is produced by the simulator. Nothing here is real telemetry.
        </div>
      )}
      <TopBar />
      <main className="main">
        <div className="workarea">
          <TabIntro />
          {(tab === "topology" || tab === "cluster") && <GettingStarted />}
          {tab === "topology" && <TopologyView />}
          {tab === "node" && <NodeView />}
          {tab === "timeline" && <TimelineView />}
          {tab === "operations" && <OperationsView />}
          {tab === "problems" && <ProblemsView />}
          {tab === "cluster" && <ClusterView />}
          {tab === "data" && <DataView />}
        </div>
        {tab !== "cluster" && tab !== "data" && <Inspector />}
      </main>
      <TimelineStrip />
      <SearchPalette />
      <GlossaryBrowser />
      <ExplainLayer />
      <Toaster />
    </div>
  );
}
