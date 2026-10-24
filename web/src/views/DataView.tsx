import { useEffect, useMemo, useRef, useState } from "react";
import { api } from "../api";
import { useControl } from "../control";
import { usePoll } from "../hooks";
import { useStore } from "../store";
import { dur, fmtTime, short } from "../format";
import type { BeeStamp, Upload } from "../types";
import { Term } from "../components/Explain";
import { ErrorNote, Skeleton } from "../components/Feedback";
import { toast } from "../ui";
import { friendly } from "../errors";

/** 1234567890 → "1.23 B" (exact value in the title). */
function compact(v: string): string {
  const n = Number(v);
  if (!isFinite(n)) return v;
  return new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 2 }).format(n);
}

// Assumed block time of the local chain, used only for "≈" TTL estimates.
const BLOCK_S = 5;

interface Addresses {
  overlay?: string;
  ethereum?: string;
  underlay?: string[];
}
interface Wallet {
  bzzBalance?: string;
  nativeTokenBalance?: string;
  chequebookContractAddress?: string;
  walletAddress?: string;
}
interface ChequebookBalance {
  totalBalance?: string;
  availableBalance?: string;
}
interface ChainState {
  currentPrice?: string;
  block?: number;
  chainTip?: number;
}
interface Tag {
  uid: number;
  split: number;
  seen: number;
  stored: number;
  sent: number;
  synced: number;
}

const errText = (e: unknown) => (e instanceof Error ? e.message : String(e));

/** Formats an integer string with `decimals` implied decimals (PLUR→BZZ: 16, wei→ETH: 18). */
export function units(v: string | number | bigint | undefined | null, decimals: number, digits = 4): string {
  if (v === undefined || v === null || v === "") return "—";
  let n: bigint;
  try {
    n = BigInt(typeof v === "number" ? Math.trunc(v) : v);
  } catch {
    return String(v);
  }
  const neg = n < 0n;
  if (neg) n = -n;
  const base = 10n ** BigInt(decimals);
  const whole = n / base;
  let frac = (n % base).toString().padStart(decimals, "0").slice(0, digits).replace(/0+$/, "");
  if (!frac && n % base !== 0n && whole === 0n) frac = "0".repeat(digits - 1) + "1"; // tiny but not zero
  return (neg ? "-" : "") + whole.toLocaleString() + (frac ? "." + frac : "");
}

function humanTTL(s: number): string {
  if (!isFinite(s) || s < 0) return "—";
  if (s < 3600) return dur(s * 1000);
  const h = s / 3600;
  if (h < 48) return `${h.toFixed(1)} h`;
  return `${(h / 24).toFixed(1)} days`;
}

function Copy({ text, label = "copy" }: { text: string; label?: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      className="btn ghost copy-btn"
      title={`copy ${text}`}
      onClick={() => {
        void navigator.clipboard?.writeText(text).then(() => {
          setDone(true);
          setTimeout(() => setDone(false), 1200);
        });
      }}
    >
      {done ? "✔ copied" : "⎘ " + label}
    </button>
  );
}

