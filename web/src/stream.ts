import { api } from "./api";
import { flowBus, useStore } from "./store";
import type { BeeEvent, FlowFrame, Snapshot } from "./types";

/**
 * Connects to /api/stream. The browser reconnects on its own and sends
 * Last-Event-ID, so the server replays what was missed; events are
 * de-duplicated by seq in the store.
 */
export function startStream(): () => void {
  const st = useStore.getState();
  api.meta().then(st.setMeta).catch(() => undefined);
  api.components().then(st.setRegistry).catch(() => undefined);

  const es = new EventSource("/api/stream");
  st.setSSE("connecting");
  es.onopen = () => useStore.getState().setSSE("open");
  es.onerror = () => {
    useStore.getState().setSSE(es.readyState === EventSource.CLOSED ? "closed" : "connecting");
  };
  es.addEventListener("hello", () => {
    // Meta can change between runs of the server (sim vs live).
    api.meta().then(useStore.getState().setMeta).catch(() => undefined);
  });
  es.addEventListener("snapshot", (m) => {
    useStore.getState().onSnapshot(JSON.parse((m as MessageEvent).data) as Snapshot);
  });
  es.addEventListener("events", (m) => {
    const evs = JSON.parse((m as MessageEvent).data) as BeeEvent[] | null;
    if (evs?.length) useStore.getState().onEvents(evs);
  });
  es.addEventListener("flows", (m) => {
    const f = JSON.parse((m as MessageEvent).data) as FlowFrame;
    if (f?.flows?.length) flowBus.push(f);
  });
  es.addEventListener("gap", (m) => {
    useStore.getState().setGap(JSON.parse((m as MessageEvent).data));
  });

  // Seed the timeline with recent history (the stream only replays what
  // happened after Last-Event-ID, which is nothing on first load).
  api
    .events({ newest: true, limit: 2000 })
    .then((evs) => useStore.getState().onEvents(evs))
    .catch(() => undefined);

  return () => es.close();
}
