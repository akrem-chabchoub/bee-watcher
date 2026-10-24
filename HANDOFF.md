# bee-scope: handoff

A real-time visual debugger for a local Bee k3d cluster. The approved plan is at
`/Users/akrem/.claude/plans/sprightly-snuggling-snail.md`; read it first. This file
records what exists, the decisions made while building, and what to do next.

**Rules:**
- Never commit or push. This folder is not a git repo.
- Do not reuse or consult `DEV/swarm-lab`; the user rejected it.
- Keep replies to the user short.

## Status (2026-10-24)

**Working end to end.** `make build` produces `dist/bee-scope` with the UI embedded. Verified:
- `go vet ./...` and `go test -race ./...` are clean.
- `tsc --noEmit` and `vite build` are clean.
- Fuzz runs were clean: `FuzzParse` for 15 s and `FuzzDecode` for 20 s.
- `--sim`, `--record` and `--replay` all work.
- Live mode against k3d-bee works: 6 nodes, 69 MB RSS, about 0% CPU idle. Headless-Chrome screenshots showed no console errors.

| Path | State |
|---|---|
| `pkg/source/sim` | 1 bootnode + 7 bees, 6-min scripted loop (see package doc / README). Emits observations only, all `Simulated`. `Options.Now` + `Step()` let tests drive it on a fake clock |
| `pkg/server` | every route in `docs/API.md`: SSE with resume and gap, query API, confirmed actions (`loggers` PUT, `status-peers`, `debugstore`, `probe-retrieval`) recorded as `action.*` events. Host-header check (DNS rebinding); actions need a JSON body from the same origin. SPA fallback for the embedded UI; `--dev` proxies to Vite |
| `cmd/bee-scope` | the flags from the old step 4, plus `--intervals k=dur,...`, `--history` and `-v`. Wiring: source → obs chan → optional `record.Tee` → engine |
| `web/` | Vite 6.4.4, React 18.3.1, zustand 5.0.15, TS 5.9.3. Views: Topology (canvas ring, min angular spacing), Node internals (SVG), Timeline, Operations, Problems; inspector; ⌘K; scrub strip. `web/embed.go` embeds `web/dist`; `go.mod` has `ignore ./web/node_modules` |
| tests | logs (parse, classify, fuzz), otlp (decode, gzip handler, fuzz), history, record round trip, kube guard, anomaly rule table + tracker, engine full-scenario test (every scripted fault opens its issue in its window), server (SSE resume/gap/live, actions, host check, UI) |
| docs | `README.md`, `docs/API.md`, `docs/DATA-SOURCES.md` (with Bee file:line), `docs/INSTRUMENTATION.md` |

**Added later the same day: cluster control, data tools, Explain, app bundle.**

| Path | State |
|---|---|
| `pkg/control` | Cluster tab backend. Status probes: tools, `docker info`, `k3d cluster list`, registry, statefulsets and pods through client-go. One-at-a-time jobs with steps, a log ring and cancel (process-group kill). Actions are listed in `docs/CONTROL-API.md`. Cluster create mirrors beelocal `k8s-local` but with explicit contexts and `--kubeconfig-switch-context=false`, and never runs sudo or `/etc/hosts` edits. Deploy writes `<UserConfigDir>/bee-scope/beekeeper-config/` (a copy of the user's config dir plus `zz-bee-scope.yaml`, which defines cluster `bee-scope` inheriting the chosen profile, with counts and an image override through `bee-scope-<ng>` node groups). It then runs `beekeeper create bee-cluster --kubeconfig <single-context file>` and deletes nodes above the counts by label `app.kubernetes.io/instance`. The registry port comes from `docker port` (the user's is 5002) |
| `pkg/server/control.go`, `data.go` | `/api/control/*`, `/api/data/*`, and `POST /api/quit`. Data: a read-only GET passthrough with an allowlist; stamp buy, top-up and dilute; upload (needs the `X-Bee-Scope: upload` header plus a same-origin check) streams to `/bzz`; download streams back. All of them record `action.data` events |
| `pkg/source/kube` | the client now reconnects: missing context or unreachable API at startup is not fatal (the guard still is). Calls reset the connection on connection or x509 errors; `Reset()` is called after cluster create, start and delete. The collector restarts workers when a node's BaseURL changes |
| `cmd/bee-scope` | `--open`. When the port is already in use and `--open` is set, it opens the browser on the running instance. The controller exists only in live mode |
| `Makefile` | `make app` builds `dist/BeeScope.app`, a background launcher (`LSUIElement`) running `bee-scope --open` and logging to `~/Library/Logs/bee-scope.log`. `make install-app` copies it to `~/Applications` |
| `web/` | Cluster and Data tabs, a right-click Explain glossary (Simple / Engineer), toasts, getting-started guidance, and a Quit button |

**Changes to earlier code:**
- engine: only `/health` lifts `unknown` (unless health polling is disabled).
- engine: keeps serving after its input closes (end of a replay).
- engine: added `Record()`.
- engine: simplified the `onStamps` condition.
- collector: added `Client()`.
- beeapi: added `Request()`.
- outlier rule: needs a minimum absolute difference.
- pullsync-stall rule: window is 2 min (span ≥ 100 s), down from 3 min.
- kube guard: also accepts `0.0.0.0`.

