# Data sources

What bee-scope can and cannot see in a Bee node, and where each signal comes
from. References are to the Bee source at `feat/logging` @ `06c04af3`
(OpenAPI 8.2.1). Every name below is used verbatim by
`pkg/component/registry.go`, `pkg/engine` or `pkg/source/logs/classify.go`.

bee-scope makes no Bee code changes. It uses four sources:

| Source | How | Cost on the node | Provenance |
|---|---|---|---|
| HTTP API | kube API-server service proxy, typed GET pollers | cheap in-memory reads; `/chainstate` is one RPC, `/redistributionstate` one chain call | `observed`; diffs between polls are `derived` |
| `/metrics` | Prometheus text, parsed with `expfmt` | one scrape per interval | gauges `observed`, counter rates `derived` |
| Pod logs | `pods/{pod}/log?follow&timestamps` via the kube API | none on Bee (read from the container runtime) | `observed` |
| OTLP traces | built-in `/v1/traces` receiver, opt-in on the Bee side | span export, only when tracing is enabled | `observed` |

## Polled endpoints

| Endpoint | Default interval | Used for |
|---|---|---|
| `GET /health` | 5 s | phase (`up` / `unreachable`), version |
| `GET /status` | 5 s | overlay, radius, committed depth, pullsyncRate, reserve, neighborhood size, warmup. A 503 means `gated` |
| `GET /topology` | 5 s | the only real peer edges: bins, latency (`latencyEWMA`, already ms), health, reachability, depth |
| `GET /metrics` | 5 s | component activity, error rates, queues, internal flow rates |
| `GET /tags` | 10 s | upload operations (split → stored → synced) |
| `GET /stamps` | 30 s | batch usability and utilization |
| `GET /chainstate` | 30 s | block vs chain tip |
| `GET /redistributionstate` | 60 s | frozen / healthy / phase. Makes a chain call; 4xx means "not applicable" |

A 503 whose body contains "no endpoints available" or "dial tcp" comes from
the kube proxy (pod down) and is treated as unreachable, not gated.

**Never polled:** `/status/peers` and `/status/neighborhoods` (they share a
semaphore of 1, `pkg/api/router.go:706`), `/debugstore` (full scan,
`pkg/api/router.go:137`), `/rchash`, `/stewardship`, `/pingpong`. The first
two are available as confirmed actions. The PATCH on `/redistributionstate`
(`pkg/api/router.go:691`) is never called.

## Metrics by component

All families are prefixed `bee_`.

| Component | Activity (rate) | Errors (rate) | Queue / gauges | Defined in |
|---|---|---|---|---|
| api | `api_request_count` | `api_response_code_count{code=5xx}` | 4xx total | `pkg/api/metrics.go:43,57` |
| pusher | `pusher_total_to_push` | `pusher_total_errors` | `pusher_total_synced` | `pkg/pusher/metrics.go:28,34,40` |
| pushsync | `pushsync_total_sent`, `_total_received` | `_total_failed_send_attempts`, `_total_handler_errors` | `_shallow_receipt`, `_forwarder`, `_storer`, `_invalid_stamps` | `pkg/pushsync/metrics.go:41,47,53,65,71,89,101,107,138` |
| retrieval | `retrieval_request_count`, `_total_retrieved` | `_request_failure_count`, `_total_errors` | `_request_success_count` | `pkg/retrieval/metrics.go:38,44,50,74,92` |
| puller | `puller_synced_chunks{type}` | `puller_worker_errors` | `puller_worker` (in-flight), `puller_pullsync_rate` | `pkg/puller/metrics.go:34,40,46,58` |
| pullsync | `pullsync_chunks_delivered`, `_chunks_sent` | `_received_invalid_chunks`, `_stream_limit_exceeded` | | `pkg/pullsync/metrics.go:58,64,82,101` |
| reserve | `localstore_method_calls` | `localstore_method_calls{status=failure}` | reserve size, within radius, storage radius, cache size, evicted | `pkg/storer/metrics.go:50,68,84,100,108,116` |
| libp2p | `libp2p_handled_stream_count`, `_created_stream_count` | `_stream_reset_count`, `_stream_handler_error_reset_count` | disconnects, kicked out, blocklisted | `pkg/p2p/libp2p/metrics.go:96,108,114,129,141,159,165` |
| kademlia | `kademlia_total_inbound_connections`, `_total_outbound_connections` | `_total_outbound_connection_failed_attempts` | connected peers, depth, storage depth | `pkg/topology/kademlia/metrics.go:62,68,80,104,116,128` |
| hive | `hive_peers_handler_count`, `_broadcast_peers_count` | underlay / store errors, `gossip_coalesce_dropped_total` | `hive_gossip_coalesce_buffer_size` (a real queue) | `pkg/hive/metrics.go:51,69,105,111,177,183` |
| salud | | | healthy, unhealthy, network radius, neighborhood radius | `pkg/salud/metrics.go:51,57,63,69` |
| accounting | debit / credit events | settle / payment errors | overdraw disconnects | `pkg/accounting/metrics.go:59,65,89,132,138` |
| storageincentives | `storageincentives_backend_calls` | commit / reveal / claim / backend errors | round, phase | `pkg/storageincentives/metrics.go:44,92,106,112,120,126,132` |

