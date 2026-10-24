// Package model holds every type shared between collectors, the state
// engine, the anomaly engine, the history store and the HTTP layer.
//
// The Bee wire types in this file mirror the JSON returned by Bee's HTTP API
// (bee v2, OpenAPI 8.2.x). Only fields bee-scope uses are declared.
package model

import (
	"encoding/json"
	"time"
)

// Health is GET /health.
type Health struct {
	Status     string `json:"status"`
	Version    string `json:"version"`
	APIVersion string `json:"apiVersion"`
}

// Status is the local snapshot from GET /status.
type Status struct {
	Overlay                 string  `json:"overlay"`
	BeeMode                 string  `json:"beeMode"`
	ReserveSize             uint64  `json:"reserveSize"`
	ReserveSizeWithinRadius uint64  `json:"reserveSizeWithinRadius"`
	PullsyncRate            float64 `json:"pullsyncRate"`
	StorageRadius           uint8   `json:"storageRadius"`
	ConnectedPeers          uint64  `json:"connectedPeers"`
	NeighborhoodSize        uint64  `json:"neighborhoodSize"`
	BatchCommitment         uint64  `json:"batchCommitment"`
	IsReachable             bool    `json:"isReachable"`
	LastSyncedBlock         uint64  `json:"lastSyncedBlock"`
	CommittedDepth          uint8   `json:"committedDepth"`
	IsWarmingUp             bool    `json:"isWarmingUp"`
}

// Topology is GET /topology (kademlia KadParams), with bins flattened.
type Topology struct {
	BaseAddr            string    `json:"baseAddr"`
	Population          int       `json:"population"`
	Connected           int       `json:"connected"`
	Depth               uint8     `json:"depth"`
	Reachability        string    `json:"reachability"`
	NetworkAvailability string    `json:"networkAvailability"`
	Peers               []KadPeer `json:"peers"`
	LightConnected      int       `json:"lightConnected"`
	BinConnected        [32]int   `json:"binConnected"`
	BinPopulation       [32]int   `json:"binPopulation"`
}

// KadPeer is one connected peer from a kademlia bin.
type KadPeer struct {
	Address      string  `json:"address"`
	Bin          int     `json:"bin"`
	Direction    string  `json:"direction"`
	LatencyMS    float64 `json:"latencyMs"`
	Reachability string  `json:"reachability"`
	Healthy      bool    `json:"healthy"`
	SessionS     float64 `json:"sessionS"`
}

// Stamp is one entry of GET /stamps.
type Stamp struct {
	BatchID          string  `json:"batchID"`
	Utilization      uint32  `json:"utilization"`
	UtilizationRatio float64 `json:"utilizationRatio"`
	Usable           bool    `json:"usable"`
	Label            string  `json:"label"`
	Depth            uint8   `json:"depth"`
	BucketDepth      uint8   `json:"bucketDepth"`
	Exists           bool    `json:"exists"`
	BatchTTL         int64   `json:"batchTTL"`
}

// ChainState is GET /chainstate.
type ChainState struct {
	ChainTip     uint64 `json:"chainTip"`
	Block        uint64 `json:"block"`
	CurrentPrice string `json:"currentPrice"`
}

// Redistribution is GET /redistributionstate (full nodes only).
type Redistribution struct {
	IsFrozen              bool    `json:"isFrozen"`
	IsFullySynced         bool    `json:"isFullySynced"`
	IsHealthy             bool    `json:"isHealthy"`
	HasSufficientFunds    bool    `json:"hasSufficientFunds"`
	Enabled               bool    `json:"enabled"`
	Phase                 string  `json:"phase"`
	Round                 uint64  `json:"round"`
	LastWonRound          uint64  `json:"lastWonRound"`
	LastPlayedRound       uint64  `json:"lastPlayedRound"`
	LastFrozenRound       uint64  `json:"lastFrozenRound"`
	LastSelectedRound     uint64  `json:"lastSelectedRound"`
	LastSampleDurationS   float64 `json:"lastSampleDurationSeconds"`
	Block                 uint64  `json:"block"`
	HasCommittedThisRound bool    `json:"hasCommittedThisRound"`
	HasRevealedThisRound  bool    `json:"hasRevealedThisRound"`
}

// Tag is one upload session from GET /tags.
type Tag struct {
	UID       uint64    `json:"uid"`
	Split     uint64    `json:"split"`
	Seen      uint64    `json:"seen"`
	Stored    uint64    `json:"stored"`
	Sent      uint64    `json:"sent"`
	Synced    uint64    `json:"synced"`
	Address   string    `json:"address"`
	StartedAt time.Time `json:"startedAt"`
}

// MetricSample is one Prometheus sample.
type MetricSample struct {
	Labels map[string]string `json:"labels,omitempty"`
	Value  float64           `json:"value"`
}

// Metrics is a parsed /metrics scrape keyed by metric family name. For
// histograms and summaries only the _count and _sum samples are kept.
type Metrics map[string][]MetricSample

// Sum adds all samples of a family, optionally restricted to samples whose
// labels match every key in match. ok is false when the family is absent.
func (m Metrics) Sum(name string, match map[string]string) (v float64, ok bool) {
	samples, ok := m[name]
	if !ok {
		return 0, false
	}
outer:
	for _, s := range samples {
		for k, want := range match {
			if s.Labels[k] != want {
				continue outer
			}
		}
		v += s.Value
	}
	return v, true
}

// LogLine is one parsed Bee log line.
type LogLine struct {
	Time    time.Time         `json:"time"`
	Level   string            `json:"level"`
	V       int               `json:"v,omitempty"`
	Logger  string            `json:"logger"`
	Msg     string            `json:"msg"`
	Error   string            `json:"error,omitempty"`
	Caller  string            `json:"caller,omitempty"`
	Fields  map[string]string `json:"fields,omitempty"`
	Raw     string            `json:"raw"`
	Partial bool              `json:"partial,omitempty"`
}

// Span is one OpenTelemetry span received over OTLP.
type Span struct {
	TraceID      string            `json:"traceId"`
	SpanID       string            `json:"spanId"`
	ParentSpanID string            `json:"parentSpanId,omitempty"`
	Name         string            `json:"name"`
	Kind         string            `json:"kind,omitempty"`
	Start        time.Time         `json:"start"`
	End          time.Time         `json:"end"`
	StatusError  bool              `json:"statusError,omitempty"`
	StatusMsg    string            `json:"statusMsg,omitempty"`
	Overlay      string            `json:"overlay,omitempty"` // service.instance.id
	Attrs        map[string]string `json:"attrs,omitempty"`
	Events       []SpanEvent       `json:"events,omitempty"`
}

// SpanEvent is an event recorded on a span.
type SpanEvent struct {
	Time time.Time `json:"time"`
	Name string    `json:"name"`
}

// RawJSON keeps an undecoded response body for the inspector's raw view.
type RawJSON = json.RawMessage
