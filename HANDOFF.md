# bee-scope: handoff

A real-time visual debugger for a local Bee k3d cluster. The approved plan is at
`/Users/akrem/.claude/plans/sprightly-snuggling-snail.md`; read it first. This file
records what exists, the decisions made while building, and what to do next.

**Rules:**
- Never commit or push. This folder is not a git repo.
- Do not reuse or consult `DEV/swarm-lab`; the user rejected it.
- Keep replies to the user short.

## Status (2026-10-23)

The backend is about half done and **has never been compiled**. There is no frontend yet.

| Path | State |
|---|---|
| `go.mod` | module `beescope`, go 1.26.0. **No `require` lines yet**; see step 1 |
| `pkg/model/bee.go` | Bee wire types (Status, Topology, Stamp, Tag, Redistribution, LogLine, Span, Metrics) |
| `pkg/model/model.go` | Observation envelope, Event (Seq, ObservedAt, Provenance, Source, Corr), NodeState, ComponentState, Edge, Flow, Issue, Operation, Snapshot |
| `pkg/component/registry.go` | 16 subsystems → metrics, loggers, spans, endpoints, blind spots, layout x/y; internal flows with their metric signal. Names verified against bee@06c04af3 |
| `pkg/source/beeapi/client.go` | Read-only GET client. Topology bins are flattened; `latencyEWMA` is already in ms |
| `pkg/source/metrics/metrics.go` | Parses with expfmt (`NewTextParser(model.UTF8Validation)`); `Value` / `Rate` with label match and prefix match |
| `pkg/source/logs/parse.go` | Parser for Bee's `"k"=v` format, bounded by `MaxLine`; `SplitKubeTimestamp` |
| `pkg/source/logs/classify.go` | Exact-message rules (kademlia connect/disconnect, pushsync, retrieval, puller) → event kind, per-peer flow, correlation |
| `pkg/source/kube/kube.go` | Guard: context must be `k3d-*` and the API server loopback (`--allow-remote` overrides). Discovers services labeled `app.kubernetes.io/name=bee` with port `api`, skipping `-headless`. Pod is found by label `app.kubernetes.io/instance`. Has `FollowLogs` |
| `pkg/collect/collect.go` | Discovery loop; a goroutine per endpoint per node; log follower that reconnects, de-duplicates by kube timestamp, and drops lines without blocking (drops are reported) |
| `pkg/history/history.go` | Bounded rings: events (contiguous seq), keyframes, flow frames, logs, raw payloads. Has Filter, Density and KeyframeAt |
| `pkg/anomaly/{series,rules,tracker}.go` | The 10 rules from the plan, each with what / where / since / evidence / hypothesis / next steps. The tracker opens issues and resolves them after 20 s quiet, keeping them listed for 15 min |
| `pkg/engine/{engine,node,ops,hub,query}.go` | Single-goroutine engine; `Do()` runs reads on the engine goroutine for HTTP handlers; SSE hub; operations (upload tags, OTLP traces) |
| `pkg/source/otlp/otlp.go` | `POST /v1/traces` receiver, protobuf or JSON, gzip, bounded sizes; `Decode` is the fuzz target |
| `pkg/source/record/record.go` | NDJSON `Tee` (record) and `Replay`, with a virtual `Clock` |

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

## Next steps (in order)

1. **Make it compile.** From `bee-scope/`:
   ```bash
   go get k8s.io/client-go@v0.36.3 k8s.io/api@v0.36.3 k8s.io/apimachinery@v0.36.3 \
     github.com/prometheus/common@v0.70.1 github.com/prometheus/client_model \
     go.opentelemetry.io/proto/otlp@v1.11.0 google.golang.org/protobuf
   gofmt -l . && go vet ./... && go build ./...
   ```
   Expect small fixes, such as unused imports and gofmt alignment in `classify.go`. Simplify the `onStamps` condition, which reads `!ok && len(...) > 0 || !ok && stampsSeen`.