export function DataView() {
  const status = useControl((s) => s.status);
  const live = useStore((s) => s.live);
  const names = useMemo(
    () => Object.values(live?.nodes ?? {}).sort((a, b) => a.name.localeCompare(b.name, undefined, { numeric: true })),
    [live],
  );
  const [node, setNode] = useState<string>("");
  // Prefer a full or light node that is up; bootnodes cannot hold stamps usefully.
  const fallback =
    names.find((n) => n.group !== "bootnode" && n.phase === "up")?.name ??
    names.find((n) => n.group !== "bootnode")?.name ??
    names[0]?.name ??
    "";
  const current = node && names.some((n) => n.name === node) ? node : fallback;
  const [stampsVersion, setStampsVersion] = useState(0);

  if (status && !status.enabled) {
    return (
      <div className="view data">
        <div className="control-disabled" role="status">
          ⊘ Data operations are disabled: {status.disabledReason || "not available in this mode"}.
        </div>
      </div>
    );
  }
  if (!current) {
    return (
      <div className="view data">
        <div className="empty">
          No Bee nodes discovered yet.{" "}
          <button className="btn" onClick={() => useStore.getState().setTab("cluster")}>
            Open Cluster setup
          </button>
        </div>
      </div>
    );
  }
  const st = names.find((n) => n.name === current);

  return (
    <div className="view data">
      <div className="data-head">
        <label>
          Node{" "}
          <select value={current} onChange={(e) => setNode(e.target.value)}>
            {names.map((n) => (
              <option key={n.name} value={n.name}>
                {n.name}
                {n.phase !== "up" ? ` (${n.phase})` : ""}
              </option>
            ))}
          </select>
        </label>
        {st && st.phase !== "up" && <span className="warn-text">▲ {current} is {st.phase}; requests may fail.</span>}
        {st?.mode && <span className="muted">mode {st.mode}</span>}
      </div>
      <div className="data-grid">
        <NodeCard node={current} />
        <StampsCard node={current} version={stampsVersion} bump={() => setStampsVersion((v) => v + 1)} />
        <UploadCard node={current} stampsVersion={stampsVersion} />
        <DownloadCard node={current} />
        <UploadsCard node={current} />
      </div>
    </div>
  );
}

// ---- Node card ----

function NodeCard({ node }: { node: string }) {
  const addr = usePoll((s) => api.data.get<Addresses>(node, "/addresses", s), [node], 0);
  const wallet = usePoll((s) => api.data.get<Wallet>(node, "/wallet", s), [node], 10_000);
  const cheque = usePoll((s) => api.data.get<ChequebookBalance>(node, "/chequebook/balance", s), [node], 10_000);
  return (
    <section className="card">
      <div className="card-head">
        <h2>⬢ {node}</h2>
        <span className="muted small">identity and money</span>
      </div>
      {addr.error && wallet.error ? (
        <ErrorNote error={addr.error} prefix={`Cannot reach ${node}`} />
      ) : addr.loading && !addr.data ? (
        <Skeleton lines={4} />
      ) : (
      <div className="kv">
        <div className="kv-row">
          <span className="k">
            <Term id="overlay">overlay</Term>
          </span>
          <span className="v mono wrap">
            {addr.data?.overlay ?? (addr.error ? <ErrorNote error={addr.error} inline /> : "…")} {addr.data?.overlay && <Copy text={addr.data.overlay} />}
          </span>
        </div>
        <div className="kv-row">
          <span className="k">ethereum</span>
          <span className="v mono wrap">
            {addr.data?.ethereum ?? "—"} {addr.data?.ethereum && <Copy text={addr.data.ethereum} />}
          </span>
        </div>
        <div className="kv-row">
          <span className="k">
            <Term id="bzz">BZZ</Term> balance
          </span>
          <span className="v">{wallet.error ? <ErrorNote error={wallet.error} inline /> : wallet.data ? <b>{units(wallet.data.bzzBalance, 16)} BZZ</b> : "…"}</span>
        </div>
        <div className="kv-row">
          <span className="k">native balance</span>
          <span className="v">{wallet.data ? `${units(wallet.data.nativeTokenBalance, 18)} ETH` : "—"}</span>
        </div>
        {cheque.data && (
          <div className="kv-row">
            <span className="k">
              <Term id="chequebook">chequebook</Term>
            </span>
            <span className="v">
              {units(cheque.data.availableBalance, 16)} BZZ available of {units(cheque.data.totalBalance, 16)}
            </span>
          </div>
        )}
      </div>
      )}
    </section>
  );
}

// ---- Stamps ----

