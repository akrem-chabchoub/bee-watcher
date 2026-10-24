// Turns raw API / Bee / kube error text into a sentence a person can act on.
// The raw text stays available as `detail`.

export interface Friendly {
  text: string;
  detail: string;
}

/** Pulls the message out of JSON error bodies ({"error"}, {"message"}, kube Status). */
export function rawMessage(e: unknown): string {
  let s = e instanceof Error ? e.message : typeof e === "string" ? e : JSON.stringify(e);
  for (let i = 0; i < 2; i++) {
    const t = s.trim();
    if (!t.startsWith("{")) break;
    try {
      const j = JSON.parse(t) as Record<string, unknown>;
      const m = j.error ?? j.message;
      if (typeof m === "string" && m) s = m;
      else break;
    } catch {
      break;
    }
  }
  return s;
}

const rules: [RegExp, string][] = [
  [/no endpoints available for service/i, "The node is not running (its pod is down, restarting or still starting)."],
  [/service unavailable|\b503\b/i, "The node is not available right now. It may be starting or restarting."],
  [/connection refused|ECONNREFUSED/i, "Could not connect. The cluster or node is probably stopped."],
  [/context .* not found|does not exist yet/i, "The k3d cluster does not exist yet. Create it in the Cluster tab."],
  [/timed? ?out|deadline exceeded/i, "The request took too long. The node may be busy or unreachable."],
  [/insufficient (amount|balance|funds)|not enough/i, "The node does not have enough BZZ or ETH for this."],
  [/batch.*not usable|not usable|unusable/i, "The postage batch is not usable yet. Wait a few blocks after buying it."],
  [/batch.*(not found|expired)/i, "The postage batch was not found or has expired."],
  [/bucket.*full|batch.*full|overissued/i, "The postage batch is full. Top it up, dilute it or buy a new one."],
  [/syncing|not ready|warming up/i, "The node is still getting ready (syncing). Try again in a moment."],
  [/another job is running/i, "Another cluster job is running. Wait for it to finish or cancel it."],
  [/unknown node/i, "That node is not known (yet). It may still be starting."],
  [/network error|failed to fetch/i, "bee-scope could not be reached. Is it still running?"],
];

export function friendly(e: unknown): Friendly {
  const detail = rawMessage(e);
  for (const [re, text] of rules) if (re.test(detail)) return { text, detail };
  const short = detail.length > 160 ? detail.slice(0, 157) + "…" : detail;
  return { text: short.charAt(0).toUpperCase() + short.slice(1), detail };
}
