// Package engine is the single goroutine that turns observations into the
// current cluster state, timeline events, per-edge flows, operations and
// issues, and publishes them to the history store and to subscribers.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"beescope/pkg/anomaly"
	"beescope/pkg/collect"
	"beescope/pkg/component"
	"beescope/pkg/history"
	"beescope/pkg/model"
	"beescope/pkg/source/logs"
	"beescope/pkg/source/metrics"
)

// Config configures the engine.
type Config struct {
	Mode      string // live, replay, sim
	Target    string
	Intervals collect.Intervals
	Clock     func() time.Time
	LogStats  func() (lines, dropped uint64)
	OTLP      string
	Log       *slog.Logger
}

// Engine owns all mutable state. Only Run's goroutine mutates it; HTTP
// handlers read published copies.
type Engine struct {
	cfg  Config
	in   <-chan model.Observation
	hist *history.Store
	hub  *Hub

	nodes     map[string]*nodeRT
	byOverlay map[string]string
	seq       uint64
	tracker   *anomaly.Tracker
	churn     map[string][]anomaly.ChurnEvent
	logGroups map[string]map[string]*anomaly.ErrGroup
	aggs      map[string]*pendingAgg
	flowAcc   map[flowKey]*model.Flow
	pending   []model.Event
	simulated bool
	lastKey   time.Time
	netRadius *uint8

	reqs chan func()

	opsMu sync.RWMutex
	ops   *ops

	snapJSON atomic.Pointer[[]byte]
	started  time.Time
	nObs     atomic.Uint64
	nEvents  atomic.Uint64
	nSpans   atomic.Uint64
}

type pendingAgg struct {
	ev    model.Event
	count int
	first time.Time
	group *anomaly.ErrGroup
}

type flowKey struct{ from, to, proto, src string }

// New returns an engine reading from in.
func New(cfg Config, in <-chan model.Observation, hist *history.Store, hub *Hub) *Engine {
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	anomaly.SetLoggerMapper(component.ForLogger)
	e := &Engine{
		cfg: cfg, in: in, hist: hist, hub: hub,
		nodes:     map[string]*nodeRT{},
		byOverlay: map[string]string{},
		tracker:   anomaly.NewTracker(20*time.Second, 15*time.Minute, cfg.Mode == "sim"),
		churn:     map[string][]anomaly.ChurnEvent{},
		logGroups: map[string]map[string]*anomaly.ErrGroup{},
		aggs:      map[string]*pendingAgg{},
		flowAcc:   map[flowKey]*model.Flow{},
		ops:       newOps(),
		started:   time.Now(),
		reqs:      make(chan func()),
	}
	e.ops.sim = cfg.Mode == "sim"
	e.simulated = cfg.Mode == "sim"
	empty := []byte("null")
	e.snapJSON.Store(&empty)
	return e
}

// Run processes observations until ctx ends or in closes.
func (e *Engine) Run(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	fast := time.NewTicker(250 * time.Millisecond)
	defer fast.Stop()
	n := 0
	for {
		select {
		case <-ctx.Done():
			return
		case o, ok := <-e.in:
			if !ok {
				// The source ended (a replay reached its end). Keep serving
				// the final state; a nil channel never receives again.
				e.in = nil
				e.flushAggs(true)
				e.add(model.Event{Kind: "collector.source_ended", Severity: model.SevInfo, Provenance: model.Observed,
					Source: model.Source{Adapter: "engine", Ref: e.cfg.Mode}, Summary: "observation source ended; showing the final state"})
				e.tick()
				continue
			}
			e.observe(o)
		case <-fast.C:
			n++
			e.flushAggs(false)
			if n%2 == 0 {
				e.flushFlows()
			}
			e.flushEvents()
		case <-tick.C:
			e.tick()
		case fn := <-e.reqs:
			fn()
		}
	}
}