function StampsCard({ node, version, bump }: { node: string; version: number; bump: () => void }) {
  const stamps = usePoll((s) => api.data.get<{ stamps: BeeStamp[] | null }>(node, "/stamps", s), [node, version], 10_000);
  const chain = usePoll((s) => api.data.get<ChainState>(node, "/chainstate", s), [node], 30_000);
  const [open, setOpen] = useState<{ id: string; kind: "topup" | "dilute" } | null>(null);
  const list = stamps.data?.stamps ?? [];
  const price = chain.data?.currentPrice ? Number(chain.data.currentPrice) : 0;

  return (
    <section className="card span2">
      <div className="card-head">
        <h2>
          ✉ <Term id="postage-stamp">Postage stamps</Term>
        </h2>
        <span className="muted small">
          {list.length} <Term id="batch">batch{list.length === 1 ? "" : "es"}</Term>
          {price ? (
            <>
              {" "}
              · <Term id="chainstate">price</Term> {price} PLUR/chunk/block
            </>
          ) : (
            ""
          )}
        </span>
      </div>
      <p className="muted small card-sub">You need a usable batch to upload. Think of it as prepaid storage: size (depth) × time (amount).</p>
      {stamps.error && <ErrorNote error={stamps.error} prefix="Could not load stamps" />}
      {stamps.loading && !stamps.data && !stamps.error && <Skeleton lines={3} />}
      {list.length > 0 && (
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>
                  <Term id="batch">batch</Term>
                </th>
                <th>label</th>
                <th>
                  <Term id="batch-depth">depth</Term>
                </th>
                <th>
                  <Term id="amount">amount</Term>
                </th>
                <th>
                  <Term id="utilization">used</Term>
                </th>
                <th>
                  <Term id="usable">usable</Term>
                </th>
                <th>
                  <Term id="ttl">TTL</Term>
                </th>
                <th>
                  <Term id="immutable">immutable</Term>
                </th>
                <th />
              </tr>
            </thead>
            <tbody>
              {list.map((b) => {
                const cap = 2 ** Math.max(0, b.depth - b.bucketDepth);
                const used = cap ? Math.min(1, b.utilization / cap) : 0;
                return [
                  <tr key={b.batchID} className="static">
                    <td className="mono">
                      {short(b.batchID, 10)} <Copy text={b.batchID} />
                    </td>
                    <td>{b.label || <span className="muted">—</span>}</td>
                    <td className="num">{b.depth}</td>
                    <td className="num" title={`${b.amount} PLUR per chunk`}>
                      {compact(b.amount)}
                    </td>
                    <td>
                      <div className="meter" title={`${b.utilization} of ${cap} per bucket`}>
                        <div style={{ width: `${used * 100}%` }} />
                        <span>{(used * 100).toFixed(0)}%</span>
                      </div>
                    </td>
                    <td>{b.usable ? <span className="ok-text">✔ yes</span> : <span className="warn-text">… not yet</span>}</td>
                    <td>{humanTTL(b.batchTTL)}</td>
                    <td>{b.immutableFlag ? "yes" : "no"}</td>
                    <td className="row-actions">
                      <button className="btn" onClick={() => setOpen(open?.id === b.batchID && open.kind === "topup" ? null : { id: b.batchID, kind: "topup" })}>
                        ＋ Top up
                      </button>
                      <button className="btn" onClick={() => setOpen(open?.id === b.batchID && open.kind === "dilute" ? null : { id: b.batchID, kind: "dilute" })}>
                        ⇲ Dilute
                      </button>
                    </td>
                  </tr>,
                  open?.id === b.batchID && (
                    <tr key={b.batchID + "-form"} className="static">
                      <td colSpan={9}>
                        {open.kind === "topup" ? (
                          <TopupForm node={node} stamp={b} price={price} done={() => (setOpen(null), bump())} />
                        ) : (
                          <DiluteForm node={node} stamp={b} done={() => (setOpen(null), bump())} />
                        )}
                      </td>
                    </tr>
                  ),
                ];
              })}
            </tbody>
          </table>
        </div>
      )}
      {!stamps.error && !stamps.loading && list.length === 0 && <div className="muted">No batches on {node} yet. Buy one below to upload data.</div>}
      <BuyForm node={node} price={price} bump={bump} />
    </section>
  );
}

