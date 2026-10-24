# bee-scope

A real-time visual debugger for a local Bee cluster (the k3d cluster that
beekeeper builds). It shows nodes, the subsystems inside each node and the
peer links between them as live entities. It draws activity only when a real
source observed it, detects anomalies and explains them, and keeps every
symptom one click away from its evidence.

bee-scope is read-only by default and makes no Bee code changes. Sources:
the Bee HTTP API, `/metrics`, pod logs through the kube API, and optionally
OTLP traces. See [docs/DATA-SOURCES.md](docs/DATA-SOURCES.md) for exactly
what is and is not observable.

## Quick start

Requirements: Go 1.26, Node 22 and npm (only to build the UI).

```bash
make build                 # builds the UI, then dist/bee-scope with the UI embedded
./dist/bee-scope --sim     # simulated cluster, no Kubernetes needed
open http://127.0.0.1:7070
```

Against the local cluster:

```bash
open -a Docker
k3d cluster start bee
docker start k3d-registry.localhost
./dist/bee-scope           # --context k3d-bee --namespace local by default
```

## Modes

| Mode | Command | Notes |
|---|---|---|
| live | `bee-scope` | discovers services labeled `app.kubernetes.io/name=bee` (port `api`, `-headless` skipped) and follows their pod logs |
| simulation | `bee-scope --sim` | 1 bootnode + 7 bees looping a 6-minute fault scenario. Everything is labeled **simulated** and the UI shows a striped SIMULATION banner |
| record | `bee-scope --record session.ndjson` | also appends every observation to an NDJSON file (works with `--sim`) |
| replay | `bee-scope --replay session.ndjson --speed 4` | re-runs a recording through the same engine. Anomaly rules evaluate once per wall-clock second, so use `--speed` 1–10 to reproduce issue timing; `--speed 0` loads as fast as possible |

The simulation scenario (scenario seconds, repeating):

| Time | What happens | Issues you should see |
|---|---|---|
| 0–60 | steady state | none |
| 60–120 | upload burst on bee-1 | upload tag operation, pushsync particles from bee-1, traces |
| 120–180 | retrieval failures on bee-2 | retrieval failure spike |
| 150–210 | bee-4 flaps with bee-5 and bee-7 | peer churn |
| 150–330 | bee-6 radius 1 vs cluster 0; historical sync stalls after 20 s | radius divergence and flapping, pullsync stall, redistribution unhealthy |
| 240–270 | bee-3 unreachable, then restarts | unreachable, "bee-3 is being disconnected by 7 nodes", restart event |

## Flags

| Flag | Default | |
|---|---|---|
| `--context` | `k3d-bee` | kube context |
| `--namespace` | `local` | namespace of the Bee nodes |
| `--kubeconfig` | standard rules | |
| `--allow-remote` | off | skip the local-cluster guard (see Safety) |
| `--listen` | `127.0.0.1:7070` | UI and API |
| `--otlp-listen` | `127.0.0.1:4318` | OTLP/HTTP trace receiver, live mode only; empty disables |
| `--sim`, `--record FILE`, `--replay FILE`, `--speed X` | | see Modes |
| `--no-logs` | off | do not follow pod logs |
| `--intervals` | | overrides such as `health=5s,metrics=10s`. Keys: discovery, health, status, topology, metrics, tags, stamps, chainstate, redistribution. `0` disables one; redistribution must stay ≥ 30 s |
| `--history` | `30m` | in-memory history window (events, keyframes every 5 s, logs, raw payloads) |
| `--dev`, `--dev-url` | off, `http://127.0.0.1:5173` | proxy the UI to the Vite dev server |
| `-v`, `--version` | | |

## Safety

- **Local clusters only.** bee-scope refuses any context that is not named
  `k3d-*` or whose API server is not loopback, because the default kube
  context has pointed at a shared cluster before. `--allow-remote`
  overrides this; don't use it casually.
- **Read-only polling.** Only cheap GET endpoints are polled.
  `/status/peers`, `/status/neighborhoods`, `/debugstore`, `/rchash` and
  `/stewardship` are never polled, and the `/redistributionstate` PATCH is
  never called.
- **Actions are explicit.** Changing logger verbosity, the two expensive GETs
  and a probe retrieval exist only as actions. Each one shows its consequence
  and requires confirmation. Each is recorded on the timeline. They are
  disabled in sim and replay mode.
- **No auth, so loopback by default.** The server rejects requests whose
  `Host` is not localhost or an IP (DNS-rebinding defense), and actions
  require a JSON body from the same origin. bee-scope warns if you listen on a
  non-loopback address.
- **Exports are redacted.** `/api/export` contains no kubeconfig data, auth
  headers or API base URLs.

## Traces (optional)

Bee exports OpenTelemetry spans when tracing is enabled. Whether to enable it
is the operator's call. To enable it, copy the beekeeper config, set
`tracing-enable: true` and `tracing-endpoint: host.k3d.internal:4318` on the
bee node group, and redeploy. bee-scope receives the spans on `--otlp-listen`.
Pods may not reach a loopback listener on the host; if no spans arrive, run
with `--otlp-listen 0.0.0.0:4318`. That exposes only the trace receiver, not
the UI.

With beekeeper, always pass an explicit `--config-dir`, because
`~/.beekeeper.yaml` overrides it.

## UI

- **Top bar:** mode (LIVE / PAUSED / REPLAY / SIMULATION), freshness, and collector overhead.
- **⌘K:** search nodes, overlays, components, issues, operations, chunk/batch/trace/peer ids.
- **Bottom strip:** event density over the history window. Click or drag to rewind every view to a keyframe.
- **Views:** Topology (Kademlia ring; particles only from observed per-peer flows), Node internals (subsystems with
  status; hatched = unobservable, dashed = stale), Timeline, Operations (upload tags, traces, chunk lookup),
  Problems (issues with evidence, hypothesis and next steps).
- **Inspector:** the same layout for anything selected, including raw JSON and logs.

Color never carries meaning alone: every state also has an icon, a label or
a pattern. The UI respects `prefers-reduced-motion`.

## Development

```bash
make test        # go vet + go test -race ./...
make test-web    # tsc --noEmit
make fuzz        # log parser and OTLP decoder fuzz targets
go run ./cmd/bee-scope --sim --dev   # API on :7070, UI from Vite
cd web && npm run dev                # Vite on :5173 (proxies /api to :7070)
```

Layout:

```
cmd/bee-scope        flags and wiring: sources → (record) → engine → server
pkg/source/kube      guarded client-go: discovery, service proxy, log follow
pkg/source/beeapi    read-only typed Bee API client
pkg/source/metrics   expfmt parsing, counter rates
pkg/source/logs      Bee key/value log parser and classifier
pkg/source/otlp      OTLP/HTTP trace receiver
pkg/source/sim       simulated cluster and scenario
pkg/source/record    NDJSON record / replay
pkg/collect          per-node pollers and log followers
pkg/engine           single-goroutine state engine, SSE hub, operations
pkg/anomaly          the 10 rules and the issue lifecycle
pkg/history          bounded rings: events, keyframes, flows, logs, raw payloads
pkg/component        registry of Bee subsystems and their data sources
pkg/server           HTTP API (docs/API.md) and the embedded UI
web/                 React + TypeScript UI (Vite)
```

Docs: [API](docs/API.md) · [Data sources](docs/DATA-SOURCES.md) ·
[Proposed Bee instrumentation](docs/INSTRUMENTATION.md)
