import { useEffect } from "react";
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
import type { Tab } from "./store";

const keyTabs: Record<string, Tab> = { "1": "topology", "2": "node", "3": "timeline", "4": "operations", "5": "problems" };

export function App() {
  const tab = useStore((s) => s.tab);
  const meta = useStore((s) => s.meta);
  const liveMode = useStore((s) => s.live?.mode);
  const sim = meta?.mode === "sim" || liveMode === "sim";

  useEffect(() => startStream(), []);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const st = useStore.getState();
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        st.setSearchOpen(!st.searchOpen);
        return;
      }
      const t = e.target as HTMLElement;
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
          {tab === "topology" && <TopologyView />}
          {tab === "node" && <NodeView />}
          {tab === "timeline" && <TimelineView />}
          {tab === "operations" && <OperationsView />}
          {tab === "problems" && <ProblemsView />}
        </div>
        <Inspector />
      </main>
      <TimelineStrip />
      <SearchPalette />
    </div>
  );
}
