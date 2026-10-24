import { create } from "zustand";
import { api } from "./api";
import type { ControlStatus, Job } from "./types";
import { toast } from "./ui";
import { friendly } from "./errors";

interface ControlState {
  status: ControlStatus | null;
  error: string | null;
  loadedAt: number;
  /** Latest job started from this page, shown until another one starts. */
  focusJob: string | null;
  setFocusJob(id: string | null): void;
  refresh(): Promise<void>;
}

export const useControl = create<ControlState>((set) => ({
  status: null,
  error: null,
  loadedAt: 0,
  focusJob: null,
  setFocusJob: (focusJob) => set({ focusJob }),
  refresh: async () => {
    try {
      const status = await api.control.status();
      const prev = useControl.getState().status?.job;
      const j = status.job;
      // Announce when a job we saw running finishes.
      if (prev && j && prev.id === j.id && prev.state === "running" && j.state !== "running") {
        if (j.state === "succeeded") toast("ok", `${j.title}: done`);
        else if (j.state === "canceled") toast("info", `${j.title}: canceled`);
        else toast("bad", `${j.title} failed: ${friendly(j.error).text}`, j.error);
      }
      set({ status, error: null, loadedAt: Date.now() });
    } catch (e) {
      set({ error: e instanceof Error ? e.message : String(e), loadedAt: Date.now() });
    }
  },
}));

export function jobRunning(j: Job | null | undefined): boolean {
  return !!j && j.state === "running";
}

/** Polls the control status: every 3 s, every 1 s while a job runs. */
export function startControlPoll(): () => void {
  let alive = true;
  let t: ReturnType<typeof setTimeout> | undefined;
  const loop = async () => {
    await useControl.getState().refresh();
    if (!alive) return;
    t = setTimeout(loop, jobRunning(useControl.getState().status?.job) ? 1000 : 3000);
  };
  void loop();
  return () => {
    alive = false;
    if (t) clearTimeout(t);
  };
}

/** Starts a control action and focuses its job. Throws on HTTP errors. */
export async function runAction(name: string, body: Record<string, unknown> = {}): Promise<Job> {
  const job = await api.control.run(name, body);
  useControl.getState().setFocusJob(job.id);
  toast("info", `Started: ${job.title}`);
  void useControl.getState().refresh();
  return job;
}
