package model

import "time"

// Provenance says how much a piece of information can be trusted.
type Provenance string

const (
	// Observed: read directly from one source (an API response, a metric
	// sample, a log line, a span).
	Observed Provenance = "observed"
	// Derived: computed from two or more observations (a counter rate, a
	// diff between two /topology polls, a cluster-wide modal radius).
	Derived Provenance = "derived"
	// Inferred: a suspicion or hypothesis built on indirect evidence.
	Inferred Provenance = "inferred"
	// Simulated: produced by the simulation source. Never real telemetry.
	Simulated Provenance = "simulated"
)

// Severity of an event or issue.
type Severity string

const (
	SevDebug Severity = "debug"
	SevInfo  Severity = "info"
	SevWarn  Severity = "warn"
	SevError Severity = "error"
)

// Source identifies where an observation came from.
type Source struct {
	Adapter string `json:"adapter"` // api, metrics, log, otlp, kube, sim, engine
	Ref     string `json:"ref"`     // e.g. "GET /topology", "bee_pusher_total_errors", log logger name
}

// ObsKind discriminates Observation payloads.
type ObsKind string

const (
	ObsNodeSeen       ObsKind = "node_seen"
	ObsNodeGone       ObsKind = "node_gone"
	ObsPollError      ObsKind = "poll_error"
	ObsHealth         ObsKind = "health"
	ObsStatus         ObsKind = "status"
	ObsTopology       ObsKind = "topology"
	ObsMetrics        ObsKind = "metrics"
	ObsStamps         ObsKind = "stamps"
	ObsChainState     ObsKind = "chainstate"
	ObsRedistribution ObsKind = "redistribution"
	ObsTags           ObsKind = "tags"
	ObsLog            ObsKind = "log"
	ObsLogDropped     ObsKind = "log_dropped"
	ObsLogStream      ObsKind = "log_stream"
	ObsSpan           ObsKind = "span"
)

// NodeInfo describes a discovered Bee node.
type NodeInfo struct {
	Name    string `json:"name"`
	Pod     string `json:"pod,omitempty"`
	Group   string `json:"group,omitempty"` // bootnode, bee, light...
	BaseURL string `json:"baseUrl,omitempty"`
}

// Observation is the single envelope every source emits. Exactly one payload
// field is set, according to Kind. It is JSON-serializable so that a session
// can be recorded and replayed deterministically.
type Observation struct {
	Kind       ObsKind   `json:"kind"`
	Node       string    `json:"node,omitempty"`
	Source     Source    `json:"source"`
	ObservedAt time.Time `json:"observedAt"`
	Simulated  bool      `json:"simulated,omitempty"`
	LatencyMS  float64   `json:"latencyMs,omitempty"` // request duration for polled sources

	Err            string          `json:"err,omitempty"`
	ErrCode        int             `json:"errCode,omitempty"`
	Info           *NodeInfo       `json:"nodeInfo,omitempty"`
	Health         *Health         `json:"health,omitempty"`
	Status         *Status         `json:"status,omitempty"`
	Topology       *Topology       `json:"topology,omitempty"`
	Metrics        Metrics         `json:"metrics,omitempty"`
	Stamps         []Stamp         `json:"stamps,omitempty"`
	ChainState     *ChainState     `json:"chainState,omitempty"`
	Redistribution *Redistribution `json:"redistribution,omitempty"`
	Tags           []Tag           `json:"tags,omitempty"`
	Log            *LogLine        `json:"log,omitempty"`
	Dropped        int             `json:"dropped,omitempty"`
	Span           *Span           `json:"span,omitempty"`
	Raw            RawJSON         `json:"raw,omitempty"`
}

// Correlation identifiers attached to an event when the source carried them.
type Correlation struct {
	TraceID  string `json:"traceId,omitempty"`
	SpanID   string `json:"spanId,omitempty"`
	Chunk    string `json:"chunk,omitempty"`
	Peer     string `json:"peer,omitempty"`     // peer overlay address
	PeerNode string `json:"peerNode,omitempty"` // peer resolved to a node in this cluster
	TagID    uint64 `json:"tagId,omitempty"`
	BatchID  string `json:"batchId,omitempty"`
}

// Event is one entry on the timeline.
type Event struct {
	Seq        uint64            `json:"seq"`
	ObservedAt time.Time         `json:"t"`
	IngestedAt time.Time         `json:"ingestedAt"`
	Node       string            `json:"node,omitempty"`
	Component  string            `json:"component,omitempty"`
	Kind       string            `json:"kind"`
	Severity   Severity          `json:"sev"`
	Provenance Provenance        `json:"prov"`
	Source     Source            `json:"source"`
	Corr       *Correlation      `json:"corr,omitempty"`
	Summary    string            `json:"summary"`
	Attrs      map[string]string `json:"attrs,omitempty"`
	RawRef     uint64            `json:"rawRef,omitempty"`
}

