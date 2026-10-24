package logs

import (
	"beescope/pkg/component"
	"beescope/pkg/model"
)

// FlowHint describes per-peer protocol activity named by a log line.
type FlowHint struct {
	Protocol string
	Peer     string
	Inbound  bool // data moved from Peer to this node
	Err      bool
}

// Class is the classification of one log line.
type Class struct {
	Component string
	Kind      string // event kind; empty = activity only
	Severity  model.Severity
	Summary   string
	Flow      *FlowHint
	Corr      *model.Correlation
	// Aggregate makes the engine coalesce bursts of identical events.
	Aggregate bool
}

type rule struct {
	kind    string
	sev     model.Severity
	summary string
	flow    string // protocol, if the line names a peer
	inbound bool
	flowErr bool
	event   bool
	agg     bool
}

// rules are keyed by exact Bee log message. Each message was taken from the
// Bee source; see the comment next to each one.
var rules = map[string]rule{
	// pkg/topology/kademlia/kademlia.go
	"connected to peer":                              {kind: "peer.connected", sev: model.SevInfo, summary: "peer connected", event: true},
	"disconnected peer":                              {kind: "peer.disconnected", sev: model.SevWarn, summary: "peer disconnected", event: true},
	"kademlia set storage radius":                    {kind: "radius.set", sev: model.SevInfo, summary: "storage radius set", event: true},
	"connected to bootnode":                          {kind: "peer.bootnode", sev: model.SevInfo, summary: "connected to bootnode", event: true},
	"kademlia: no connected peers, trying bootnodes": {kind: "peer.isolated", sev: model.SevWarn, summary: "no connected peers, trying bootnodes", event: true, agg: true},
	// pkg/puller/puller.go
	"radius decrease":                                             {kind: "puller.radius_decrease", sev: model.SevWarn, summary: "puller radius decreased", event: true},
	"warmup period complete, starting worker":                     {kind: "puller.warmup_done", sev: model.SevInfo, summary: "warmup complete, sync workers started", event: true},
	"sync peer failed":                                            {kind: "puller.sync_failed", sev: model.SevWarn, summary: "sync with peer failed", event: true, agg: true, flow: "pullsync", inbound: true, flowErr: true},
	"syncWorker interval failed":                                  {kind: "puller.interval_failed", sev: model.SevWarn, summary: "sync interval failed", event: true, agg: true, flow: "pullsync", inbound: true, flowErr: true},
	"syncWorker interval failed, quitting":                        {kind: "puller.worker_quit", sev: model.SevError, summary: "sync worker quit after repeated failures", event: true, agg: true, flow: "pullsync", inbound: true, flowErr: true},
	"peer epoch change detected, resetting past synced intervals": {kind: "puller.epoch_reset", sev: model.SevWarn, summary: "peer epoch changed, resync from scratch", event: true},
	// pkg/pushsync/pushsync.go
	"chunk pushed":           {flow: "pushsync"},
	"shallow receipt":        {kind: "pushsync.shallow_receipt", sev: model.SevWarn, summary: "shallow receipt", event: true, agg: true, flow: "pushsync"},
	"could not push to peer": {kind: "pushsync.push_failed", sev: model.SevWarn, summary: "push to peer failed", event: true, agg: true, flow: "pushsync", flowErr: true},
	"no peers left":          {kind: "noPeersLeft", sev: model.SevWarn, summary: "no peers left to try", event: true, agg: true},
	// pkg/retrieval/retrieval.go
	"retrieved chunk":     {flow: "retrieval", inbound: true},
	"failed to get chunk": {kind: "retrieval.peer_failed", sev: model.SevWarn, summary: "chunk request to peer failed", event: true, agg: true, flow: "retrieval", inbound: true, flowErr: true},
	"retrieval failed":    {kind: "retrieval.failed", sev: model.SevWarn, summary: "retrieval failed", event: true, agg: true},
	// pkg/puller/puller.go
	"disconnecting peer": {kind: "puller.peer_removed", sev: model.SevInfo, summary: "puller dropped peer", event: true},
}

// peerKeys are the field names Bee uses for a peer overlay address.
var peerKeys = []string{"peer_address", "peer", "address"}

// Classify maps a parsed line to a component, an optional event and an
// optional per-peer flow.
func Classify(l *model.LogLine) Class {
	c := Class{Component: component.ForLogger(l.Logger)}
	corr := correlation(l)
	r, known := rules[l.Msg]
	if known && r.kind == "noPeersLeft" {
		r.kind = c.Component + ".no_peers_left"
	}
	if known && r.flow != "" && corr != nil && corr.Peer != "" {
		c.Flow = &FlowHint{Protocol: r.flow, Peer: corr.Peer, Inbound: r.inbound, Err: r.flowErr}
	}
	switch {
	case known && r.event:
		c.Kind, c.Severity, c.Summary, c.Aggregate = r.kind, r.sev, r.summary, r.agg
	case l.Level == "error":
		c.Kind, c.Severity, c.Summary, c.Aggregate = "log.error", model.SevError, l.Msg, true
	case l.Level == "warning":
		c.Kind, c.Severity, c.Summary, c.Aggregate = "log.warning", model.SevWarn, l.Msg, true
	case l.Level == "info":
		c.Kind, c.Severity, c.Summary, c.Aggregate = "log.info", model.SevInfo, l.Msg, true
	}
	if c.Kind != "" && l.Error != "" {
		c.Summary += ": " + l.Error
	} else if c.Kind != "" && l.Fields["error"] != "" {
		c.Summary += ": " + l.Fields["error"]
	}
	c.Corr = corr
	return c
}

func correlation(l *model.LogLine) *model.Correlation {
	var c model.Correlation
	for _, k := range peerKeys {
		if v := l.Fields[k]; isOverlay(v) {
			c.Peer = v
			break
		}
	}
	if v := l.Fields["chunk_address"]; v != "" {
		c.Chunk = v
	}
	if v := l.Fields["batch_id"]; v != "" {
		c.BatchID = v
	}
	if v := l.Fields["traceID"]; v != "" {
		c.TraceID = v
	}
	if c == (model.Correlation{}) {
		return nil
	}
	return &c
}

// isOverlay reports whether s looks like a 32-byte hex overlay address.
func isOverlay(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
