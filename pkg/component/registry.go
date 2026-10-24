// Package component is the registry of Bee subsystems that bee-scope can
// show inside a node, and of the data sources that feed each of them.
//
// Every metric and logger name here was checked against the Bee source
// (bee feat/logging @ 06c04af3). A component with no live source is shown as
// "unobservable", never as idle.
package component

// MetricRef selects samples of one Prometheus family.
type MetricRef struct {
	Name        string            // full family name, e.g. bee_pusher_total_errors
	Match       map[string]string // exact label match
	LabelPrefix map[string]string // label value prefix match (e.g. code "5")
	Label       string            // human label
}

// Spec describes one subsystem.
type Spec struct {
	ID          string
	Label       string
	Lane        string // ingress, data, network, storage, economy
	Pkg         string // Bee package path
	Description string
	Activity    []MetricRef // counters; their summed rate is the activity
	Errors      []MetricRef // counters; their summed rate is the error rate
	Queue       *MetricRef  // gauge that measures queued / in-flight work
	Gauges      []MetricRef // extra gauges shown in the inspector
	Loggers     []string    // logger name segments under "node/"
	Spans       []string    // OTel span names emitted by this subsystem
	Endpoints   []string    // HTTP endpoints that reveal its state
	Blind       []string    // known blind spots, shown in the inspector
	X, Y        float64     // layout position in the node diagram (0..1)
}

// FlowSpec is a directed relation between two subsystems in a node.
type FlowSpec struct {
	From, To string
	Path     string // upload, download, sync, topology, economy
	Label    string
	Signal   *MetricRef // counter whose rate animates this flow; nil = no signal
}

func m(name, label string) MetricRef { return MetricRef{Name: name, Label: label} }