// Phase of a node as seen by the collector.
type Phase string

const (
	PhaseUnknown     Phase = "unknown"
	PhaseUp          Phase = "up"
	PhaseGated       Phase = "gated"       // API answers 503 (still starting / syncing)
	PhaseUnreachable Phase = "unreachable" // API does not answer
	PhaseGone        Phase = "gone"        // no longer discovered
)

// ComponentStatus is the visual state of one subsystem.
type ComponentStatus string

const (
	CompActive       ComponentStatus = "active"
	CompIdle         ComponentStatus = "idle"
	CompDegraded     ComponentStatus = "degraded"
	CompFailed       ComponentStatus = "failed"
	CompUnobservable ComponentStatus = "unobservable"
	CompUnknown      ComponentStatus = "unknown"
)

// Signal is one number behind a component's status.
type Signal struct {
	Name   string     `json:"name"`
	Label  string     `json:"label"`
	Value  float64    `json:"value"`
	Unit   string     `json:"unit,omitempty"`
	Kind   string     `json:"kind"` // rate, gauge, total
	Bad    bool       `json:"bad,omitempty"`
	Source Source     `json:"source"`
	Prov   Provenance `json:"prov"`
}

// ComponentState is the live state of one subsystem inside one node.
type ComponentState struct {
	ID         string          `json:"id"`
	Status     ComponentStatus `json:"status"`
	Reason     string          `json:"reason,omitempty"`
	Activity   float64         `json:"activity"`  // ops/s, best available measure
	ErrorRate  float64         `json:"errorRate"` // errors/s
	Queue      *float64        `json:"queue,omitempty"`
	LogRate    float64         `json:"logRate"` // log lines/s attributed to this component
	Signals    []Signal        `json:"signals,omitempty"`
	Prov       Provenance      `json:"prov"`
	FreshAt    time.Time       `json:"freshAt"`
	Stale      bool            `json:"stale,omitempty"`
	Observable []string        `json:"observable,omitempty"` // which sources currently feed it
}

// SourceHealth tracks freshness of one polled source for one node.
type SourceHealth struct {
	Ref       string    `json:"ref"`
	LastOK    time.Time `json:"lastOk"`
	LastTry   time.Time `json:"lastTry"`
	LastErr   string    `json:"lastErr,omitempty"`
	Interval  float64   `json:"intervalS"`
	Stale     bool      `json:"stale"`
	LatencyMS float64   `json:"latencyMs"`
}

// NodeState is the collector's current view of one node.
type NodeState struct {
	Name           string                     `json:"name"`
	Pod            string                     `json:"pod,omitempty"`
	Group          string                     `json:"group,omitempty"`
	Phase          Phase                      `json:"phase"`
	PhaseSince     time.Time                  `json:"phaseSince"`
	PhaseReason    string                     `json:"phaseReason,omitempty"`
	Version        string                     `json:"version,omitempty"`
	Overlay        string                     `json:"overlay,omitempty"`
	Mode           string                     `json:"mode,omitempty"`
	Status         *Status                    `json:"status,omitempty"`
	Depth          uint8                      `json:"depth"`
	Reachability   string                     `json:"reachability,omitempty"`
	Population     int                        `json:"population"`
	Connected      int                        `json:"connected"`
	BinConnected   [32]int                    `json:"binConnected"`
	Unhealthy      int                        `json:"unhealthyPeers"`
	ExternalPeers  int                        `json:"externalPeers"`
	Redistribution *Redistribution            `json:"redistribution,omitempty"`
	ChainState     *ChainState                `json:"chainState,omitempty"`
	Stamps         []Stamp                    `json:"stamps,omitempty"`
	Components     map[string]*ComponentState `json:"components"`
	FlowRates      map[string]float64         `json:"flowRates,omitempty"` // "from>to" internal flow rate (metric-derived)
	Sources        map[string]*SourceHealth   `json:"sources"`
	LogStream      string                     `json:"logStream"` // following, off, error
	LogDropped     int                        `json:"logDropped"`
	IssueCount     int                        `json:"issueCount"`
	Worst          Severity                   `json:"worst,omitempty"`
}

// Edge is an observed connection between two nodes of the cluster.
type Edge struct {
	A         string     `json:"a"`
	B         string     `json:"b"`
	Bin       int        `json:"bin"`
	LatencyMS float64    `json:"latencyMs"`
	Healthy   bool       `json:"healthy"`
	SeenByA   bool       `json:"seenByA"`
	SeenByB   bool       `json:"seenByB"`
	Prov      Provenance `json:"prov"`
	FreshAt   time.Time  `json:"freshAt"`
}