The engine also reads `bee_libp2p_created_connection_count`
(`pkg/p2p/libp2p/metrics.go:65`) to detect process restarts (a monotonic
counter that goes down), and `bee_log_error_count` (`pkg/log/metrics.go:46`)
for the outlier rule.

## Log lines that become events

Bee's default log format is `"key"="value"` pairs; there is no JSON flag and
no log endpoint, so logs come only through the kube API. Lines at error,
warning and info level become timeline events (bursts merged per second).
Debug lines count toward component activity and are kept for search; only
these exact messages become events or flows:

| Message | Where | Result |
|---|---|---|
| `connected to peer`, `disconnected peer` | `pkg/topology/kademlia/kademlia.go:472,1304` | peer churn events (preferred over `/topology` diffs while present) |
| `chunk pushed` | `pkg/pushsync/pushsync.go:609` | pushsync flow to `peer_address` |
| `could not push to peer` | `pkg/pushsync/pushsync.go:506` | failed pushsync flow, event |
| `shallow receipt` | `pkg/pushsync/pushsync.go:604` | event with `peer_radius` / `self_radius` |
| `retrieved chunk`, `failed to get chunk` | `pkg/retrieval/retrieval.go:274,278` | retrieval flow from `peer_address` (failed: event) |
| `retrieval failed` | `pkg/retrieval/retrieval.go:291` | event |
| `sync peer failed`, `syncWorker interval failed` | `pkg/puller/puller.go:249,387` | failed pullsync flow, event |
| `radius decrease` | `pkg/puller/puller.go:188` | event |

Log-derived per-peer flows are the only source of particles between nodes on
the topology map (plus spans that carry `swarm.peer.address`). Metric rates
animate only the flows inside one node.

## Traces

Bee exports OpenTelemetry spans over OTLP when started with
`--tracing-enable`. `service.instance.id` is the overlay
(`pkg/tracing/tracing.go:202`), which is how bee-scope maps spans to nodes.
Trace context crosses p2p in the `tracing-span-context-v2` header, so one
trace can span several nodes.

## Blind spots (shown as "unobservable", never as zero)

- No per-peer metric labels anywhere.
- No spans for pullsync, hive, salud or redistribution.
- Deferred uploads break the trace: the pusher starts a fresh root span per chunk.
- The tag `sent` counter is never incremented.
- `bee_pushsync_total_outgoing_errors` is never incremented.
- No pusher or retrieval queue-depth gauge.
- Sharky metrics exist in code but are never registered.
- No trace or request ID in API responses.
- `pullsyncRate` covers historical sync only, over a 10-minute window; 0 means synced.
- The netstore has no metrics of its own; it is visible only through spans.
