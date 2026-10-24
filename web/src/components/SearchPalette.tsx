import { useEffect, useRef, useState } from "react";
import { api } from "../api";
import { useStore } from "../store";
import type { SearchResult, Selection } from "../types";

const typeIcon: Record<string, string> = {
  node: "⬢",
  peer: "↔",
  component: "▣",
  chunk: "◆",
  batch: "✉",
  trace: "⋯",
  tag: "⇪",
  issue: "⚠",
  operation: "⚙",
};

/** Maps a search hit to the selection (and tab) that shows it best. */
export function selectionFor(r: SearchResult, fallbackNode: string | null): { sel: Selection; tab?: ReturnType<typeof useStore.getState>["tab"] } {
  switch (r.type) {
    case "node":
      return { sel: { type: "node", node: r.id } };
    case "peer":
      return { sel: { type: "peer", address: r.id, node: r.detail && !r.detail.includes(" ") ? r.detail : undefined } };
    case "component": {
      const [a, b] = r.id.includes("/") ? r.id.split("/", 2) : [fallbackNode ?? "", r.id];
      return { sel: { type: "component", node: a, component: b }, tab: "node" };
    }
    case "issue":
      return { sel: { type: "issue", id: r.id }, tab: "problems" };
    case "operation":
      return { sel: { type: "operation", id: r.id }, tab: "operations" };
    case "tag":
      if (r.id.startsWith("tag:")) return { sel: { type: "operation", id: r.id }, tab: "operations" };
      return { sel: { type: "corr", kind: "tag", id: r.id } };
    case "trace":
      if (r.id.startsWith("trace:")) return { sel: { type: "operation", id: r.id }, tab: "operations" };
      return { sel: { type: "corr", kind: "trace", id: r.id } };
    default:
      return { sel: { type: "corr", kind: r.type, id: r.id } };
  }
}

export function SearchPalette() {
  const open = useStore((s) => s.searchOpen);
  const setOpen = useStore((s) => s.setSearchOpen);
  const [q, setQ] = useState("");
  const [results, setResults] = useState<SearchResult[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [idx, setIdx] = useState(0);
  const input = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (open) {
      setIdx(0);
      setTimeout(() => input.current?.select(), 0);
    }
  }, [open]);

  useEffect(() => {
    if (!open || !q.trim()) {
      setResults([]);
      return;
    }
    const ac = new AbortController();
    const t = setTimeout(() => {
      api
        .search(q.trim(), ac.signal)
        .then((r) => {
          setResults(r.slice(0, 50));
          setErr(null);
          setIdx(0);
        })
        .catch((e: unknown) => {
          if (!ac.signal.aborted) setErr(e instanceof Error ? e.message : String(e));
        });
    }, 150);
    return () => {
      clearTimeout(t);
      ac.abort();
    };
  }, [q, open]);

  if (!open) return null;

  const choose = (r: SearchResult) => {
    const st = useStore.getState();
    const { sel, tab } = selectionFor(r, st.internalsNode);
    st.select(sel);
    if (tab) st.setTab(tab);
    setOpen(false);
  };

  return (
    <div className="overlay" onMouseDown={() => setOpen(false)}>
      <div className="palette" onMouseDown={(e) => e.stopPropagation()} role="dialog" aria-label="Search">
        <input
          ref={input}
          value={q}
          placeholder="Node, peer overlay, component, chunk / batch address, trace or tag id…"
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Escape") setOpen(false);
            else if (e.key === "ArrowDown") {
              e.preventDefault();
              setIdx((i) => Math.min(i + 1, results.length - 1));
            } else if (e.key === "ArrowUp") {
              e.preventDefault();
              setIdx((i) => Math.max(i - 1, 0));
            } else if (e.key === "Enter" && results[idx]) choose(results[idx]);
          }}
        />
        <ul className="palette-results">
          {err && <li className="muted">search failed: {err}</li>}
          {!err && q.trim() && results.length === 0 && <li className="muted">no match</li>}
          {results.map((r, i) => (
            <li key={r.type + r.id} className={i === idx ? "on" : ""} onMouseEnter={() => setIdx(i)} onClick={() => choose(r)}>
              <span className="ptype" aria-hidden>
                {typeIcon[r.type] ?? "•"}
              </span>
              <span className="plabel">{r.label}</span>
              <span className="ptag">{r.type}</span>
              {r.detail && <span className="pdetail">{r.detail}</span>}
            </li>
          ))}
        </ul>
        <div className="palette-hint muted">↑↓ to move · Enter to open · Esc to close</div>
      </div>
    </div>
  );
}