// Specs is the ordered list of subsystems.
var Specs = []Spec{
	{
		ID: "api", Label: "HTTP API", Lane: "ingress", Pkg: "pkg/api", X: 0.06, Y: 0.40,
		Description: "Request entry point: /bytes, /bzz, /chunks, /soc, /feeds, /stamps and the debug endpoints.",
		Activity:    []MetricRef{m("bee_api_request_count", "requests")},
		Errors:      []MetricRef{{Name: "bee_api_response_code_count", LabelPrefix: map[string]string{"code": "5"}, Label: "5xx responses"}},
		Gauges: []MetricRef{
			{Name: "bee_api_response_code_count", LabelPrefix: map[string]string{"code": "4"}, Label: "4xx responses (total)"},
		},
		Loggers:   []string{"api"},
		Spans:     []string{"bytes-upload", "bytes-download", "bytes-head", "chunks-stream-upload", "bzz-upload", "bzz-download", "bzz-post", "bytes-post"},
		Endpoints: []string{"GET /health", "GET /readiness"},
		Blind:     []string{"No per-route metric label: errors cannot be attributed to an endpoint from metrics alone.", "No request-ID header in responses."},
	},
	{
		ID: "pusher", Label: "Pusher (upload queue)", Lane: "data", Pkg: "pkg/pusher", X: 0.26, Y: 0.18,
		Description: "Drains the local upload store and pushes each chunk into the network via pushsync.",
		Activity:    []MetricRef{m("bee_pusher_total_to_push", "chunks handed to pushsync")},
		Errors:      []MetricRef{m("bee_pusher_total_errors", "push errors")},
		Gauges:      []MetricRef{m("bee_pusher_total_synced", "chunks synced (total)")},
		Loggers:     []string{"pusher"},
		Endpoints:   []string{"GET /tags"},
		Blind:       []string{"Queue depth has no gauge (concurrency cap 128). Only /debugstore (full scan) reveals pending uploads.", "Deferred uploads start a fresh trace per chunk, so the HTTP upload and the push are not linked."},
	},
	{
		ID: "pushsync", Label: "Pushsync", Lane: "data", Pkg: "pkg/pushsync", X: 0.48, Y: 0.18,
		Description: "Forwards chunks toward their neighborhood and returns storage receipts.",
		Activity:    []MetricRef{m("bee_pushsync_total_sent", "sent"), m("bee_pushsync_total_received", "received")},
		Errors:      []MetricRef{m("bee_pushsync_total_failed_send_attempts", "failed send attempts"), m("bee_pushsync_total_handler_errors", "handler errors")},
		Gauges: []MetricRef{
			m("bee_pushsync_shallow_receipt", "shallow receipts (total)"),
			m("bee_pushsync_forwarder", "forwarded (total)"),
			m("bee_pushsync_storer", "stored as storer (total)"),
			m("bee_pushsync_invalid_stamps", "invalid stamps (total)"),
		},
		Loggers: []string{"pushsync"},
		Spans:   []string{"pushsync-handler", "push-chunk-async"},
		Blind:   []string{"No per-peer metric; per-peer activity comes only from debug logs or spans."},
	},
	{
		ID: "netstore", Label: "Netstore", Lane: "data", Pkg: "pkg/storer/netstore.go", X: 0.26, Y: 0.62,
		Description: "Serves reads: local reserve/cache first, then network retrieval.",
		Spans:       []string{"get-chunk", "put-direct-upload"},
		Blind:       []string{"No metrics of its own. Visible only through OTel spans (tracing must be enabled)."},
	},
	{
		ID: "retrieval", Label: "Retrieval", Lane: "data", Pkg: "pkg/retrieval", X: 0.48, Y: 0.62,
		Description: "Fetches chunks from peers (origin and forwarding) and serves peer requests.",
		Activity:    []MetricRef{m("bee_retrieval_request_count", "outbound requests"), m("bee_retrieval_total_retrieved", "served")},
		Errors:      []MetricRef{m("bee_retrieval_request_failure_count", "failed requests"), m("bee_retrieval_total_errors", "handler errors")},
		Gauges:      []MetricRef{m("bee_retrieval_request_success_count", "successful requests (total)")},
		Loggers:     []string{"retrieval"},
		Spans:       []string{"retrieve-chunk", "handle-retrieve-chunk"},
		Blind:       []string{"No in-flight gauge.", "retrieve-chunk span has no peer attribute."},
	},
	{
		ID: "puller", Label: "Puller", Lane: "data", Pkg: "pkg/puller", X: 0.26, Y: 0.86,
		Description: "Schedules historical and live sync of every bin within the storage radius.",
		Activity:    []MetricRef{m("bee_puller_synced_chunks", "chunks synced")},
		Errors:      []MetricRef{m("bee_puller_worker_errors", "worker errors")},
		Queue:       &MetricRef{Name: "bee_puller_worker", Label: "active sync workers"},
		Gauges:      []MetricRef{m("bee_puller_pullsync_rate", "historical pullsync rate (chunks/s, 10 min window)")},
		Loggers:     []string{"puller"},
		Endpoints:   []string{"GET /status (pullsyncRate)"},
		Blind:       []string{"No spans."},
	},
	{
		ID: "pullsync", Label: "Pullsync", Lane: "data", Pkg: "pkg/pullsync", X: 0.48, Y: 0.86,
		Description: "Offer/want protocol that copies chunks between neighbors.",
		Activity:    []MetricRef{m("bee_pullsync_chunks_delivered", "delivered to us"), m("bee_pullsync_chunks_sent", "sent to peers")},
		Errors:      []MetricRef{m("bee_pullsync_received_invalid_chunks", "invalid chunks"), m("bee_pullsync_stream_limit_exceeded", "stream limit exceeded")},
		Loggers:     []string{"pullsync"},
		Blind:       []string{"No spans and no per-peer metric."},
	},
	{
		ID: "reserve", Label: "Reserve & cache", Lane: "storage", Pkg: "pkg/storer", X: 0.70, Y: 0.74,
		Description: "Local chunk storage: the reserve (neighborhood responsibility) and the cache.",
		Activity:    []MetricRef{{Name: "bee_localstore_method_calls", Label: "storer method calls"}},
		Errors:      []MetricRef{{Name: "bee_localstore_method_calls", Match: map[string]string{"status": "failure"}, Label: "failed method calls"}},
		Gauges: []MetricRef{
			m("bee_localstore_reserve_size", "reserve size"),
			m("bee_localstore_reserve_size_within_radius", "reserve within radius"),
			m("bee_localstore_storage_radius", "storage radius"),
			m("bee_localstore_cache_size", "cache size"),
			m("bee_localstore_evicted_count", "evicted (total)"),
		},
		Loggers:   []string{"storer", "leveldb"},
		Endpoints: []string{"GET /status", "GET /reservestate"},
		Blind:     []string{"Sharky metrics exist in code but are never registered."},
	},
	{
		ID: "libp2p", Label: "libp2p", Lane: "network", Pkg: "pkg/p2p/libp2p", X: 0.92, Y: 0.40,
		Description: "Transport: connections, streams, protocol handlers, reachability.",
		Activity:    []MetricRef{m("bee_libp2p_handled_stream_count", "inbound streams"), m("bee_libp2p_created_stream_count", "outbound streams")},
		Errors:      []MetricRef{m("bee_libp2p_stream_reset_count", "stream resets"), m("bee_libp2p_stream_handler_error_reset_count", "handler error resets")},
		Gauges: []MetricRef{
			m("bee_libp2p_disconnect_count", "disconnects (total)"),
			m("bee_libp2p_kickedout_peers_count", "kicked out peers (total)"),
			m("bee_libp2p_blocklisted_peer_count", "blocklisted (total)"),
		},
		Loggers:   []string{"libp2p", "handshake"},
		Endpoints: []string{"GET /peers", "GET /addresses", "GET /blocklist"},
	},
	{
		ID: "kademlia", Label: "Kademlia", Lane: "network", Pkg: "pkg/topology/kademlia", X: 0.70, Y: 0.30,
		Description: "Routing table: bins, depth, connection decisions.",
		Activity:    []MetricRef{m("bee_kademlia_total_inbound_connections", "inbound connections"), m("bee_kademlia_total_outbound_connections", "outbound connections")},
		Errors:      []MetricRef{m("bee_kademlia_total_outbound_connection_failed_attempts", "failed outbound attempts")},
		Gauges: []MetricRef{
			m("bee_kademlia_currently_connected_peers", "connected peers"),
			m("bee_kademlia_current_depth", "depth"),
			m("bee_kademlia_current_storage_depth", "storage depth"),
		},
		Loggers:   []string{"kademlia"},
		Endpoints: []string{"GET /topology"},
	},
	{
		ID: "hive", Label: "Hive", Lane: "network", Pkg: "pkg/hive", X: 0.70, Y: 0.08,
		Description: "Peer discovery gossip.",
		Activity:    []MetricRef{m("bee_hive_peers_handler_count", "peer messages handled"), m("bee_hive_broadcast_peers_count", "broadcasts")},
		Errors:      []MetricRef{m("bee_hive_peer_underlay_err_count", "underlay errors"), m("bee_hive_store_peer_err_count", "store errors"), m("bee_hive_gossip_coalesce_dropped_total", "gossip dropped")},
		Queue:       &MetricRef{Name: "bee_hive_gossip_coalesce_buffer_size", Label: "gossip coalesce buffer"},
		Loggers:     []string{"hive"},
		Blind:       []string{"No spans."},
	},
	{
		ID: "salud", Label: "Salud", Lane: "network", Pkg: "pkg/salud", X: 0.92, Y: 0.12,
		Description: "Health scoring of peers and network radius estimation.",
		Gauges: []MetricRef{
			m("bee_salud_healthy", "healthy peers"),
			m("bee_salud_unhealthy", "unhealthy peers"),
			m("bee_salud_network_radius", "network radius"),
			m("bee_salud_neighborhood_radius", "neighborhood radius"),
		},
		Loggers: []string{"salud"},
		Blind:   []string{"Only gauges; no activity counter."},
	},
	{
		ID: "accounting", Label: "Accounting & settlement", Lane: "economy", Pkg: "pkg/accounting", X: 0.70, Y: 0.52,
		Description: "Per-peer balances, refreshments and payments.",
		Activity:    []MetricRef{m("bee_accounting_debit_events_count", "debits"), m("bee_accounting_credit_events_count", "credits")},
		Errors:      []MetricRef{m("bee_accounting_settle_error_count", "settle errors"), m("bee_accounting_payment_error_count", "payment errors")},
		Gauges:      []MetricRef{m("bee_accounting_disconnects_overdraw_count", "overdraw disconnects (total)")},
		Loggers:     []string{"accounting", "pseudosettle", "swap", "chequebook", "swapprotocol"},
		Endpoints:   []string{"GET /accounting", "GET /settlements"},
	},
	{
		ID: "postage", Label: "Postage", Lane: "economy", Pkg: "pkg/postage", X: 0.06, Y: 0.80,
		Description: "Batches, stamp issuing and the on-chain batch event listener.",
		Loggers:     []string{"postage", "batchservice", "batchstore", "listener"},
		Endpoints:   []string{"GET /stamps", "GET /chainstate"},
		Blind:       []string{"No activity metric; state comes from /stamps and /chainstate polling and logs."},
	},
	{
		ID: "storageincentives", Label: "Redistribution agent", Lane: "economy", Pkg: "pkg/storageincentives", X: 0.92, Y: 0.70,
		Description: "Plays the redistribution game: commit, reveal, claim.",
		Errors: []MetricRef{
			m("bee_storageincentives_commit_phase_errors", "commit errors"),
			m("bee_storageincentives_reveal_phase_errors", "reveal errors"),
			m("bee_storageincentives_claim_phase_errors", "claim errors"),
			m("bee_storageincentives_backend_errors", "backend errors"),
		},
		Activity:  []MetricRef{m("bee_storageincentives_backend_calls", "chain backend calls")},
		Gauges:    []MetricRef{m("bee_storageincentives_round", "round"), m("bee_storageincentives_current_phase", "phase")},
		Loggers:   []string{"storageincentives", "redistributionContract"},
		Endpoints: []string{"GET /redistributionstate"},
		Blind:     []string{"Full nodes only.", "No spans."},
	},
	{
		ID: "chain", Label: "Chain backend", Lane: "economy", Pkg: "pkg/transaction", X: 0.92, Y: 0.92,
		Description: "Blockchain RPC: transactions, price oracle, block sync.",
		Loggers:     []string{"transaction", "priceoracle"},
		Endpoints:   []string{"GET /chainstate"},
		Blind:       []string{"No RPC latency metric is exposed."},
	},
}

