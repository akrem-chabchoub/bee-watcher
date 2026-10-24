# bee-scope HTTP API

The server listens on `--listen` (default `127.0.0.1:7070`) and serves the
embedded UI at `/` and this JSON API under `/api/`. Every type below is the
JSON encoding of a Go type in `pkg/model` (field names are the `json` tags),
`pkg/history` or `pkg/engine`. Times are RFC 3339 strings. Every endpoint is
read-only except `POST /api/actions/*`.

Query parameters that take a time accept RFC 3339 or Unix milliseconds.

## Live stream

### `GET /api/stream`

Server-sent events. Resume with the `Last-Event-ID` header (browsers send it
on reconnect) or `?after=<seq>`.

Order on connect:

1. `hello`: `{"lastSeq": n, "oldestSeq": n, "mode": "live|sim|replay"}`
2. `snapshot`: the current `Snapshot`
3. `gap` (only when resuming and events were evicted): `{"requested": n, "oldest": n}`
4. `events` frames replaying history after the requested seq
5. live frames

Frame types:

| event | id | data |
|---|---|---|
| `snapshot` | none | `Snapshot`, once per second |
| `events` | last seq in the batch | `Event[]`, every 250 ms when there are new events |
| `flows` | none | `FlowFrame` `{at, flows: Flow[]}`, every 500 ms when there was per-edge activity |
| `gap` | none | `{requested, oldest}` |
| `hello` | none | see above |

Clients de-duplicate events by `seq`: replayed and live batches may overlap.
When a client is too slow the server closes the stream; the browser
reconnects with `Last-Event-ID` and is replayed from history.

## State and history

| Route | Returns |
|---|---|
| `GET /api/meta` | `Meta` (below) |
| `GET /api/snapshot` | current `Snapshot` |
| `GET /api/state?at=T` | `Snapshot` from the newest keyframe at or before `T` (keyframes every 5 s). Header `X-Keyframe-At` gives its time. 404 when no keyframe exists |
| `GET /api/window` | `{"from": T, "to": T, "oldestSeq": n, "lastSeq": n}`: the rewindable range |
| `GET /api/events` | `Event[]` in seq order. Filters: `from`, `to`, `after` (seq), `before` (seq), `node` (repeatable or comma list), `component` (same), `kind` (prefix, comma list), `minSev` (`debug|info|warn|error`), `prov` (comma list), `q` (text), `corr` (exact chunk/peer/trace/batch id or peer node), `limit` (default 1000, max 20000), `newest=1` (return the newest `limit` matches) |
| `GET /api/event/{seq}` | one `Event` |
| `GET /api/density?from=T&to=T&n=N` | `Bucket[]` `{t, count: {debug, info, warn, error}}`; same filters as `/api/events` |
| `GET /api/flows?from=T&to=T` | `FlowFrame[]` |
| `GET /api/raw/{id}` | the raw JSON body behind a `rawRef` / `NodeDetail.rawRefs` |
| `GET /api/node/{name}` | `NodeDetail` `{name, peers: PeerView[], rawRefs: {ref: id}, series: {name: [unixMs, value][]}}` |
| `GET /api/components` | `{"specs": ComponentSpec[], "flows": FlowSpecJSON[]}` (below) |
| `GET /api/rules` | `[{id, name}]` |
| `GET /api/logs` | `LogRecord[]` `{id, node, component, line: LogLine}` oldest first. Filters: `node`, `component`, `q`, `level` (minimum: `debug|info|warning|error`), `from`, `to`, `limit` (default 500, max 5000) |
| `GET /api/chunk/{addr}` | `{address, correlation: "approximate", events: Event[], logs: LogRecord[]}` |
| `GET /api/operations` | `Operation[]`, newest first |
| `GET /api/operations/{id}` | one `Operation` (ids contain `:`; URL-encode them) |
| `GET /api/search?q=` | `SearchResult[]` `{type, id, label, detail}`; `type` is `node|peer|component|chunk|batch|trace|tag|issue|operation` |
| `GET /api/export` | redacted JSON download: `{exportedAt, meta, snapshot, events, operations}`. Never contains kubeconfig data, auth headers or API base URLs |

`Meta`:

```json
{
  "mode": "live | sim | replay",
  "target": "k3d-bee/local",
  "version": "dev",
  "startedAt": "…",
  "otlp": "127.0.0.1:4318",
  "logs": true,
  "actions": true,
  "intervals": {"health": 5, "status": 5, "topology": 5, "metrics": 5, "tags": 10, "stamps": 30, "chainState": 30, "redistribution": 60},
  "historyS": 1800
}
```

`actions` is false in sim and replay mode.

`ComponentSpec`:

```json
{
  "id": "pushsync", "label": "Pushsync", "lane": "data", "pkg": "pkg/pushsync",
  "description": "…",
  "metrics": ["bee_pushsync_total_sent", "…"],
  "loggers": ["pushsync"], "spans": ["pushsync-handler"], "endpoints": [],
  "blind": ["…"], "x": 0.48, "y": 0.18
}
```

`FlowSpecJSON`: `{"from", "to", "path", "label", "signal"}` where `signal` is
the metric name or `""`. The live rate of a flow inside a node is
`NodeState.flowRates["from>to"]`.

## Actions

`POST /api/actions/{name}` with a JSON body. Every action requires
`"confirm": true`; without it the server answers 400 and describes the
consequence. In sim and replay mode actions answer 409.

| name | body | effect |
|---|---|---|
| `loggers` | `{node, expression, verbosity}` | `PUT /loggers/{base64url(expression)}/{verbosity}` on the node. `verbosity` is `none|error|warning|info|debug|all`. Changes the node's log volume until it restarts |
| `status-peers` | `{node}` | `GET /status/peers`. Every node in the cluster is queried by that node; it shares a semaphore of 1 with `/status/neighborhoods` |
| `debugstore` | `{node}` | `GET /debugstore`: full scan of the node's store |
| `probe-retrieval` | `{node, chunk}` | `GET /chunks/{chunk}` with a fresh `traceparent`. Returns `{status, ms, bytes, traceId, error}` |

Responses are `{ok, status, ms, body}` where `body` is the node's JSON (or
text) response, truncated to 1 MiB.
