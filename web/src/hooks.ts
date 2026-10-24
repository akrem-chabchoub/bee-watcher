import { useEffect, useState } from "react";

export function useReducedMotion(): boolean {
  const q = "(prefers-reduced-motion: reduce)";
  const [r, setR] = useState(() => typeof window !== "undefined" && window.matchMedia(q).matches);
  useEffect(() => {
    const m = window.matchMedia(q);
    const on = () => setR(m.matches);
    m.addEventListener("change", on);
    return () => m.removeEventListener("change", on);
  }, []);
  return r;
}

/** Runs an async loader on mount, when deps change and every `everyMs`. */
export function usePoll<T>(load: (signal: AbortSignal) => Promise<T>, deps: unknown[], everyMs = 0): { data: T | null; error: string | null; loading: boolean } {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    let ac = new AbortController();
    let alive = true;
    const run = () => {
      ac.abort();
      ac = new AbortController();
      const sig = ac.signal;
      load(sig)
        .then((d) => {
          if (!alive || sig.aborted) return;
          setData(d);
          setError(null);
          setLoading(false);
        })
        .catch((e: unknown) => {
          if (!alive || sig.aborted) return;
          setError(e instanceof Error ? e.message : String(e));
          setLoading(false);
        });
    };
    setLoading(true);
    run();
    const t = everyMs > 0 ? setInterval(run, everyMs) : undefined;
    return () => {
      alive = false;
      ac.abort();
      if (t) clearInterval(t);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);
  return { data, error, loading };
}