// Flows are the relations drawn inside a node.
var Flows = []FlowSpec{
	{From: "api", To: "pusher", Path: "upload", Label: "upload → upload store"},
	{From: "pusher", To: "pushsync", Path: "upload", Label: "push chunk", Signal: &MetricRef{Name: "bee_pusher_total_to_push"}},
	{From: "pushsync", To: "libp2p", Path: "upload", Label: "send chunk", Signal: &MetricRef{Name: "bee_pushsync_total_sent"}},
	{From: "libp2p", To: "pushsync", Path: "upload", Label: "receive chunk", Signal: &MetricRef{Name: "bee_pushsync_total_received"}},
	{From: "pushsync", To: "reserve", Path: "upload", Label: "store in reserve", Signal: &MetricRef{Name: "bee_pushsync_storer"}},
	{From: "api", To: "netstore", Path: "download", Label: "download"},
	{From: "netstore", To: "reserve", Path: "download", Label: "local lookup"},
	{From: "netstore", To: "retrieval", Path: "download", Label: "fetch from network", Signal: &MetricRef{Name: "bee_retrieval_request_count"}},
	{From: "retrieval", To: "libp2p", Path: "download", Label: "request chunk", Signal: &MetricRef{Name: "bee_retrieval_request_count"}},
	{From: "puller", To: "pullsync", Path: "sync", Label: "sync bins", Signal: &MetricRef{Name: "bee_puller_synced_chunks"}},
	{From: "pullsync", To: "libp2p", Path: "sync", Label: "offer / want", Signal: &MetricRef{Name: "bee_pullsync_chunks_sent"}},
	{From: "pullsync", To: "reserve", Path: "sync", Label: "store synced", Signal: &MetricRef{Name: "bee_pullsync_chunks_delivered"}},
	{From: "hive", To: "kademlia", Path: "topology", Label: "learned peers", Signal: &MetricRef{Name: "bee_hive_peers_handler_count"}},
	{From: "kademlia", To: "libp2p", Path: "topology", Label: "connect", Signal: &MetricRef{Name: "bee_kademlia_total_outbound_connections"}},
	{From: "salud", To: "kademlia", Path: "topology", Label: "health verdicts"},
	{From: "pushsync", To: "accounting", Path: "economy", Label: "debit / credit"},
	{From: "retrieval", To: "accounting", Path: "economy", Label: "debit / credit"},
	{From: "storageincentives", To: "reserve", Path: "economy", Label: "reserve sample"},
	{From: "storageincentives", To: "chain", Path: "economy", Label: "commit / reveal / claim", Signal: &MetricRef{Name: "bee_storageincentives_backend_calls"}},
	{From: "postage", To: "chain", Path: "economy", Label: "batch events"},
	{From: "api", To: "postage", Path: "upload", Label: "stamp chunks"},
}

