import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { entry, match, search, type GlossaryEntry } from "../glossary";
import { useUI, type Level } from "../ui";

/** A Bee term in the UI: dotted underline; click, tap or right-click explains it. */
export function Term({ id, children, title }: { id: string; children?: ReactNode; title?: string }) {
  const openExplain = useUI((s) => s.openExplain);
  const g = entry(id);
  return (
    <span
      className="term"
      data-term={id}
      role="button"
      tabIndex={0}
      title={title ?? (g ? `What is “${g.term}”? Click to explain` : undefined)}
      onClick={(e) => {
        e.stopPropagation();
        const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
        openExplain(id, r.left, r.bottom + 6);
      }}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
          openExplain(id, r.left, r.bottom + 6);
        }
      }}
    >
      {children ?? g?.term ?? id}
    </span>
  );
}

function LevelToggle() {
  const level = useUI((s) => s.level);
  const setLevel = useUI((s) => s.setLevel);
  const opt = (l: Level, label: string, hint: string) => (
    <button type="button" role="radio" aria-checked={level === l} className={"seg" + (level === l ? " on" : "")} onClick={() => setLevel(l)} title={hint}>
      {label}
    </button>
  );
  return (
    <div className="segmented" role="radiogroup" aria-label="Explanation level">
      {opt("kid", "🐝 Simple", "Explain it like I'm new")}
      {opt("engineer", "⚙ Engineer", "Bee internals: packages, endpoints, metrics")}
    </div>
  );
}

function ExplainBody({ g, onPick }: { g: GlossaryEntry; onPick: (id: string) => void }) {
  const level = useUI((s) => s.level);
  return (
    <>
      <p className={"explain-text " + level}>{level === "kid" ? g.kid : g.engineer}</p>
      {(g.seeAlso ?? []).length > 0 && (
        <div className="explain-see">
          <span className="muted small">See also</span>
          <div className="chips">
            {(g.seeAlso ?? []).map((id) => {
              const s = entry(id);
              return s ? (
                <button key={id} type="button" className="chip" onClick={() => onPick(id)}>
                  {s.term}
                </button>
              ) : null;
            })}
          </div>
        </div>
      )}
    </>
  );
}

/** Mounted once: handles right-click explain and renders the popover. */
export function ExplainLayer() {
  const explain = useUI((s) => s.explain);
  const openExplain = useUI((s) => s.openExplain);
  const close = useUI((s) => s.closeExplain);

  useEffect(() => {
    const onContext = (e: MouseEvent) => {
      const target = e.target as HTMLElement | null;
      if (!target || target.closest("input, textarea, [contenteditable=true], .explain-pop")) return;
      let id: string | undefined = (target.closest("[data-term]") as HTMLElement | null)?.dataset.term;
      if (!id) {
        const sel = window.getSelection()?.toString() ?? "";
        id = (sel.trim() && match(sel)?.id) || undefined;
      }
      if (!id) {
        // Text of the clicked element itself (short labels only, not whole panels).
        const own = (target.innerText || target.textContent || "").trim();
        if (own && own.length <= 80) id = match(own)?.id;
      }
      if (!id) return; // let the browser menu show
      e.preventDefault();
      openExplain(id, e.clientX, e.clientY);
    };
    document.addEventListener("contextmenu", onContext);
    return () => document.removeEventListener("contextmenu", onContext);
  }, [openExplain]);

  if (!explain) return null;
  const g = entry(explain.id);
  if (!g) return null;
  return <ExplainPopover g={g} x={explain.x} y={explain.y} onClose={close} onPick={(id) => openExplain(id, explain.x, explain.y)} />;
}