2. **`pkg/source/sim`.** A fake 1 bootnode + 7 bee cluster that emits *observations* (not events), all `Simulated: true`, reusing the real refs (`GET /status`, …). Loop a scripted scenario of about 6 min:
   - steady state;
   - upload burst on bee-1 (tags, pusher counters, pushsync "chunk pushed" logs);
   - retrieval failures on bee-2;
   - peer flapping on bee-4 (churn rule);
   - pullsync stall plus radius divergence on bee-6;
   - bee-3 unreachable for 30 s.
3. **`pkg/server`.** Embed `web/dist` and add these routes:
   - `GET /api/stream`: SSE. Subscribe first, send the snapshot, replay `history.Events(AfterSeq: Last-Event-ID)`, send a `gap` frame if the oldest seq is greater than the requested one, then go live. Close the stream on `Lagged`.
   - `GET /api/snapshot`
   - `GET /api/state?at=` (keyframe)
   - `GET /api/events?…`
   - `GET /api/density`
   - `GET /api/flows`
   - `GET /api/raw/{id}`
   - `GET /api/node/{name}` (`engine.NodeDetail`)
   - `GET /api/components` (registry)
   - `GET /api/logs`
   - `GET /api/chunk/{addr}`: logs and events matching the address, labeled **approximate**
   - `GET /api/operations[/{id}]`
   - `GET /api/search`
   - `GET /api/export`: redacted. Never include the kubeconfig or Authorization header.
   - `GET /api/meta`
   - `POST /api/actions/{loggers|status-peers|debugstore|probe-retrieval}`: requires `{"confirm":true}`. The UI shows the consequence before sending.
4. **`cmd/bee-scope/main.go`.** Flags:
   - `--context` (default `k3d-bee`), `--namespace` (default `local`), `--kubeconfig`, `--allow-remote`
   - `--listen` (default `127.0.0.1:7070`), `--otlp-listen` (default `127.0.0.1:4318`, empty disables)
   - `--sim`, `--record`, `--replay`, `--speed`, `--no-logs`, per-endpoint intervals
   - `--dev` (proxy to Vite)

   Wiring: obs chan (cap 16384) → optional `record.Tee` → `engine.Run`. Collector and OTLP feed the obs chan. Shut down on SIGINT.
5. **Go tests.**
   - Fixtures in `testdata/`, recorded from the live cluster: `/topology`, `/status`, `/metrics` and log lines.
   - `logs/parse_fuzz_test.go` and `otlp/decode_fuzz_test.go`.
   - Anomaly rule table tests.
   - History ring and seq-contiguity tests.
   - An engine test driven by a replay file.
   - Run with `go test -race ./...`.
6. **`web/`.** The shell is the top bar (LIVE / PAUSED / REPLAY / striped SIMULATION badge, freshness), ⌘K search, a bottom timeline strip with a density track and brush, and a right-hand inspector. Every view shares one selection and one time cursor; scrubbing loads `/api/state?at=`. Views:
   - **Topology:** canvas; particles only from `flows` frames.
   - **Node internals:** SVG; hatched means unobservable, dashed means stale.
   - **Timeline:** lanes per node.
   - **Operations:** tag pipeline and trace waterfall.
   - **Problems:** issue cards, jump to timeline, copy summary.

   Visual language: never color alone; always add an icon, label or pattern. Dark mode first; respect `prefers-reduced-motion`.
7. **Docs.** `README.md`, `docs/DATA-SOURCES.md` (the observability matrix in the plan) and `docs/INSTRUMENTATION.md` (the proposed Bee patch, not shipped).
8. **Live verification.** Follow the plan's Verification section: scale `bee-3` to 0 and back, run beekeeper ci-pushsync/ci-retrieval, confirm the sim banner, and check overhead.

## Environment notes

- **The cluster was down on 2026-10-23.** Docker Desktop was not running; `open -a Docker` was issued. Then run `k3d cluster start bee` and `docker start k3d-registry.localhost`. The current kube context is `k3d-bee`, but the default has been the **shared** cluster before, which is why the guard exists.
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