function ttlPresets(price: number): { label: string; amount: string }[] {
  if (!price) return [{ label: "100M", amount: "100000000" }, { label: "1B", amount: "1000000000" }];
  return [
    { label: "≈ 1 day", s: 86_400 },
    { label: "≈ 1 week", s: 7 * 86_400 },
    { label: "≈ 1 month", s: 30 * 86_400 },
  ].map((p) => ({ label: p.label, amount: String(Math.ceil(p.s / BLOCK_S) * price) }));
}

function BuyForm({ node, price, bump }: { node: string; price: number; bump: () => void }) {
  const presets = ttlPresets(price);
  const [depth, setDepth] = useState(20);
  const [amount, setAmount] = useState("");
  const [label, setLabel] = useState("");
  const [immutable, setImmutable] = useState(false);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const amt = amount || presets[0].amount;
  const valid = /^\d+$/.test(amt) && depth >= 17 && depth <= 30;
  const cost = valid ? BigInt(amt) * 2n ** BigInt(depth) : null;
  const ttl = valid && price ? (Number(amt) / price) * BLOCK_S : NaN;

  const buy = () => {
    setBusy(true);
    setMsg(null);
    api.data
      .buyStamp(node, { amount: amt, depth, label, immutable })
      .then((r) => {
        setMsg({ ok: true, text: `✔ Bought batch ${r.batchID}. It becomes usable after a few blocks.` });
        toast("ok", `Bought postage batch ${short(r.batchID, 10)} on ${node}`);
        bump();
      })
      .catch((e: unknown) => {
        setMsg({ ok: false, text: errText(e) });
        toast("bad", `Buying a batch failed: ${friendly(e).text}`, errText(e));
      })
      .finally(() => setBusy(false));
  };

  return (
    <div className="subform">
      <h3>Buy a batch</h3>
      <div className="form-grid">
        <label className="field">
          <span>
            <Term id="batch-depth">Depth</Term> <span className="muted small">— how much you can store</span>
          </span>
          <select value={depth} onChange={(e) => setDepth(Number(e.target.value))} disabled={busy}>
            {Array.from({ length: 14 }, (_, i) => 17 + i).map((d) => (
              <option key={d} value={d}>
                {d} — {fmtBytes(2 ** d * 4096)}
              </option>
            ))}
          </select>
        </label>
        <label className="field">
          <span>
            <Term id="amount">Amount</Term> <span className="muted small">— how long it lasts (PLUR per chunk)</span>
          </span>
          <input value={amt} onChange={(e) => setAmount(e.target.value.replace(/\D/g, ""))} disabled={busy} inputMode="numeric" />
          <div className="chips">
            {presets.map((p) => (
              <button key={p.label} type="button" className={"chip" + (p.amount === amt ? " on" : "")} onClick={() => setAmount(p.amount)} disabled={busy}>
                {p.label}
              </button>
            ))}
          </div>
        </label>
        <label className="field">
          Label
          <input value={label} onChange={(e) => setLabel(e.target.value)} disabled={busy} placeholder="optional" />
        </label>
        <label className="field check">
          <input type="checkbox" checked={immutable} onChange={(e) => setImmutable(e.target.checked)} disabled={busy} /> <Term id="immutable">Immutable</Term>{" "}
          <span className="muted small">(cannot be diluted; when full, rejects uploads instead of overwriting)</span>
        </label>
      </div>
      <div className="row">
        <button className="btn primary" disabled={busy || !valid} onClick={buy}>
          {busy ? <span className="spinner" aria-hidden /> : "＄"} {busy ? "Buying… waiting for the chain" : "Buy batch"}
        </button>
        {cost !== null && (
          <span className="muted small">
            cost {units(cost, 16)} BZZ{isFinite(ttl) ? ` · TTL ≈ ${humanTTL(ttl)} (assuming ${BLOCK_S} s blocks)` : ""}
          </span>
        )}
      </div>
      {busy && <div className="muted small">Bee waits for the transaction; this can take a minute.</div>}
      {msg && (msg.ok ? <div className="ok-text">{msg.text}</div> : <ErrorNote error={msg.text} />)}
    </div>
  );
}