// Flow is aggregated per-edge protocol activity observed in one interval.
// Flows are only produced from sources that name both ends (log lines and
// spans that carry a peer address), never from aggregate metrics.
type Flow struct {
	From     string     `json:"from"`
	To       string     `json:"to"`
	Protocol string     `json:"protocol"` // pushsync, retrieval, pullsync, hive
	Count    int        `json:"count"`
	Errors   int        `json:"errors"`
	Prov     Provenance `json:"prov"`
	Source   string     `json:"source"` // log, otlp
}

// IssueClass distinguishes certainty.
type IssueClass string

const (
	Confirmed  IssueClass = "confirmed"
	Suspicious IssueClass = "suspicious"
	Hypothesis IssueClass = "hypothesis"
)

// Evidence links an issue to the facts behind it.
type Evidence struct {
	Text     string     `json:"text"`
	EventSeq uint64     `json:"eventSeq,omitempty"`
	Node     string     `json:"node,omitempty"`
	Signal   string     `json:"signal,omitempty"`
	Value    string     `json:"value,omitempty"`
	At       time.Time  `json:"at"`
	Source   Source     `json:"source"`
	Prov     Provenance `json:"prov"`
}

// Issue is a detected anomaly with its explanation.
type Issue struct {
	ID         string     `json:"id"`
	Rule       string     `json:"rule"`
	Title      string     `json:"title"`
	Class      IssueClass `json:"class"`
	Severity   Severity   `json:"sev"`
	Nodes      []string   `json:"nodes"`
	Components []string   `json:"components"`
	StartedAt  time.Time  `json:"startedAt"`
	LastSeenAt time.Time  `json:"lastSeenAt"`
	ResolvedAt *time.Time `json:"resolvedAt,omitempty"`
	What       string     `json:"what"`
	Where      string     `json:"where"`
	Evidence   []Evidence `json:"evidence"`
	Hypothesis string     `json:"hypothesis,omitempty"`
	NextSteps  []string   `json:"nextSteps"`
	Simulated  bool       `json:"simulated,omitempty"`
}

// Stage is one step of an operation lifecycle.
type Stage struct {
	Name      string            `json:"name"`
	Node      string            `json:"node,omitempty"`
	Component string            `json:"component,omitempty"`
	State     string            `json:"state"` // done, pending, failed, running
	Start     *time.Time        `json:"start,omitempty"`
	End       *time.Time        `json:"end,omitempty"`
	Detail    string            `json:"detail,omitempty"`
	Count     uint64            `json:"count,omitempty"`
	Total     uint64            `json:"total,omitempty"`
	SpanID    string            `json:"spanId,omitempty"`
	Parent    string            `json:"parent,omitempty"`
	Attrs     map[string]string `json:"attrs,omitempty"`
}

// Operation is a correlated unit of work (an upload tag, a trace, a chunk).
type Operation struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"` // upload-tag, trace, chunk
	Title       string     `json:"title"`
	Node        string     `json:"node,omitempty"`
	Nodes       []string   `json:"nodes"`
	State       string     `json:"state"`       // running, done, failed, stalled
	Correlation string     `json:"correlation"` // observed, approximate
	Start       time.Time  `json:"start"`
	Updated     time.Time  `json:"updated"`
	Stages      []Stage    `json:"stages"`
	Prov        Provenance `json:"prov"`
	Note        string     `json:"note,omitempty"`
}

// Snapshot is the whole cluster view at one instant.
type Snapshot struct {
	At            time.Time             `json:"at"`
	Mode          string                `json:"mode"` // live, replay, sim
	Target        string                `json:"target"`
	Nodes         map[string]*NodeState `json:"nodes"`
	Edges         []Edge                `json:"edges"`
	NetworkRadius *uint8                `json:"networkRadius,omitempty"`
	Issues        []Issue               `json:"issues"`
	Collector     CollectorStats        `json:"collector"`
}

// CollectorStats exposes bee-scope's own health and overhead.
type CollectorStats struct {
	StartedAt     time.Time `json:"startedAt"`
	Observations  uint64    `json:"observations"`
	Events        uint64    `json:"events"`
	EventsStored  int       `json:"eventsStored"`
	OldestEvent   time.Time `json:"oldestEvent"`
	Keyframes     int       `json:"keyframes"`
	Subscribers   int       `json:"subscribers"`
	LogLines      uint64    `json:"logLines"`
	LogDropped    uint64    `json:"logDropped"`
	Spans         uint64    `json:"spans"`
	OTLPListening string    `json:"otlpListening,omitempty"`
	HeapMB        float64   `json:"heapMb"`
	Goroutines    int       `json:"goroutines"`
	ObsBacklog    int       `json:"obsBacklog"`
}
