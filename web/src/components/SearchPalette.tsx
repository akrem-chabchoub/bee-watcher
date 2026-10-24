import { useEffect, useRef, useState } from "react";
import { api } from "../api";
import { useStore } from "../store";
import type { SearchResult, Selection } from "../types";
import { search as searchGlossary, type GlossaryEntry } from "../glossary";
import { useUI } from "../ui";

type Item = { kind: "glossary"; g: GlossaryEntry } | { kind: "result"; r: SearchResult };

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

  const gl = q.trim().length >= 2 ? searchGlossary(q).slice(0, 3) : [];
  const items: Item[] = [...gl.map((g) => ({ kind: "glossary" as const, g })), ...results.map((r) => ({ kind: "result" as const, r }))];

  const pick = (it: Item) => {
    if (it.kind === "result") return choose(it.r);
    setOpen(false);
    useUI.getState().openExplain(it.g.id, Math.max(16, window.innerWidth / 2 - 180), window.innerHeight * 0.2);
  };

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
          placeholder="Node, peer, component, chunk / batch address, trace, tag — or a Bee term…"
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Escape") setOpen(false);
            else if (e.key === "ArrowDown") {
              e.preventDefault();
              setIdx((i) => Math.min(i + 1, items.length - 1));
            } else if (e.key === "ArrowUp") {
              e.preventDefault();
              setIdx((i) => Math.max(i - 1, 0));
            } else if (e.key === "Enter" && items[idx]) pick(items[idx]);
          }}
        />
        <ul className="palette-results">
          {err && <li className="muted">search failed: {err}</li>}
          {!err && q.trim() && items.length === 0 && <li className="muted">no match</li>}
          {items.map((it, i) =>
            it.kind === "glossary" ? (
              <li key={"g:" + it.g.id} className={i === idx ? "on" : ""} onMouseEnter={() => setIdx(i)} onClick={() => pick(it)}>
                <span className="ptype" aria-hidden>
                  💡
                </span>
                <span className="plabel">{it.g.term}</span>
                <span className="ptag">glossary</span>
                <span className="pdetail">{it.g.kid}</span>
              </li>
            ) : (
              <li key={it.r.type + it.r.id} className={i === idx ? "on" : ""} onMouseEnter={() => setIdx(i)} onClick={() => pick(it)}>
                <span className="ptype" aria-hidden>
                  {typeIcon[it.r.type] ?? "•"}
                </span>
                <span className="plabel">{it.r.label}</span>
                <span className="ptag">{it.r.type}</span>
                {it.r.detail && <span className="pdetail">{it.r.detail}</span>}
              </li>
            ),
          )}
        </ul>
        <div className="palette-hint muted">↑↓ to move · Enter to open · Esc to close</div>
      </div>
    </div>
  );
}