function TopupForm({ node, stamp, price, done }: { node: string; stamp: BeeStamp; price: number; done: () => void }) {
  const [amount, setAmount] = useState(ttlPresets(price)[0].amount);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const valid = /^\d+$/.test(amount) && amount !== "0";
  return (
    <div className="inline-form">
      <span>Top up {short(stamp.batchID, 10)} by</span>
      <input value={amount} onChange={(e) => setAmount(e.target.value.replace(/\D/g, ""))} disabled={busy} inputMode="numeric" />
      <span className="muted small">PLUR/chunk · cost {valid ? units(BigInt(amount) * 2n ** BigInt(stamp.depth), 16) : "—"} BZZ</span>
      <button
        className="btn primary"
        disabled={busy || !valid}
        onClick={() => {
          setBusy(true);
          api.data
            .topup(node, stamp.batchID, amount)
            .then(() => {
              toast("ok", `Topped up ${short(stamp.batchID, 10)}`);
              done();
            })
            .catch((e: unknown) => setErr(errText(e)))
            .finally(() => setBusy(false));
        }}
      >
        {busy ? <span className="spinner" aria-hidden /> : null} {busy ? "Waiting for the chain…" : "Top up"}
      </button>
      {err && <ErrorNote error={err} inline />}
    </div>
  );
}

function DiluteForm({ node, stamp, done }: { node: string; stamp: BeeStamp; done: () => void }) {
  const [depth, setDepth] = useState(stamp.depth + 1);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  if (stamp.immutableFlag) return <div className="muted">Immutable batches cannot be diluted.</div>;
  return (
    <div className="inline-form">
      <span>Dilute {short(stamp.batchID, 10)} to depth</span>
      <select value={depth} onChange={(e) => setDepth(Number(e.target.value))} disabled={busy}>
        {Array.from({ length: 6 }, (_, i) => stamp.depth + 1 + i).map((d) => (
          <option key={d} value={d}>
            {d}
          </option>
        ))}
      </select>
      <span className="muted small">doubles capacity per step and halves the remaining TTL</span>
      <button
        className="btn primary"
        disabled={busy}
        onClick={() => {
          setBusy(true);
          api.data
            .dilute(node, stamp.batchID, depth)
            .then(() => {
              toast("ok", `Diluted ${short(stamp.batchID, 10)} to depth ${depth}`);
              done();
            })
            .catch((e: unknown) => setErr(errText(e)))
            .finally(() => setBusy(false));
        }}
      >
        {busy ? <span className="spinner" aria-hidden /> : null} {busy ? "Waiting for the chain…" : "Dilute"}
      </button>
      {err && <ErrorNote error={err} inline />}
    </div>
  );
}

function fmtBytes(n: number): string {
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) {
    n /= 1024;
    i++;
  }
  return `${n < 10 && i ? n.toFixed(1) : Math.round(n)} ${u[i]}`;
}

// ---- Upload ----

interface UploadResult {
  reference: string;
  tagUid: number;
  upload?: Upload;
}