// Do runs fn on the engine goroutine, so it may read engine state safely.
func (e *Engine) Do(ctx context.Context, fn func()) error {
	done := make(chan struct{})
	select {
	case e.reqs <- func() { fn(); close(done) }:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SnapshotJSON returns the latest published snapshot.
func (e *Engine) SnapshotJSON() []byte { return *e.snapJSON.Load() }

// LastSeq returns the newest event sequence.
func (e *Engine) LastSeq() uint64 { return atomic.LoadUint64(&e.seq) }

// Operations returns all operations, newest first.
func (e *Engine) Operations() []model.Operation {
	e.opsMu.RLock()
	defer e.opsMu.RUnlock()
	return e.ops.list()
}

// Operation returns one operation.
func (e *Engine) Operation(id string) (model.Operation, bool) {
	e.opsMu.RLock()
	defer e.opsMu.RUnlock()
	return e.ops.get(id)
}

func (e *Engine) now() time.Time { return e.cfg.Clock() }

func (e *Engine) prov(p model.Provenance) model.Provenance {
	if e.simulated {
		return model.Simulated
	}
	return p
}

// add stores and queues an event for publishing.
func (e *Engine) add(ev model.Event) uint64 {
	seq := atomic.AddUint64(&e.seq, 1)
	ev.Seq = seq
	ev.IngestedAt = time.Now()
	if ev.ObservedAt.IsZero() {
		ev.ObservedAt = e.now()
	}
	ev.Provenance = e.prov(ev.Provenance)
	if ev.Severity == "" {
		ev.Severity = model.SevInfo
	}
	e.hist.AddEvent(ev)
	e.pending = append(e.pending, ev)
	e.nEvents.Add(1)
	return seq
}

func (e *Engine) flushEvents() {
	if len(e.pending) == 0 {
		return
	}
	f, err := encodeFrame("events", e.pending[len(e.pending)-1].Seq, e.pending)
	e.pending = e.pending[:0]
	if err != nil {
		e.cfg.Log.Error("encode events", "error", err)
		return
	}
	e.hub.Publish(f)
}

// aggregate coalesces bursts of the same event into one entry per second.
func (e *Engine) aggregate(key string, ev model.Event, group *anomaly.ErrGroup) {
	if a, ok := e.aggs[key]; ok {
		a.count++
		return
	}
	e.aggs[key] = &pendingAgg{ev: ev, count: 1, first: time.Now(), group: group}
}

func (e *Engine) flushAggs(all bool) {
	for k, a := range e.aggs {
		if !all && time.Since(a.first) < time.Second {
			continue
		}
		ev := a.ev
		if a.count > 1 {
			if ev.Attrs == nil {
				ev.Attrs = map[string]string{}
			}
			ev.Attrs["count"] = fmt.Sprint(a.count)
			ev.Summary = fmt.Sprintf("%s (×%d in 1s)", ev.Summary, a.count)
		}
		seq := e.add(ev)
		if a.group != nil {
			a.group.LastSeq = seq
		}
		delete(e.aggs, k)
	}
}

func (e *Engine) flushFlows() {
	if len(e.flowAcc) == 0 {
		return
	}
	frame := history.FlowFrame{At: e.now()}
	for _, f := range e.flowAcc {
		frame.Flows = append(frame.Flows, *f)
	}
	clear(e.flowAcc)
	e.hist.AddFlows(frame)
	if f, err := encodeFrame("flows", 0, frame); err == nil {
		e.hub.Publish(f)
	}
}

func (e *Engine) addFlow(from, to, proto, src string, isErr bool) {
	k := flowKey{from, to, proto, src}
	f, ok := e.flowAcc[k]
	if !ok {
		f = &model.Flow{From: from, To: to, Protocol: proto, Source: src, Prov: e.prov(model.Observed)}
		e.flowAcc[k] = f
	}
	f.Count++
	if isErr {
		f.Errors++
	}
}

// resolvePeer maps an overlay to a node name ("" if outside the cluster).
func (e *Engine) resolvePeer(overlay string) string { return e.byOverlay[overlay] }

func (e *Engine) observe(o model.Observation) {
	e.nObs.Add(1)
	if o.Simulated {
		e.simulated = true
		e.ops.sim = true
	}
	if o.Kind == model.ObsPollError && o.Node == "" {
		e.add(model.Event{ObservedAt: o.ObservedAt, Kind: "collector.error", Severity: model.SevError, Provenance: model.Observed, Source: o.Source, Summary: o.Source.Ref + " failed: " + o.Err})
		return
	}
	if o.Kind == model.ObsSpan {
		e.observeSpan(o)
		return
	}
	if o.Node == "" {
		return
	}
	n := e.node(o.Node)
	switch o.Kind {
	case model.ObsNodeSeen:
		e.onNodeSeen(n, o)
	case model.ObsNodeGone:
		e.setPhase(n, model.PhaseGone, "no longer discovered", o.ObservedAt, o.Source, model.Observed)
	case model.ObsPollError:
		e.onPollError(n, o)
	case model.ObsHealth:
		e.srcOK(n, o)
		e.onHealth(n, o)
	case model.ObsStatus:
		e.srcOK(n, o)
		e.onStatus(n, o)
	case model.ObsTopology:
		e.srcOK(n, o)
		e.onTopology(n, o)
	case model.ObsMetrics:
		e.srcOK(n, o)
		e.onMetrics(n, o)
	case model.ObsStamps:
		e.srcOK(n, o)
		e.onStamps(n, o)
	case model.ObsChainState:
		e.srcOK(n, o)
		n.st.ChainState = o.ChainState
	case model.ObsRedistribution:
		e.srcOK(n, o)
		e.onRedistribution(n, o)
	case model.ObsTags:
		e.srcOK(n, o)
		e.onTags(n, o)
	case model.ObsLog:
		e.onLog(n, o)
	case model.ObsLogDropped:
		n.st.LogDropped += o.Dropped
		e.aggregate("logdrop:"+n.st.Name, model.Event{ObservedAt: o.ObservedAt, Node: n.st.Name, Kind: "collector.log_dropped", Severity: model.SevWarn, Provenance: model.Observed, Source: o.Source,
			Summary: fmt.Sprintf("bee-scope dropped %d log lines (engine backpressure); activity from logs is under-counted", o.Dropped)}, nil)
	case model.ObsLogStream:
		e.onLogStream(n, o)
	}
}

func (e *Engine) onNodeSeen(n *nodeRT, o model.Observation) {
	if o.Info != nil {
		n.st.Pod, n.st.Group = o.Info.Pod, o.Info.Group
	}
	if n.st.Phase == model.PhaseGone || n.st.Phase == "" {
		n.st.Phase = model.PhaseUnknown
		n.st.PhaseSince = o.ObservedAt
	}
	e.add(model.Event{ObservedAt: o.ObservedAt, Node: n.st.Name, Kind: "node.discovered", Severity: model.SevInfo, Provenance: model.Observed, Source: o.Source,
		Summary: fmt.Sprintf("discovered %s (pod %s)", n.st.Name, n.st.Pod)})
}

func (e *Engine) srcOK(n *nodeRT, o model.Observation) *model.SourceHealth {
	s := n.src(o.Source.Ref, e.interval(o.Source.Ref))
	if s.LastErr != "" && !s.LastOK.IsZero() {
		e.add(model.Event{ObservedAt: o.ObservedAt, Node: n.st.Name, Kind: "poll.recovered", Severity: model.SevInfo, Provenance: model.Observed, Source: o.Source,
			Summary: o.Source.Ref + " answers again"})
	}
	s.LastOK, s.LastTry, s.LastErr, s.LatencyMS = o.ObservedAt, o.ObservedAt, "", o.LatencyMS
	if o.Raw != nil {
		n.raw[o.Source.Ref] = e.hist.AddRaw(o.Raw)
	}
	return s
}

func (e *Engine) interval(ref string) time.Duration {
	iv := e.cfg.Intervals
	switch ref {
	case "GET /health":
		return iv.Health
	case "GET /status":
		return iv.Status
	case "GET /topology":
		return iv.Topology
	case "GET /metrics":
		return iv.Metrics
	case "GET /tags":
		return iv.Tags
	case "GET /stamps":
		return iv.Stamps
	case "GET /chainstate":
		return iv.ChainState
	case "GET /redistributionstate":
		return iv.Redistribution
	}
	return 10 * time.Second
}

// proxyDown reports whether a 503 came from the kube API-server proxy (the
// pod is not reachable) rather than from Bee itself.
func proxyDown(err string) bool {
	return strings.Contains(err, "no endpoints available") || strings.Contains(err, "error trying to reach service") ||
		strings.Contains(err, "dial tcp") || strings.Contains(err, "connection refused")
}

func (e *Engine) onPollError(n *nodeRT, o model.Observation) {
	s := n.src(o.Source.Ref, e.interval(o.Source.Ref))
	first := s.LastErr == ""
	s.LastTry, s.LastErr = o.ObservedAt, o.Err
	if o.Raw != nil {
		n.raw[o.Source.Ref] = e.hist.AddRaw(o.Raw)
	}
	switch {
	case o.ErrCode == 503 && !proxyDown(o.Err) && o.Source.Ref != "GET /health":
		if o.Source.Ref == "GET /status" || o.Source.Ref == "GET /topology" {
			e.setPhase(n, model.PhaseGated, o.Source.Ref+" → 503: "+o.Err, o.ObservedAt, o.Source, model.Observed)
		}
	case o.Source.Ref == "GET /health":
		e.setPhase(n, model.PhaseUnreachable, o.Err, o.ObservedAt, o.Source, model.Observed)
	case o.Source.Ref == "GET /redistributionstate" && (o.ErrCode == 404 || o.ErrCode == 400 || o.ErrCode == 403):
		// light node or storage incentives disabled: not an error
		s.LastErr = ""
		return
	}
	if first {
		sev := model.SevWarn
		if o.ErrCode == 0 {
			sev = model.SevError
		}
		e.add(model.Event{ObservedAt: o.ObservedAt, Node: n.st.Name, Kind: "poll.error", Severity: sev, Provenance: model.Observed, Source: o.Source,
			Summary: fmt.Sprintf("%s failed: %s", o.Source.Ref, o.Err), Attrs: map[string]string{"code": fmt.Sprint(o.ErrCode)}})
	}
}

func (e *Engine) setPhase(n *nodeRT, p model.Phase, reason string, at time.Time, src model.Source, prov model.Provenance) {
	if n.st.Phase == p {
		n.st.PhaseReason = reason
		return
	}
	prev := n.st.Phase
	n.st.Phase, n.st.PhaseSince, n.st.PhaseReason = p, at, reason
	sev := model.SevInfo
	switch p {
	case model.PhaseUnreachable, model.PhaseGone:
		sev = model.SevError
	case model.PhaseGated:
		sev = model.SevWarn
	}
	summary := fmt.Sprintf("%s: %s → %s", n.st.Name, prev, p)
	if reason != "" && p != model.PhaseUp {
		summary += " (" + reason + ")"
	}
	e.add(model.Event{ObservedAt: at, Node: n.st.Name, Component: "api", Kind: "node." + string(p), Severity: sev, Provenance: prov, Source: src, Summary: summary})
}

func (e *Engine) onHealth(n *nodeRT, o model.Observation) {
	h := o.Health
	if n.st.Version != "" && n.st.Version != h.Version {
		e.add(model.Event{ObservedAt: o.ObservedAt, Node: n.st.Name, Kind: "node.version", Severity: model.SevWarn, Provenance: model.Derived, Source: o.Source,
			Summary: fmt.Sprintf("version changed %s → %s", n.st.Version, h.Version)})
	}
	n.st.Version = h.Version
	if n.st.Phase != model.PhaseUp && n.st.Phase != model.PhaseGated {
		e.setPhase(n, model.PhaseUp, "", o.ObservedAt, o.Source, model.Observed)
	}
}

func (e *Engine) onStatus(n *nodeRT, o model.Observation) {
	s, prev := o.Status, n.st.Status
	// Only /health moves a node out of unknown; a good /status ends gating.
	if n.st.Phase == model.PhaseGated {
		e.setPhase(n, model.PhaseUp, "", o.ObservedAt, o.Source, model.Observed)
	}
	if s.Overlay != "" && s.Overlay != n.st.Overlay {
		if n.st.Overlay != "" {
			delete(e.byOverlay, n.st.Overlay)
			e.add(model.Event{ObservedAt: o.ObservedAt, Node: n.st.Name, Kind: "node.overlay_changed", Severity: model.SevWarn, Provenance: model.Derived, Source: o.Source,
				Summary: fmt.Sprintf("overlay changed %s → %s (new identity / wiped data dir)", short(n.st.Overlay), short(s.Overlay))})
		}
		n.st.Overlay = s.Overlay
		e.byOverlay[s.Overlay] = n.st.Name
	}
	n.st.Mode = s.BeeMode
	ev := func(kind string, sev model.Severity, comp, summary string) {
		e.add(model.Event{ObservedAt: o.ObservedAt, Node: n.st.Name, Component: comp, Kind: kind, Severity: sev, Provenance: model.Derived, Source: o.Source, Summary: summary})
	}
	if prev != nil {
		if prev.StorageRadius != s.StorageRadius {
			ev("radius.changed", model.SevWarn, "reserve", fmt.Sprintf("storage radius %d → %d", prev.StorageRadius, s.StorageRadius))
		}
		if prev.CommittedDepth != s.CommittedDepth {
			ev("radius.committed_depth", model.SevInfo, "reserve", fmt.Sprintf("committed depth %d → %d", prev.CommittedDepth, s.CommittedDepth))
		}
		if prev.IsWarmingUp && !s.IsWarmingUp {
			ev("node.warmed_up", model.SevInfo, "puller", "warmup finished")
		}
		if prev.IsReachable != s.IsReachable {
			ev("node.reachability", model.SevWarn, "libp2p", fmt.Sprintf("isReachable %t → %t", prev.IsReachable, s.IsReachable))
		}
		if (prev.PullsyncRate > 0) != (s.PullsyncRate > 0) {
			if s.PullsyncRate > 0 {
				ev("sync.historical_started", model.SevInfo, "puller", fmt.Sprintf("historical sync running (pullsyncRate %.2f chunks/s)", s.PullsyncRate))
			} else {
				ev("sync.historical_done", model.SevInfo, "puller", "historical sync caught up (pullsyncRate 0)")
			}
		}
		if prev.NeighborhoodSize > 0 && s.NeighborhoodSize == 0 {
			ev("topology.no_neighbors", model.SevWarn, "kademlia", "no neighbors left within storage radius")
		}
	}
	n.st.Status = s
	ns := n.series
	at := o.ObservedAt
	ns.Get(anomaly.SPullsyncRate).Add(at, s.PullsyncRate)
	ns.Get(anomaly.SRadius).Add(at, float64(s.StorageRadius))
	ns.Get(anomaly.SReserveInRadius).Add(at, float64(s.ReserveSizeWithinRadius))
}

func (e *Engine) onTopology(n *nodeRT, o model.Observation) {
	t := o.Topology
	at := o.ObservedAt
	if n.topo != nil {
		if n.topo.Depth != t.Depth {
			e.add(model.Event{ObservedAt: at, Node: n.st.Name, Component: "kademlia", Kind: "topology.depth", Severity: model.SevInfo, Provenance: model.Derived, Source: o.Source,
				Summary: fmt.Sprintf("kademlia depth %d → %d", n.topo.Depth, t.Depth)})
		}
		if n.topo.Reachability != t.Reachability {
			e.add(model.Event{ObservedAt: at, Node: n.st.Name, Component: "libp2p", Kind: "topology.reachability", Severity: model.SevWarn, Provenance: model.Derived, Source: o.Source,
				Summary: fmt.Sprintf("reachability %s → %s", n.topo.Reachability, t.Reachability)})
		}
		// Peer diffs from two polls are only used when the node's kademlia
		// debug log is not available (the log is exact, polls miss flaps).
		if !n.kadLogs(at) {
			cur := map[string]model.KadPeer{}
			for _, p := range t.Peers {
				cur[p.Address] = p
			}
			for addr, p := range cur {
				if _, ok := n.peers[addr]; !ok {
					e.churnEvent(n, at, addr, true, o.Source, model.Derived, fmt.Sprintf("bin %d", p.Bin))
				}
			}
			for addr := range n.peers {
				if _, ok := cur[addr]; !ok {
					e.churnEvent(n, at, addr, false, o.Source, model.Derived, "")
				}
			}
		}
	}
	n.topo = t
	n.peers = make(map[string]model.KadPeer, len(t.Peers))
	unhealthy := 0
	for _, p := range t.Peers {
		n.peers[p.Address] = p
		if !p.Healthy {
			unhealthy++
		}
	}
	n.st.Depth, n.st.Reachability, n.st.Population, n.st.Connected = t.Depth, t.Reachability, t.Population, t.Connected
	n.st.BinConnected = t.BinConnected
	n.st.Unhealthy = unhealthy
	n.series.Get(anomaly.SConnected).Add(at, float64(t.Connected))
}

func (e *Engine) churnEvent(n *nodeRT, at time.Time, peer string, connected bool, src model.Source, prov model.Provenance, extra string) {
	peerNode := e.resolvePeer(peer)
	label := short(peer)
	if peerNode != "" {
		label = peerNode
	}
	kind, sev, verb := "peer.connected", model.SevInfo, "connected to"
	if !connected {
		kind, sev, verb = "peer.disconnected", model.SevWarn, "disconnected from"
	}
	sum := fmt.Sprintf("%s %s", verb, label)
	if extra != "" {
		sum += " (" + extra + ")"
	}
	seq := e.add(model.Event{ObservedAt: at, Node: n.st.Name, Component: "kademlia", Kind: kind, Severity: sev, Provenance: prov, Source: src,
		Corr: &model.Correlation{Peer: peer, PeerNode: peerNode}, Summary: sum})
	e.churn[n.st.Name] = append(e.churn[n.st.Name], anomaly.ChurnEvent{T: at, Peer: peer, PeerNode: peerNode, Connected: connected, Seq: seq})
}

func (e *Engine) onMetrics(n *nodeRT, o model.Observation) {
	cur := o.Metrics
	at := o.ObservedAt
	dt := at.Sub(n.metricsAt).Seconds()
	prev := n.metrics
	if prev != nil {
		// A drop of a monotonic counter means the process restarted.
		a, ok1 := metrics.Value(prev, component.MetricRef{Name: "bee_libp2p_created_connection_count"})
		b, ok2 := metrics.Value(cur, component.MetricRef{Name: "bee_libp2p_created_connection_count"})
		if ok1 && ok2 && b < a {
			e.add(model.Event{ObservedAt: at, Node: n.st.Name, Kind: "node.restarted", Severity: model.SevWarn, Provenance: model.Derived, Source: o.Source,
				Summary: "Bee process restarted (monotonic counters reset)"})
		}
	}
	for _, spec := range component.Specs {
		mp := &metricPart{at: at}
		have := false
		addRate := func(ref component.MetricRef, isErr bool) {
			v, ok := metrics.Value(cur, ref)
			if !ok {
				return
			}
			have = true
			r, rok := metrics.Rate(prev, cur, ref, dt)
			sig := model.Signal{Name: ref.Name, Label: ref.Label, Value: r, Unit: "/s", Kind: "rate", Source: model.Source{Adapter: "metrics", Ref: ref.Name}, Prov: model.Derived}
			if !rok {
				sig.Kind, sig.Value, sig.Unit, sig.Prov = "total", v, "", model.Observed
			}
			if isErr {
				mp.errRate += r
				sig.Bad = r > 0
			} else {
				mp.activity += r
			}
			mp.signals = append(mp.signals, sig)
		}
		for _, r := range spec.Activity {
			addRate(r, false)
		}
		for _, r := range spec.Errors {
			addRate(r, true)
		}
		if spec.Queue != nil {
			if v, ok := metrics.Value(cur, *spec.Queue); ok {
				have = true
				q := v
				mp.queue = &q
				mp.signals = append(mp.signals, model.Signal{Name: spec.Queue.Name, Label: spec.Queue.Label, Value: v, Kind: "gauge", Source: model.Source{Adapter: "metrics", Ref: spec.Queue.Name}, Prov: model.Observed})
			}
		}
		for _, g := range spec.Gauges {
			if v, ok := metrics.Value(cur, g); ok {
				have = true
				mp.signals = append(mp.signals, model.Signal{Name: g.Name, Label: g.Label, Value: v, Kind: "gauge", Source: model.Source{Adapter: "metrics", Ref: g.Name}, Prov: model.Observed})
			}
		}
		mp.have = have
		mp.hasActivityMetric = have && len(spec.Activity) > 0
		n.metricParts[spec.ID] = mp
	}
	n.flowRates = map[string]float64{}
	for _, f := range component.Flows {
		if f.Signal == nil {
			continue
		}
		if r, ok := metrics.Rate(prev, cur, *f.Signal, dt); ok {
			n.flowRates[f.From+">"+f.To] = r
		}
	}
	ns := n.series
	rec := func(name string, ref component.MetricRef) {
		if v, ok := metrics.Value(cur, ref); ok {
			ns.Get(name).Add(at, v)
		}
	}
	rec(anomaly.SSyncedHist, component.MetricRef{Name: "bee_puller_synced_chunks", Match: map[string]string{"type": "historical"}})
	rec(anomaly.SSyncedLive, component.MetricRef{Name: "bee_puller_synced_chunks", Match: map[string]string{"type": "live"}})
	rec(anomaly.SPullerWorkers, component.MetricRef{Name: "bee_puller_worker"})
	rec(anomaly.SToPush, component.MetricRef{Name: "bee_pusher_total_to_push"})
	rec(anomaly.SPushSynced, component.MetricRef{Name: "bee_pusher_total_synced"})
	rec(anomaly.SPushErrors, component.MetricRef{Name: "bee_pusher_total_errors"})
	rec(anomaly.SPushAttempts, component.MetricRef{Name: "bee_pushsync_total_send_attempts"})
	rec(anomaly.SPushFailed, component.MetricRef{Name: "bee_pushsync_total_failed_send_attempts"})
	rec(anomaly.SShallow, component.MetricRef{Name: "bee_pushsync_shallow_receipt"})
	rec(anomaly.SRetrReq, component.MetricRef{Name: "bee_retrieval_request_count"})
	rec(anomaly.SRetrFail, component.MetricRef{Name: "bee_retrieval_request_failure_count"})
	rec(anomaly.SAPIReq, component.MetricRef{Name: "bee_api_request_count"})
	rec(anomaly.SAPI5xx, component.MetricRef{Name: "bee_api_response_code_count", LabelPrefix: map[string]string{"code": "5"}})
	rec(anomaly.SDisconnects, component.MetricRef{Name: "bee_libp2p_disconnect_count"})
	rec(anomaly.SLogErrors, component.MetricRef{Name: "bee_log_error_count"})
	n.metrics, n.metricsAt = cur, at
}

func (e *Engine) onStamps(n *nodeRT, o model.Observation) {
	prev := map[string]model.Stamp{}
	for _, s := range n.st.Stamps {
		prev[s.BatchID] = s
	}
	for _, s := range o.Stamps {
		p, ok := prev[s.BatchID]
		corr := &model.Correlation{BatchID: s.BatchID}
		switch {
		case !ok && n.stampsSeen:
			e.add(model.Event{ObservedAt: o.ObservedAt, Node: n.st.Name, Component: "postage", Kind: "stamp.new", Severity: model.SevInfo, Provenance: model.Derived, Source: o.Source, Corr: corr,
				Summary: fmt.Sprintf("new batch %s (depth %d, usable=%t)", short(s.BatchID), s.Depth, s.Usable)})
		case ok && p.Usable != s.Usable:
			sev := model.SevInfo
			if !s.Usable {
				sev = model.SevWarn
			}
			e.add(model.Event{ObservedAt: o.ObservedAt, Node: n.st.Name, Component: "postage", Kind: "stamp.usable", Severity: sev, Provenance: model.Derived, Source: o.Source, Corr: corr,
				Summary: fmt.Sprintf("batch %s usable %t → %t", short(s.BatchID), p.Usable, s.Usable)})
		}
	}
	n.stampsSeen = true
	n.st.Stamps = o.Stamps
}

func (e *Engine) onRedistribution(n *nodeRT, o model.Observation) {
	r, p := o.Redistribution, n.st.Redistribution
	ev := func(kind string, sev model.Severity, summary string) {
		e.add(model.Event{ObservedAt: o.ObservedAt, Node: n.st.Name, Component: "storageincentives", Kind: kind, Severity: sev, Provenance: model.Derived, Source: o.Source, Summary: summary})
	}
	if p != nil {
		if p.IsFrozen != r.IsFrozen {
			if r.IsFrozen {
				ev("redistribution.frozen", model.SevError, fmt.Sprintf("node frozen (round %d)", r.Round))
			} else {
				ev("redistribution.unfrozen", model.SevInfo, "node no longer frozen")
			}
		}
		if p.LastWonRound != r.LastWonRound && r.LastWonRound > 0 {
			ev("redistribution.won", model.SevInfo, fmt.Sprintf("won round %d", r.LastWonRound))
		}
		if p.LastSelectedRound != r.LastSelectedRound && r.LastSelectedRound > 0 {
			ev("redistribution.selected", model.SevInfo, fmt.Sprintf("neighborhood selected in round %d", r.LastSelectedRound))
		}
		if p.IsHealthy != r.IsHealthy {
			sev := model.SevInfo
			if !r.IsHealthy {
				sev = model.SevWarn
			}
			ev("redistribution.health", sev, fmt.Sprintf("isHealthy %t → %t", p.IsHealthy, r.IsHealthy))
		}
		if p.Phase != r.Phase {
			ev("redistribution.phase", model.SevDebug, fmt.Sprintf("phase %s → %s (round %d, sampled every %s)", p.Phase, r.Phase, r.Round, e.cfg.Intervals.Redistribution))
		}
	}
	n.st.Redistribution = r
}

func (e *Engine) onTags(n *nodeRT, o model.Observation) {
	e.opsMu.Lock()
	started, finished := e.ops.observeTags(n.st.Name, o.Tags, o.ObservedAt)
	e.opsMu.Unlock()
	for _, t := range started {
		e.add(model.Event{ObservedAt: o.ObservedAt, Node: n.st.Name, Component: "pusher", Kind: "upload.started", Severity: model.SevInfo, Provenance: model.Observed, Source: o.Source,
			Corr: &model.Correlation{TagID: t.UID}, Summary: fmt.Sprintf("upload tag %d: %d chunks split", t.UID, t.Split),
			Attrs: map[string]string{"operation": fmt.Sprintf("tag:%s:%d", n.st.Name, t.UID)}})
	}
	for _, t := range finished {
		e.add(model.Event{ObservedAt: o.ObservedAt, Node: n.st.Name, Component: "pusher", Kind: "upload.synced", Severity: model.SevInfo, Provenance: model.Observed, Source: o.Source,
			Corr: &model.Correlation{TagID: t.UID}, Summary: fmt.Sprintf("upload tag %d fully synced (%d chunks)", t.UID, t.Synced),
			Attrs: map[string]string{"operation": fmt.Sprintf("tag:%s:%d", n.st.Name, t.UID)}})
	}
}

func (e *Engine) onLogStream(n *nodeRT, o model.Observation) {
	if o.Err != "" {
		if n.st.LogStream != "error" {
			e.add(model.Event{ObservedAt: o.ObservedAt, Node: n.st.Name, Kind: "collector.log_stream", Severity: model.SevWarn, Provenance: model.Observed, Source: o.Source,
				Summary: "log stream unavailable: " + o.Err})
		}
		n.st.LogStream = "error"
		return
	}
	n.st.LogStream = "following"
}

func (e *Engine) onLog(n *nodeRT, o model.Observation) {
	l := o.Log
	cl := logs.Classify(l)
	comp := cl.Component
	n.logCount[comp]++
	if l.Level == "error" || l.Level == "warning" {
		n.logErrCount[comp]++
	}
	if comp != "" {
		n.loggerSeen[comp] = o.ObservedAt
	}
	logID := e.hist.AddLog(n.st.Name, comp, *l)
	if cl.Corr != nil && cl.Corr.Peer != "" {
		cl.Corr.PeerNode = e.resolvePeer(cl.Corr.Peer)
	}
	if cl.Flow != nil {
		peer := cl.Corr.PeerNode
		if peer == "" {
			peer = "ext:" + short(cl.Flow.Peer)
		}
		from, to := n.st.Name, peer
		if cl.Flow.Inbound {
			from, to = peer, n.st.Name
		}
		e.addFlow(from, to, cl.Flow.Protocol, "log", cl.Flow.Err)
	}
	if cl.Kind == "" {
		return
	}
	attrs := map[string]string{"logger": l.Logger, "log_id": fmt.Sprint(logID)}
	for k, v := range l.Fields {
		if len(attrs) >= 16 {
			break
		}
		attrs[k] = v
	}
	if l.Caller != "" {
		attrs["caller"] = l.Caller
	}
	ev := model.Event{ObservedAt: o.ObservedAt, Node: n.st.Name, Component: comp, Kind: cl.Kind, Severity: cl.Severity, Provenance: model.Observed,
		Source: model.Source{Adapter: "log", Ref: l.Logger}, Corr: cl.Corr, Summary: cl.Summary, Attrs: attrs}
	switch cl.Kind {
	case "peer.connected", "peer.disconnected":
		n.kadLogAt = o.ObservedAt
		e.churnEvent(n, o.ObservedAt, cl.Corr.Peer, cl.Kind == "peer.connected", ev.Source, model.Observed, "")
		return
	}
	if !cl.Aggregate {
		e.add(ev)
		return
	}
	var group *anomaly.ErrGroup
	if l.Level == "error" || l.Level == "warning" {
		gm := e.logGroups[n.st.Name]
		if gm == nil {
			gm = map[string]*anomaly.ErrGroup{}
			e.logGroups[n.st.Name] = gm
		}
		gk := l.Logger + "|" + l.Msg
		group = gm[gk]
		if group == nil {
			group = &anomaly.ErrGroup{Level: l.Level, Logger: l.Logger, Msg: l.Msg, First: o.ObservedAt}
			gm[gk] = group
		}
		group.Count++
		group.Last = o.ObservedAt
		group.Example = l.Raw
	}
	key := n.st.Name + "|" + cl.Kind + "|" + l.Logger + "|" + l.Msg
	e.aggregate(key, ev, group)
}

func (e *Engine) observeSpan(o model.Observation) {
	sp := o.Span
	e.nSpans.Add(1)
	node := e.resolvePeer(sp.Overlay)
	if sp.Attrs == nil {
		sp.Attrs = map[string]string{}
	}
	if node != "" {
		sp.Attrs["bee.node"] = node
	}
	e.opsMu.Lock()
	op := e.ops.observeSpan(sp, node)
	opID := op.ID
	e.opsMu.Unlock()
	comp := component.ForSpan(sp.Name)
	if node != "" {
		n := e.node(node)
		n.spanCount[comp]++
		n.spanSeen[comp] = o.ObservedAt
	}
	corr := &model.Correlation{TraceID: sp.TraceID, SpanID: sp.SpanID, Chunk: sp.Attrs["swarm.chunk.address"], Peer: sp.Attrs["swarm.peer.address"]}
	if corr.Peer != "" {
		corr.PeerNode = e.resolvePeer(corr.Peer)
		if node != "" {
			proto := "pushsync"
			if strings.Contains(sp.Name, "retrieve") {
				proto = "retrieval"
			}
			peer := corr.PeerNode
			if peer == "" {
				peer = "ext:" + short(corr.Peer)
			}
			e.addFlow(node, peer, proto, "otlp", sp.StatusError)
		}
	}
	if sp.StatusError || sp.ParentSpanID == "" {
		sev, kind := model.SevInfo, "trace.root"
		sum := fmt.Sprintf("%s (%s)", sp.Name, sp.End.Sub(sp.Start).Round(time.Millisecond))
		if sp.StatusError {
			sev, kind = model.SevWarn, "trace.error"
			sum += ": " + sp.StatusMsg
		}
		e.add(model.Event{ObservedAt: sp.End, Node: node, Component: comp, Kind: kind, Severity: sev, Provenance: model.Observed, Source: o.Source, Corr: corr, Summary: sum,
			Attrs: map[string]string{"operation": opID}})
	}
}

// tick recomputes derived state, runs the anomaly rules and publishes.
func (e *Engine) tick() {
	now := e.now()
	e.flushAggs(false)
	for _, n := range e.nodes {
		e.refreshNode(n, now)
	}
	edges := e.edges()
	e.netRadius = e.modalRadius()
	e.opsMu.Lock()
	e.ops.refresh(now)
	opsList := e.ops.list()
	e.opsMu.Unlock()
	e.pruneWindows(now)

	snap := &model.Snapshot{At: now, Mode: e.cfg.Mode, Target: e.cfg.Target, Nodes: map[string]*model.NodeState{}, Edges: edges, NetworkRadius: e.netRadius}
	for name, n := range e.nodes {
		snap.Nodes[name] = n.st
	}
	in := &anomaly.Input{Now: now, Snap: snap, Series: map[string]anomaly.NodeSeries{}, Churn: e.churn, LogGroups: map[string][]anomaly.ErrGroup{}, Ops: opsList}
	for name, n := range e.nodes {
		in.Series[name] = n.series
	}
	for node, gm := range e.logGroups {
		for _, g := range gm {
			in.LogGroups[node] = append(in.LogGroups[node], *g)
		}
		sort.Slice(in.LogGroups[node], func(i, j int) bool { return in.LogGroups[node][i].Msg < in.LogGroups[node][j].Msg })
	}
	for _, tr := range e.tracker.Update(in) {
		is := tr.Issue
		kind, sev, verb := "issue.opened", is.Severity, "detected"
		if !tr.Opened {
			kind, sev, verb = "issue.resolved", model.SevInfo, "resolved"
		}
		prov := model.Derived
		if is.Class != model.Confirmed {
			prov = model.Inferred
		}
		node := ""
		if len(is.Nodes) > 0 {
			node = is.Nodes[0]
		}
		comp := ""
		if len(is.Components) > 0 {
			comp = is.Components[0]
		}
		e.add(model.Event{ObservedAt: now, Node: node, Component: comp, Kind: kind, Severity: sev, Provenance: prov, Source: model.Source{Adapter: "engine", Ref: "rule " + is.Rule},
			Summary: fmt.Sprintf("%s %s: %s", is.Class, verb, is.Title), Attrs: map[string]string{"issue": is.ID}})
	}
	snap.Issues = e.tracker.Issues()
	for _, n := range e.nodes {
		n.st.IssueCount, n.st.Worst = 0, ""
	}
	for _, is := range snap.Issues {
		if is.ResolvedAt != nil {
			continue
		}
		for _, nn := range is.Nodes {
			if n, ok := e.nodes[nn]; ok {
				n.st.IssueCount++
				if history.SevRank(is.Severity) > history.SevRank(n.st.Worst) || n.st.Worst == "" {
					n.st.Worst = is.Severity
				}
			}
		}
	}
	snap.Collector = e.stats()
	e.flushEvents()
	js, err := json.Marshal(snap)
	if err != nil {
		e.cfg.Log.Error("encode snapshot", "error", err)
		return
	}
	e.snapJSON.Store(&js)
	e.hub.Publish(rawFrame("snapshot", 0, js))
	if now.Sub(e.lastKey) >= 5*time.Second {
		e.hist.AddKeyframe(now, js)
		e.lastKey = now
	}
}

func (e *Engine) stats() model.CollectorStats {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	evs, oldest, keys := e.hist.Stats()
	st := model.CollectorStats{
		StartedAt: e.started, Observations: e.nObs.Load(), Events: e.nEvents.Load(), EventsStored: evs, OldestEvent: oldest, Keyframes: keys,
		Subscribers: e.hub.Count(), Spans: e.nSpans.Load(), OTLPListening: e.cfg.OTLP, HeapMB: float64(ms.HeapAlloc) / (1 << 20),
		Goroutines: runtime.NumGoroutine(), ObsBacklog: len(e.in),
	}
	if e.cfg.LogStats != nil {
		st.LogLines, st.LogDropped = e.cfg.LogStats()
	}
	return st
}

func (e *Engine) pruneWindows(now time.Time) {
	for node, cs := range e.churn {
		i := 0
		for i < len(cs) && now.Sub(cs[i].T) > 10*time.Minute {
			i++
		}
		if i == len(cs) {
			delete(e.churn, node)
		} else {
			e.churn[node] = cs[i:]
		}
	}
	for node, gm := range e.logGroups {
		for k, g := range gm {
			if now.Sub(g.First) > 2*time.Minute {
				delete(gm, k)
			}
		}
		if len(gm) == 0 {
			delete(e.logGroups, node)
		}
	}
}

func (e *Engine) modalRadius() *uint8 {
	count := map[uint8]int{}
	for _, n := range e.nodes {
		if n.st.Phase == model.PhaseUp && n.st.Status != nil && n.st.Mode != "light" && n.st.Group != "bootnode" && !n.st.Status.IsWarmingUp {
			count[n.st.Status.StorageRadius]++
		}
	}
	if len(count) == 0 {
		return nil
	}
	var best uint8
	bc := -1
	for r, c := range count {
		if c > bc || (c == bc && r < best) {
			best, bc = r, c
		}
	}
	return &best
}

func (e *Engine) edges() []model.Edge {
	type key struct{ a, b string }
	m := map[key]*model.Edge{}
	for name, n := range e.nodes {
		if n.topo == nil || n.st.Phase == model.PhaseGone {
			continue
		}
		at := n.st.Sources["GET /topology"].LastOK
		ext := 0
		for _, p := range n.topo.Peers {
			peer := e.resolvePeer(p.Address)
			if peer == "" {
				ext++
				continue
			}
			k := key{name, peer}
			aSide := true
			if peer < name {
				k, aSide = key{peer, name}, false
			}
			ed, ok := m[k]
			if !ok {
				ed = &model.Edge{A: k.a, B: k.b, Bin: p.Bin, Healthy: true, Prov: e.prov(model.Observed), FreshAt: at}
				m[k] = ed
			}
			if aSide {
				ed.SeenByA = true
			} else {
				ed.SeenByB = true
			}
			ed.Healthy = ed.Healthy && p.Healthy
			ed.LatencyMS = max(ed.LatencyMS, p.LatencyMS)
			if at.Before(ed.FreshAt) {
				ed.FreshAt = at
			}
		}
		n.st.ExternalPeers = ext
	}
	out := make([]model.Edge, 0, len(m))
	for _, ed := range m {
		out = append(out, *ed)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].A != out[j].A {
			return out[i].A < out[j].A
		}
		return out[i].B < out[j].B
	})
	return out
}

func short(a string) string {
	if len(a) > 8 {
		return a[:8]
	}
	return a
}