**Live cluster facts:** the cluster is `bootnode-0`, `bee-0..2` and `light-0..1`, not 8 bees. Salud reports some peers `healthy=false` after a restart, so the "marks N peers unhealthy" issues are real.

## Key design decisions (already in code)

**Provenance and source labeling**
- Every event, component and edge carries a provenance:
  - `observed`: one source;
  - `derived`: a diff or a rate;
  - `inferred`: a suspicious or hypothesis issue;
  - `simulated`.
- An observation with `Simulated: true` switches the engine to label everything `simulated`.

**Flows and peer-change events**
- **Flows between nodes** come only from log lines or spans that name a peer. Metric rates animate only the flows *inside* one node (`NodeState.FlowRates`, keyed `"from>to"`).
- **Peer connect/disconnect events** come from kademlia debug logs when those are present. Otherwise they come from diffs between two `/topology` polls, labeled `derived` (see `nodeRT.kadLogs`).

**Node phase**
- **Kube-proxy 503s are told apart from Bee 503s.** A 503 from the kube API-server proxy (pod down) has a body containing "no endpoints" or "dial tcp". Such a response means unreachable, not gated (`proxyDown`).
- **Health is unknown before the first /health poll.** The phase only becomes `up` after a successful /health. A /status 503 means `gated`.

**Volume control**
- Bursts of info, warning and error lines are merged into one event per second, with an `Attrs["count"]` (`aggregate` / `flushAggs`).
- The full snapshot is JSON-encoded once per second, sent as an SSE `snapshot` frame, and kept as a keyframe every 5 s.
- Event batches go out every 250 ms as SSE `events` with `id: lastSeq`. Flows go out every 500 ms as `flows`.
- Clients de-duplicate by seq.

**Expensive endpoints are never polled.** That means `/status/peers`, `/debugstore`, `/rchash` and `/stewardship`. `/redistributionstate` is polled every 60 s, and treats 4xx as "not applicable".

**Frontend stack (decided, not built):** Vite 6, React 18.3.1, TypeScript and zustand 5. No xyflow, d3 or uPlot: the topology is custom Canvas 2D on a Kademlia ring (angle from the overlay prefix), and the node internals are custom SVG laid out from the registry x/y. Keep dependencies minimal.

## Next steps

1. **Scale-down check (user's call):** `kubectl -n local scale sts bee-2 --replicas=0`, then back to 1. The permission classifier blocked running it automatically.
2. **Traffic check:** `beekeeper check --checks ci-pushsync,ci-retrieval --config-dir …` while bee-scope runs. Confirm the particles, and that the inspector's numbers match `curl`.
3. **Fixtures:** record `testdata/` from the live cluster (`--record`) and add parser tests on real `/topology`, `/metrics` and log lines.
4. **Frontend gaps:**
   - no light theme;
   - no side-by-side compare (Timeline node chips only);
   - operations are not tied to the time cursor;
   - no frontend unit tests.
5. **Tracing:** enable it only if the user wants it (see Environment notes).

## Environment notes

- **Cluster start:** `open -a Docker`, `k3d cluster start bee`, `docker start k3d-registry.localhost` (done on 2026-10-24; the cluster is running). The current kube context is `k3d-bee`, but the default has been the **shared** cluster before, which is why the guard exists.
- **Local bee config:** `DEV/beekeeper/config/local.yaml` sets `verbosity: 5`, so debug logs are available, but tracing is off.
- **Enabling traces is the user's call.** It needs a copied beekeeper config with `tracing-enable: true` and `tracing-endpoint: host.k3d.internal:4318`. Whether pods can reach a Mac loopback listener this way is unverified; you may need `--otlp-listen 0.0.0.0:4318`.
- **Beekeeper:** always pass an explicit `--config-dir`, because `~/.beekeeper.yaml` overrides it.
- **Toolchains:** Go 1.26.0 and Node 22.18 / npm 10.9 are installed, and the module proxy and npm registry are reachable.

## Verified Bee facts that shape the design (bee feat/logging @ 06c04af3)

**Logs**
- Format: `"time"="2006-01-02 15:04:05.000000" "level"=… "logger"="node/<pkg>/…" ["v"=N] ["caller"=" pkg/x.go:1"] "msg"=…`. There is no JSON flag and no log endpoint.

**Tracing**
- Bee uses OTel over OTLP, with `service.instance.id` set to the overlay.
- Trace context is carried in the p2p header `tracing-span-context-v2`.
- Deferred uploads break the trace: the pusher starts a new root span.
- There are no spans for pullsync, hive, salud or redistribution.

**Metrics**
- `pullsyncRate` covers historical sync only, over a 10-minute window; 0 means synced. It is also exposed as `bee_puller_pullsync_rate`.
- Sharky metrics are never registered.
- `bee_pushsync_total_outgoing_errors` is never incremented.
- The tag `sent` counter is always 0.

**API**
- `/status/peers` and `/status/neighborhoods` share a semaphore of 1, so concurrent calls get 429.
- `/debugstore` does full scans.
- `/redistributionstate` makes a chain call and now also has a PATCH. Never call the PATCH.
- There is no auth. CORS blocks cross-site non-GET requests, which doesn't matter here because bee-scope calls Bee server-side.