function UploadCard({ node, stampsVersion }: { node: string; stampsVersion: number }) {
  const stamps = usePoll((s) => api.data.get<{ stamps: BeeStamp[] | null }>(node, "/stamps", s), [node, stampsVersion], 10_000);
  const usable = (stamps.data?.stamps ?? []).filter((b) => b.usable);
  const [batch, setBatch] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [opts, setOpts] = useState({ pin: true, encrypt: false, deferred: true, redundancy: 0 });
  const [drag, setDrag] = useState(false);
  const [progress, setProgress] = useState<number | null>(null);
  const [result, setResult] = useState<UploadResult | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const xhr = useRef<XMLHttpRequest | null>(null);
  const picker = useRef<HTMLInputElement>(null);
  const chosen = usable.find((b) => b.batchID === batch)?.batchID ?? usable[0]?.batchID ?? "";
  const uploading = progress !== null;

  useEffect(() => () => xhr.current?.abort(), []);

  const start = () => {
    if (!file || !chosen) return;
    setErr(null);
    setResult(null);
    setProgress(0);
    const x = new XMLHttpRequest();
    xhr.current = x;
    x.open("POST", api.data.uploadURL(node, { batch: chosen, name: file.name, ...opts }));
    x.setRequestHeader("Content-Type", file.type || "application/octet-stream");
    x.setRequestHeader("X-Bee-Scope", "upload");
    x.upload.onprogress = (e) => e.lengthComputable && setProgress(e.loaded / e.total);
    x.onload = () => {
      setProgress(null);
      let body: unknown = x.responseText;
      try {
        body = JSON.parse(x.responseText);
      } catch {
        /* text */
      }
      if (x.status >= 200 && x.status < 300 && body && typeof body === "object") {
        setResult(body as UploadResult);
        toast("ok", `Uploaded ${file.name} to ${node}`);
      } else {
        const msg = body && typeof body === "object" && "error" in body ? String((body as { error: unknown }).error) : String(body || x.statusText);
        setErr(`HTTP ${x.status}: ${msg}`);
        toast("bad", `Upload failed: ${friendly(msg).text}`, `HTTP ${x.status}: ${msg}`);
      }
    };
    x.onerror = () => {
      setProgress(null);
      setErr("network error while uploading");
    };
    x.onabort = () => {
      setProgress(null);
      setErr("upload canceled");
    };
    x.send(file);
  };

  return (
    <section className="card">
      <div className="card-head">
        <h2>⇪ Upload</h2>
        <span className="muted small">to {node}</span>
      </div>
      <div
        className={"dropzone" + (drag ? " over" : "") + (file ? " has-file" : "")}
        onDragOver={(e) => {
          e.preventDefault();
          setDrag(true);
        }}
        onDragLeave={() => setDrag(false)}
        onDrop={(e) => {
          e.preventDefault();
          setDrag(false);
          const f = e.dataTransfer.files?.[0];
          if (f) setFile(f);
        }}
        onClick={() => picker.current?.click()}
        role="button"
        tabIndex={0}
        onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && picker.current?.click()}
      >
        <input ref={picker} type="file" hidden onChange={(e) => setFile(e.target.files?.[0] ?? null)} />
        {file ? (
          <>
            <b>{file.name}</b>
            <span className="muted">
              {fmtBytes(file.size)} · {file.type || "application/octet-stream"} · click to change
            </span>
          </>
        ) : (
          <>
            <b>⇪ Drop a file here</b>
            <span className="muted">or click to choose one</span>
          </>
        )}
      </div>
      <div className="form-grid">
        <label className="field">
          <span>
            Stamp <Term id="batch">batch</Term>
          </span>
          {stamps.loading && !stamps.data ? (
            <Skeleton lines={1} />
          ) : usable.length ? (
            <select value={chosen} onChange={(e) => setBatch(e.target.value)} disabled={uploading}>
              {usable.map((b) => (
                <option key={b.batchID} value={b.batchID}>
                  {short(b.batchID, 10)} {b.label ? `(${b.label})` : ""} · depth {b.depth} · {humanTTL(b.batchTTL)}
                </option>
              ))}
            </select>
          ) : (
            <span className="warn-text small">▲ No usable batch on {node}. Buy one in Postage stamps (it takes a few blocks to become usable).</span>
          )}
        </label>
        <div className="field">
          <span>Options</span>
          <div className="opts">
            <label className="check">
              <input type="checkbox" checked={opts.pin} onChange={(e) => setOpts({ ...opts, pin: e.target.checked })} disabled={uploading} /> <Term id="pinning">pin</Term>
            </label>
            <label className="check">
              <input type="checkbox" checked={opts.encrypt} onChange={(e) => setOpts({ ...opts, encrypt: e.target.checked })} disabled={uploading} /> <Term id="encryption">encrypt</Term>
            </label>
            <label className="check" title="Deferred: the node stores locally first and pushes in the background">
              <input type="checkbox" checked={opts.deferred} onChange={(e) => setOpts({ ...opts, deferred: e.target.checked })} disabled={uploading} /> <Term id="deferred">deferred</Term>
            </label>
            <label className="check">
              <Term id="redundancy">redundancy</Term>{" "}
              <select value={opts.redundancy} onChange={(e) => setOpts({ ...opts, redundancy: Number(e.target.value) })} disabled={uploading}>
                {[0, 1, 2, 3, 4].map((r) => (
                  <option key={r} value={r}>
                    {r}
                  </option>
                ))}
              </select>
            </label>
          </div>
        </div>
      </div>
      <div className="row">
        <button
          className="btn primary"
          disabled={!file || !chosen || uploading}
          onClick={start}
          title={!file ? "Choose a file first" : !chosen ? "Needs a usable postage batch" : undefined}
        >
          ⇪ Upload to {node}
        </button>
        {!uploading && (!file || !chosen) && <span className="hint">ⓘ {!file ? "Choose a file first." : "Needs a usable postage batch."}</span>}
        {uploading && (
          <>
            <div className="meter wide" aria-label="upload progress">
              <div style={{ width: `${(progress ?? 0) * 100}%` }} />
              <span>{progress !== null && progress >= 1 ? "processing in Bee…" : `${Math.round((progress ?? 0) * 100)}%`}</span>
            </div>
            <button className="btn ghost" onClick={() => xhr.current?.abort()}>
              cancel
            </button>
          </>
        )}
      </div>
      {err && <ErrorNote error={err} prefix="Upload failed" />}
      {result && <UploadDone node={node} res={result} name={file?.name ?? ""} />}
    </section>
  );
}

