# Proposed Bee instrumentation (not shipped)

bee-scope v1 needs no Bee changes. This is a small, opt-in patch that would
close the most painful blind spots listed in `DATA-SOURCES.md`. Nothing here
is implemented; it is a proposal for discussion.

## 1. `/debug/events` server-sent events (opt-in)

An SSE endpoint, enabled by a flag (`--debug-events`, default off), that
forwards signals Bee already computes internally:

- `SubscribeTopologyChange` (kademlia): peer connected / disconnected with
  bin and reason. Today bee-scope must choose between debug logs (exact but
  verbose) and `/topology` diffs (sampled every 5 s, misses flaps).
- `SubscribeNetworkStorageRadius` (salud / reserve): radius changes as they
  happen, with the old and new value.
- Puller worker start / stop per bin.

Each event: `{"t":…,"kind":…,"peer":…,"attrs":{…}}`. Bounded per-subscriber
buffer; slow subscribers are dropped, never block Bee.

## 2. In-flight gauges

- `bee_pusher_inflight`: chunks handed to pushsync and not yet receipted
  (the pusher has a concurrency cap of 128 but no gauge).
- `bee_retrieval_inflight`: outstanding retrieval requests.

Both let the node view draw real queues instead of inferring them from
counter differences.

## 3. Register the sharky metrics

`pkg/sharky` defines metrics that are never registered with the node's
registry. Registering them exposes shard free slots and write latency, which
matters when the reserve is near capacity.

## 4. Echo a trace / request ID on API responses

Add `traceparent` (or `X-Request-Id`) to every API response when tracing is
enabled. bee-scope could then jump from an HTTP failure to the exact trace
and log lines without approximate chunk-address matching.

## 5. Keep the trace across deferred uploads

Store the upload's span context with the chunk in the upload store and use
it as the parent (or a span link) when the pusher pushes the chunk, so a
deferred upload is one trace end to end.

## Rollout

All five are additive and off by default (1) or zero-cost when nobody reads
them (2–4). Item 5 touches the upload store schema and needs a migration
plan, so it should land separately.