var byID = func() map[string]*Spec {
	out := make(map[string]*Spec, len(Specs))
	for i := range Specs {
		out[Specs[i].ID] = &Specs[i]
	}
	return out
}()

// ByID returns the spec of a component.
func ByID(id string) (*Spec, bool) {
	s, ok := byID[id]
	return s, ok
}

var byLogger = func() map[string]string {
	out := map[string]string{}
	for _, s := range Specs {
		for _, l := range s.Loggers {
			out[l] = s.ID
		}
	}
	return out
}()

// ForLogger maps a Bee logger name ("node/pushsync/...") to a component.
// The empty string means the line belongs to no known component.
func ForLogger(logger string) string {
	seg := logger
	if len(seg) > 5 && seg[:5] == "node/" {
		seg = seg[5:]
	}
	for i := 0; i < len(seg); i++ {
		if seg[i] == '/' {
			seg = seg[:i]
			break
		}
	}
	return byLogger[seg]
}

var bySpan = func() map[string]string {
	out := map[string]string{}
	for _, s := range Specs {
		for _, n := range s.Spans {
			if _, dup := out[n]; !dup {
				out[n] = s.ID
			}
		}
	}
	return out
}()

// ForSpan maps an OTel span name to a component.
func ForSpan(name string) string { return bySpan[name] }