function UploadDone({ node, res, name }: { node: string; res: UploadResult; name: string }) {
  const select = useStore((s) => s.select);
  const setTab = useStore((s) => s.setTab);
  const opId = `tag:${node}:${res.tagUid}`;
  return (
    <div className="upload-done">
      <div className="ok-text">✔ Uploaded{name ? ` ${name}` : ""}</div>
      <div className="kv-row">
        <span className="k">
          <Term id="reference">reference</Term>
        </span>
        <span className="v mono wrap">
          {res.reference} <Copy text={res.reference} />
        </span>
      </div>
      {res.tagUid ? <TagProgress node={node} uid={res.tagUid} /> : null}
      <div className="row">
        <a className="btn" href={api.data.downloadURL(node, res.reference, "", name)}>
          ⇩ Download
        </a>
        {res.tagUid ? (
          <button
            className="btn"
            onClick={() => {
              select({ type: "operation", id: opId });
              setTab("operations");
            }}
            title="Open the upload tag in Operations (appears after the next /tags poll)"
          >
            ⇢ Follow in Operations
          </button>
        ) : null}
      </div>
    </div>
  );
}

function TagProgress({ node, uid }: { node: string; uid: number }) {
  const [tag, setTag] = useState<Tag | null>(null);
  useEffect(() => {
    let alive = true;
    let t: ReturnType<typeof setTimeout> | undefined;
    const poll = () =>
      api.data
        .get<Tag>(node, `/tags/${uid}`)
        .then((g) => {
          if (!alive) return;
          setTag(g);
          if (!(g.split > 0 && g.synced >= g.split)) t = setTimeout(poll, 2000);
        })
        .catch(() => alive && (t = setTimeout(poll, 4000)));
    void poll();
    return () => {
      alive = false;
      if (t) clearTimeout(t);
    };
  }, [node, uid]);
  if (!tag) return null;
  const frac = tag.split ? Math.min(1, tag.synced / tag.split) : 0;
  return (
    <div className="kv-row">
      <span className="k">
        <Term id="tag">tag</Term> {uid}
      </span>
      <span className="v">
        <div className="meter wide" title="synced / split chunks">
          <div style={{ width: `${frac * 100}%` }} />
          <span>
            {tag.synced}/{tag.split} chunks synced {frac >= 1 ? "✔" : ""}
          </span>
        </div>
      </span>
    </div>
  );
}