function ExplainPopover({ g, x, y, onClose, onPick }: { g: GlossaryEntry; x: number; y: number; onClose: () => void; onPick: (id: string) => void }) {
  const box = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState({ left: x, top: y });
  const [q, setQ] = useState("");
  const hits = q.trim() ? search(q).slice(0, 6) : [];
  const setGlossaryOpen = useUI((s) => s.setGlossaryOpen);

  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    const r = el.getBoundingClientRect();
    const vw = window.innerWidth;
    const vh = window.innerHeight;
    let left = Math.min(x, vw - r.width - 8);
    let top = y + r.height + 8 > vh ? Math.max(8, y - r.height - 8) : y;
    left = Math.max(8, left);
    top = Math.max(8, Math.min(top, vh - r.height - 8));
    setPos({ left, top });
  }, [x, y, g.id]);

  useEffect(() => {
    box.current?.querySelector<HTMLElement>(".segmented .seg.on")?.focus();
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && (e.stopPropagation(), onClose());
    const onDown = (e: MouseEvent) => {
      if (box.current && !box.current.contains(e.target as Node) && !(e.target as HTMLElement).closest?.("[data-term]")) onClose();
    };
    window.addEventListener("keydown", onKey, true);
    window.addEventListener("mousedown", onDown);
    return () => {
      window.removeEventListener("keydown", onKey, true);
      window.removeEventListener("mousedown", onDown);
    };
  }, [onClose, g.id]);

  return (
    <div
      ref={box}
      className="explain-pop"
      role="dialog"
      aria-label={`Explain ${g.term}`}
      style={{ left: pos.left, top: pos.top }}
      onKeyDown={(e) => {
        // Light focus trap: keep Tab inside the popover.
        if (e.key !== "Tab" || !box.current) return;
        const f = [...box.current.querySelectorAll<HTMLElement>("button, input")];
        if (!f.length) return;
        const first = f[0];
        const last = f[f.length - 1];
        if (e.shiftKey && document.activeElement === first) (e.preventDefault(), last.focus());
        else if (!e.shiftKey && document.activeElement === last) (e.preventDefault(), first.focus());
      }}
    >
      <div className="explain-head">
        <h3>{g.term}</h3>
        <button className="btn ghost" onClick={onClose} aria-label="Close">
          ✕
        </button>
      </div>
      <LevelToggle />
      <ExplainBody g={g} onPick={onPick} />
      <div className="explain-search">
        <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search glossary…" aria-label="Search glossary" />
        {hits.length > 0 && (
          <ul>
            {hits.map((h) => (
              <li key={h.id}>
                <button type="button" onClick={() => (setQ(""), onPick(h.id))}>
                  {h.term}
                </button>
              </li>
            ))}
          </ul>
        )}
        <button
          type="button"
          className="btn ghost small"
          onClick={() => {
            onClose();
            setGlossaryOpen(true);
          }}
        >
          Open full glossary
        </button>
      </div>
    </div>
  );
}

/** Full glossary browser (top bar "?"). */
export function GlossaryBrowser() {
  const open = useUI((s) => s.glossaryOpen);
  const setOpen = useUI((s) => s.setGlossaryOpen);
  const [q, setQ] = useState("");
  const [cur, setCur] = useState<string>("postage-stamp");
  const list = search(q);
  const g = entry(cur) ?? list[0];
  const input = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!open) return;
    setTimeout(() => input.current?.focus(), 0);
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, setOpen]);

  if (!open) return null;
  return (
    <div className="overlay" onMouseDown={() => setOpen(false)}>
      <div className="glossary" role="dialog" aria-label="Glossary" onMouseDown={(e) => e.stopPropagation()}>
        <div className="glossary-head">
          <h2>Glossary</h2>
          <LevelToggle />
          <div className="spacer" />
          <button className="btn ghost" onClick={() => setOpen(false)} aria-label="Close">
            ✕
          </button>
        </div>
        <p className="muted small glossary-tip">Tip: right-click (or long-press) any Bee term in the app to get it explained.</p>
        <div className="glossary-body">
          <div className="glossary-list">
            <input ref={input} value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search terms…" aria-label="Search terms" />
            <ul>
              {list.map((x) => (
                <li key={x.id}>
                  <button type="button" className={x.id === g?.id ? "on" : ""} onClick={() => setCur(x.id)}>
                    {x.term}
                  </button>
                </li>
              ))}
              {list.length === 0 && <li className="muted">no match</li>}
            </ul>
          </div>
          <div className="glossary-entry">
            {g ? (
              <>
                <h3>{g.term}</h3>
                <ExplainBody g={g} onPick={setCur} />
              </>
            ) : null}
          </div>
        </div>
      </div>
    </div>
  );
}