// ---- Download ----

function DownloadCard({ node }: { node: string }) {
  const [ref, setRef] = useState("");
  const [path, setPath] = useState("");
  const clean = ref.trim().replace(/^bzz:\/\//, "");
  const valid = /^[0-9a-fA-F]{64}([0-9a-fA-F]{64})?$/.test(clean);
  return (
    <section className="card">
      <div className="card-head">
        <h2>⇩ Download</h2>
        <span className="muted small">by reference</span>
      </div>
      <p className="muted small">Fetches through {node}: if the content is not local, the node retrieves it from the network.</p>
      <label className="field">
        <span>
          <Term id="reference">Reference</Term>
        </span>
        <input value={ref} onChange={(e) => setRef(e.target.value)} placeholder="64 or 128 hex characters" spellCheck={false} />
      </label>
      <label className="field">
        <span>
          Path in <Term id="manifest">manifest</Term> <span className="muted small">(optional)</span>
        </span>
        <input value={path} onChange={(e) => setPath(e.target.value)} placeholder="index.html" spellCheck={false} />
      </label>
      <div className="row">
        {valid ? (
          <a className="btn primary" href={api.data.downloadURL(node, clean, path.trim())}>
            ⇩ Download from {node}
          </a>
        ) : (
          <button className="btn primary" disabled title="Paste a reference first">
            ⇩ Download
          </button>
        )}
        {ref && !valid && <span className="warn-text small">not a Swarm reference</span>}
      </div>
    </section>
  );
}

// ---- Uploads list ----

function UploadsCard({ node }: { node: string }) {
  const { data, error } = usePoll((s) => api.data.uploads(s), [], 5000);
  const list = data ?? [];
  return (
    <section className="card span2">
      <h2>Uploads this session</h2>
      {error && <ErrorNote error={error} />}
      {list.length === 0 ? (
        <div className="muted">Nothing uploaded through bee-scope yet.</div>
      ) : (
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>time</th>
                <th>file</th>
                <th>size</th>
                <th>node</th>
                <th>reference</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {list.map((u) => (
                <tr key={u.id} className="static">
                  <td className="mono">{fmtTime(u.at)}</td>
                  <td>
                    {u.name}
                    {u.encrypt ? " 🔒" : ""}
                  </td>
                  <td className="num">{fmtBytes(u.size)}</td>
                  <td>{u.node}</td>
                  <td className="mono">
                    {short(u.reference, 12)} <Copy text={u.reference} />
                  </td>
                  <td className="row-actions">
                    <a className="btn" href={api.data.downloadURL(u.node, u.reference, "", u.name)} title={`download via ${u.node}`}>
                      ⇩ {u.node}
                    </a>
                    {u.node !== node && (
                      <a className="btn" href={api.data.downloadURL(node, u.reference, "", u.name)} title={`retrieve through ${node} (tests retrieval)`}>
                        ⇩ via {node}
                      </a>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
