// Package sim is a fake Bee cluster (one bootnode and seven bees) that emits
// the same observations the live adapters do, every one marked Simulated.
// It loops a scripted six-minute scenario so that every view and most
// anomaly rules can be exercised without a cluster:
//
//	  0– 60 s  steady state
//	 60–120 s  upload burst on bee-1 (tag, pusher counters, "chunk pushed" logs, traces)
//	120–180 s  retrieval failures on bee-2
//	150–210 s  peer flapping on bee-4 (with bee-5 and bee-7)
//	150–330 s  storage radius divergence on bee-6; its historical sync stalls after 20 s
//	240–270 s  bee-3 unreachable, then restarts (counters reset)
//
// The scenario drives the engine only through observations; it never
// produces events, issues or flows directly.
package sim

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/bits"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"beescope/pkg/collect"
	"beescope/pkg/model"
	"beescope/pkg/source/logs"
)

// Loop is the scenario length in scenario seconds.
const Loop = 360

type window struct{ from, to int }

func (w window) in(t int) bool { return t >= w.from && t < w.to }

// Scenario windows, in seconds within the loop.
var (
	upload   = window{60, 120}
	retrFail = window{120, 180}
	flap     = window{150, 210}
	stall    = window{150, 330}
	down     = window{240, 270}
)

const (
	uploader  = "bee-1"
	retriever = "bee-2"
	victim    = "bee-3"
	flapper   = "bee-4"
	staller   = "bee-6"
)

// Options configure the simulation.
type Options struct {
	// Speed multiplies scenario time (1 = real time).
	Speed     float64
	Intervals collect.Intervals
	Seed      uint64
	// Now is the clock (default time.Now). Tests drive Step with a fake one.
	Now func() time.Time
}

type mkey struct{ name, labels string }

type node struct {
	name, group string
	idx         int
	overlay     string
	addr        [32]byte
	ctr         map[mkey]float64
	gauge       map[mkey]float64
	radius      uint8
	gone        map[string]bool // peers this node is currently disconnected from
	isDown      bool
	tags        []model.Tag
	stamp       *model.Stamp
}

// Sim is the simulated cluster.
type Sim struct {
	opt     Options
	rng     *rand.Rand
	nodes   []*node
	byName  map[string]*node
	tick    int
	block   uint64
	nextUID uint64
	now     time.Time
	out     chan<- model.Observation
	lines   atomic.Uint64
}

// New builds the cluster.
func New(opt Options) *Sim {
	if opt.Speed <= 0 {
		opt.Speed = 1
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	s := &Sim{opt: opt, rng: rand.New(rand.NewPCG(opt.Seed, 0x5eed)), byName: map[string]*node{}, block: 1000, nextUID: 1}
	names := []string{"bootnode-0", "bee-1", "bee-2", "bee-3", "bee-4", "bee-5", "bee-6", "bee-7"}
	for i, name := range names {
		n := &node{name: name, group: groupOf(name), idx: i, ctr: map[mkey]float64{}, gauge: map[mkey]float64{}, gone: map[string]bool{}}
		for j := range n.addr {
			n.addr[j] = byte(s.rng.UintN(256))
		}
		// Spread the first byte so the nodes sit apart on the ring and the
		// radius-1 neighborhoods split the cluster in two.
		n.addr[0] = byte(i*32 + int(n.addr[0]%24))
		n.overlay = hex.EncodeToString(n.addr[:])
		s.nodes = append(s.nodes, n)
		s.byName[name] = n
	}
	for _, n := range s.nodes {
		s.initMetrics(n)
		// Connections made before the simulation started; a restart resets
		// this to zero, which is how the engine detects restarts.
		n.ctr[mkey{"bee_libp2p_created_connection_count", ""}] = float64(20 + 3*n.idx)
	}
	up := s.byName[uploader]
	up.stamp = &model.Stamp{BatchID: s.randHex(32), Usable: true, Label: "sim", Depth: 20, BucketDepth: 16, Exists: true, BatchTTL: 7 * 24 * 3600}
	return s
}

// LogStats reports simulated log lines (none are ever dropped).
func (s *Sim) LogStats() (lines, dropped uint64) { return s.lines.Load(), 0 }

func groupOf(name string) string {
	if i := strings.LastIndexByte(name, '-'); i > 0 {
		return name[:i]
	}
	return name
}

func (s *Sim) randHex(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(s.rng.UintN(256))
	}
	return hex.EncodeToString(b)
}

// Run emits observations until ctx ends.
func (s *Sim) Run(ctx context.Context, out chan<- model.Observation) {
	step := time.Duration(float64(time.Second) / s.opt.Speed)
	t := time.NewTicker(step)
	defer t.Stop()
	for {
		s.Step(ctx, out)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Sim) emit(ctx context.Context, o model.Observation) {
	o.Simulated = true
	if o.ObservedAt.IsZero() {
		o.ObservedAt = s.now
	}
	select {
	case s.out <- o:
	case <-ctx.Done():
	}
}

// Step advances the scenario by one second and emits what is due.
func (s *Sim) Step(ctx context.Context, out chan<- model.Observation) {
	s.out = out
	s.now = s.opt.Now()
	t := s.tick % Loop
	if s.tick == 0 {
		for _, n := range s.nodes {
			s.emit(ctx, model.Observation{Kind: model.ObsNodeSeen, Node: n.name, Source: model.Source{Adapter: "kube", Ref: "discovery"},
				Info: &model.NodeInfo{Name: n.name, Pod: n.name + "-0", Group: n.group}})
			s.emit(ctx, model.Observation{Kind: model.ObsLogStream, Node: n.name, Source: model.Source{Adapter: "log", Ref: "pod/" + n.name + "-0"}})
		}
	}
	if s.tick%5 == 0 {
		s.block++
	}
	s.scenario(ctx, t)
	for _, n := range s.nodes {
		if !n.isDown {
			s.background(ctx, n, t)
		}
	}
	for _, n := range s.nodes {
		s.polls(ctx, n)
	}
	s.tick++
}

// scenario applies the scripted faults for scenario second t.
func (s *Sim) scenario(ctx context.Context, t int) {
	// bee-3 goes away and comes back as a fresh process.
	v := s.byName[victim]
	switch {
	case down.in(t) && !v.isDown:
		v.isDown = true
		s.emit(ctx, model.Observation{Kind: model.ObsLogStream, Node: v.name, Source: model.Source{Adapter: "log", Ref: "pod/" + v.name + "-0"},
			Err: `container "bee" in pod "` + v.name + `-0" is not running`})
		for _, n := range s.nodes {
			if n != v && !n.gone[v.name] {
				s.disconnect(ctx, n, v, "peer went away")
			}
		}
	case !down.in(t) && v.isDown:
		v.isDown = false
		s.initMetrics(v) // restart: every counter starts again from zero
		v.gone = map[string]bool{}
		s.emit(ctx, model.Observation{Kind: model.ObsLogStream, Node: v.name, Source: model.Source{Adapter: "log", Ref: "pod/" + v.name + "-0"}})
		s.log(ctx, v, "info", "node", "starting bee", 0, "version", "2.7.0-sim")
		for _, n := range s.nodes {
			if n != v {
				s.connect(ctx, v, n)
				s.connect(ctx, n, v)
			}
		}
	}

	// bee-4 flaps with bee-5 and bee-7: drop every 6 s, reconnect 3 s later.
	if flap.in(t) {
		f := s.byName[flapper]
		k := t - flap.from
		peer := s.byName[[]string{"bee-5", "bee-7"}[(k/6)%2]]
		switch k % 6 {
		case 0:
			s.disconnect(ctx, f, peer, "stream reset")
			s.disconnect(ctx, peer, f, "stream reset")
		case 3:
			s.connect(ctx, f, peer)
			s.connect(ctx, peer, f)
		}
	}

	// bee-6 storage radius diverges and its historical sync stalls.
	st := s.byName[staller]
	if stall.in(t) {
		st.radius = 1
		st.gauge[mkey{"bee_puller_worker", ""}] = 4
		// The new radius starts a historical sync that makes progress for
		// 20 s and then stops while pullsyncRate still says "catching up".
		if t < stall.from+20 {
			st.ctr[mkey{"bee_puller_synced_chunks", "type=historical"}] += 4
		} else {
			st.ctr[mkey{"bee_puller_worker_errors", ""}] += 0.25
		}
		if t >= stall.from+20 && t%4 == 0 {
			p := s.peerOf(st)
			s.log(ctx, st, "debug", "puller", "syncWorker interval failed", 1, "peer_address", p.overlay, "bin", "1", "error", "context deadline exceeded")
		}
	} else {
		st.radius = 0
		st.gauge[mkey{"bee_puller_worker", ""}] = 2
	}

	// bee-1 uploads 2048 chunks.
	up := s.byName[uploader]
	if t == upload.from {
		uid := s.nextUID
		s.nextUID++
		up.tags = append(up.tags, model.Tag{UID: uid, StartedAt: s.now, Address: s.randHex(32)})
		if len(up.tags) > 5 {
			up.tags = up.tags[len(up.tags)-5:]
		}
		// The fullest bucket of a depth-20/bucket-depth-16 batch holds 16 chunks.
		up.stamp.Utilization = min(up.stamp.Utilization+1, 12)
		up.stamp.UtilizationRatio = float64(up.stamp.Utilization) / 16
		s.log(ctx, up, "info", "api", "upload started", 0, "tag", strconv.FormatUint(uid, 10))
	}
	if upload.in(t) && len(up.tags) > 0 {
		tag := &up.tags[len(up.tags)-1]
		const total = 2048
		if tag.Split < total {
			n := min(uint64(205), total-tag.Split)
			tag.Split += n
			tag.Stored += n
			up.ctr[mkey{"bee_api_request_count", ""}] += float64(n) / 16
			up.ctr[mkey{"bee_api_response_code_count", `code=201,method=POST`}] += float64(n) / 16
		}
		if t >= upload.from+2 && tag.Synced < tag.Split {
			n := min(uint64(36+s.rng.IntN(6)), tag.Split-tag.Synced)
			for i := uint64(0); i < n; i++ {
				s.pushChunk(ctx, up, i < 8)
			}
			tag.Synced += n
		}
		if t%4 == 0 {
			s.uploadTrace(ctx, up)
		}
	}

	// bee-2 fails to retrieve chunks.
	if retrFail.in(t) {
		r := s.byName[retriever]
		r.ctr[mkey{"bee_retrieval_request_count", ""}] += 8
		r.ctr[mkey{"bee_retrieval_request_failure_count", ""}] += 3
		r.ctr[mkey{"bee_retrieval_request_success_count", ""}] += 5
		r.ctr[mkey{"bee_api_request_count", ""}] += 2
		r.ctr[mkey{"bee_api_response_code_count", "code=404,method=GET"}] += 2
		for i := 0; i < 3; i++ {
			p := s.peerOf(r)
			s.log(ctx, r, "debug", "retrieval", "failed to get chunk", 1, "chunk_address", s.randHex(32), "peer_address", p.overlay, "error", "read message: stream reset")
		}
		if t%5 == 0 {
			s.retrievalTrace(ctx, r)
		}
	}
}

// background adds the steady trickle of activity every node has.
func (s *Sim) background(ctx context.Context, n *node, t int) {
	c := n.ctr
	jit := func(v float64) float64 { return v * (0.7 + 0.6*s.rng.Float64()) }
	c[mkey{"bee_api_request_count", ""}] += 1.4
	c[mkey{"bee_api_response_code_count", "code=200,method=GET"}] += 1.4
	c[mkey{"bee_libp2p_handled_stream_count", ""}] += jit(3)
	c[mkey{"bee_libp2p_created_stream_count", ""}] += jit(3)
	c[mkey{"bee_hive_peers_handler_count", ""}] += jit(0.1)
	c[mkey{"bee_hive_broadcast_peers_count", ""}] += jit(0.05)
	c[mkey{"bee_localstore_method_calls", "method=get,status=success"}] += jit(4)
	c[mkey{"bee_storageincentives_backend_calls", ""}] += jit(0.1)

	live := jit(0.4)
	c[mkey{"bee_puller_synced_chunks", "type=live"}] += live
	c[mkey{"bee_pullsync_chunks_delivered", ""}] += live
	c[mkey{"bee_pullsync_chunks_sent", ""}] += jit(0.4)
	n.gauge[mkey{"bee_localstore_reserve_size", ""}] += live
	n.gauge[mkey{"bee_localstore_reserve_size_within_radius", ""}] += live

	if n.name != retriever || !retrFail.in(t) {
		r := jit(0.3)
		c[mkey{"bee_retrieval_request_count", ""}] += r
		c[mkey{"bee_retrieval_request_success_count", ""}] += r
		c[mkey{"bee_retrieval_total_retrieved", ""}] += jit(0.3)
		c[mkey{"bee_accounting_debit_events_count", ""}] += r
		c[mkey{"bee_accounting_credit_events_count", ""}] += jit(0.3)
	}

	// A few debug lines so logger-fed components show as observable.
	switch (t + n.idx) % 7 {
	case 0:
		s.log(ctx, n, "debug", "pullsync", "make offer", 1, "bin", "0", "topmost", strconv.Itoa(s.tick))
	case 3:
		s.log(ctx, n, "debug", "hive", "broadcast peers", 1, "count", "3")
	case 5:
		s.log(ctx, n, "debug", "salud", "computed", 2, "avg_dur", "0.004", "pDur", "0.006")
	}
	n.gauge[mkey{"bee_localstore_storage_radius", ""}] = float64(n.radius)
	n.gauge[mkey{"bee_kademlia_current_storage_depth", ""}] = float64(n.radius)
	n.gauge[mkey{"bee_salud_neighborhood_radius", ""}] = float64(n.radius)
	if n.name == staller && stall.in(t) {
		n.gauge[mkey{"bee_puller_pullsync_rate", ""}] = 3.2
	} else {
		n.gauge[mkey{"bee_puller_pullsync_rate", ""}] = 0
	}
	n.gauge[mkey{"bee_storageincentives_round", ""}] = float64(s.block / 38)
	n.gauge[mkey{"bee_storageincentives_current_phase", ""}] = float64((s.block % 38) / 13)
}

func (s *Sim) initMetrics(n *node) {
	n.ctr, n.gauge = map[mkey]float64{}, map[mkey]float64{}
	for _, name := range []string{
		"bee_api_request_count", "bee_pusher_total_to_push", "bee_pusher_total_errors", "bee_pusher_total_synced",
		"bee_pushsync_total_sent", "bee_pushsync_total_received", "bee_pushsync_total_send_attempts", "bee_pushsync_total_failed_send_attempts",
		"bee_pushsync_total_handler_errors", "bee_pushsync_shallow_receipt", "bee_pushsync_forwarder", "bee_pushsync_storer", "bee_pushsync_invalid_stamps",
		"bee_retrieval_request_count", "bee_retrieval_total_retrieved", "bee_retrieval_request_failure_count", "bee_retrieval_total_errors", "bee_retrieval_request_success_count",
		"bee_puller_worker_errors", "bee_pullsync_chunks_delivered", "bee_pullsync_chunks_sent", "bee_pullsync_received_invalid_chunks", "bee_pullsync_stream_limit_exceeded",
		"bee_libp2p_handled_stream_count", "bee_libp2p_created_stream_count", "bee_libp2p_stream_reset_count", "bee_libp2p_stream_handler_error_reset_count",
		"bee_libp2p_disconnect_count", "bee_libp2p_kickedout_peers_count", "bee_libp2p_blocklisted_peer_count", "bee_libp2p_created_connection_count",
		"bee_kademlia_total_inbound_connections", "bee_kademlia_total_outbound_connections", "bee_kademlia_total_outbound_connection_failed_attempts",
		"bee_hive_peers_handler_count", "bee_hive_broadcast_peers_count", "bee_hive_peer_underlay_err_count", "bee_hive_store_peer_err_count", "bee_hive_gossip_coalesce_dropped_total",
		"bee_accounting_debit_events_count", "bee_accounting_credit_events_count", "bee_accounting_settle_error_count", "bee_accounting_payment_error_count", "bee_accounting_disconnects_overdraw_count",
		"bee_storageincentives_commit_phase_errors", "bee_storageincentives_reveal_phase_errors", "bee_storageincentives_claim_phase_errors", "bee_storageincentives_backend_errors", "bee_storageincentives_backend_calls",
		"bee_log_error_count",
	} {
		n.ctr[mkey{name, ""}] = 0
	}
	n.ctr[mkey{"bee_api_response_code_count", "code=200,method=GET"}] = 0
	n.ctr[mkey{"bee_puller_synced_chunks", "type=historical"}] = 0
	n.ctr[mkey{"bee_puller_synced_chunks", "type=live"}] = 0
	n.ctr[mkey{"bee_localstore_method_calls", "method=get,status=success"}] = 0
	n.ctr[mkey{"bee_localstore_method_calls", "method=put,status=failure"}] = 0
	for _, name := range []string{
		"bee_puller_worker", "bee_puller_pullsync_rate", "bee_localstore_reserve_size", "bee_localstore_reserve_size_within_radius", "bee_localstore_storage_radius",
		"bee_localstore_cache_size", "bee_localstore_evicted_count", "bee_kademlia_currently_connected_peers", "bee_kademlia_current_depth", "bee_kademlia_current_storage_depth",
		"bee_hive_gossip_coalesce_buffer_size", "bee_salud_healthy", "bee_salud_unhealthy", "bee_salud_network_radius", "bee_salud_neighborhood_radius",
		"bee_storageincentives_round", "bee_storageincentives_current_phase",
	} {
		n.gauge[mkey{name, ""}] = 0
	}
	n.gauge[mkey{"bee_localstore_reserve_size", ""}] = float64(4000 + 300*n.idx)
	n.gauge[mkey{"bee_localstore_reserve_size_within_radius", ""}] = float64(4000 + 300*n.idx)
	n.gauge[mkey{"bee_localstore_cache_size", ""}] = 1200
	n.gauge[mkey{"bee_puller_worker", ""}] = 2
}

// peers returns the nodes n is currently connected to.
func (s *Sim) peers(n *node) []*node {
	var out []*node
	for _, p := range s.nodes {
		if p != n && !p.isDown && !n.gone[p.name] {
			out = append(out, p)
		}
	}
	return out
}

func (s *Sim) peerOf(n *node) *node {
	ps := s.peers(n)
	if len(ps) == 0 {
		return n
	}
	return ps[s.rng.IntN(len(ps))]
}

func proximity(a, b []byte) int {
	for i := range a {
		if x := a[i] ^ b[i]; x != 0 {
			return i*8 + bits.LeadingZeros8(x)
		}
	}
	return len(a) * 8
}

func (s *Sim) closest(from *node, chunk []byte) *node {
	var best *node
	bp := -1
	for _, p := range s.peers(from) {
		if po := proximity(p.addr[:], chunk); po > bp {
			best, bp = p, po
		}
	}
	return best
}

func (s *Sim) pushChunk(ctx context.Context, up *node, logIt bool) {
	chunk := make([]byte, 32)
	for i := range chunk {
		chunk[i] = byte(s.rng.UintN(256))
	}
	target := s.closest(up, chunk)
	if target == nil {
		up.ctr[mkey{"bee_pusher_total_errors", ""}]++
		return
	}
	up.ctr[mkey{"bee_pusher_total_to_push", ""}]++
	up.ctr[mkey{"bee_pusher_total_synced", ""}]++
	up.ctr[mkey{"bee_pushsync_total_send_attempts", ""}]++
	up.ctr[mkey{"bee_pushsync_total_sent", ""}]++
	up.ctr[mkey{"bee_accounting_debit_events_count", ""}]++
	target.ctr[mkey{"bee_pushsync_total_received", ""}]++
	target.ctr[mkey{"bee_pushsync_storer", ""}]++
	target.ctr[mkey{"bee_accounting_credit_events_count", ""}]++
	target.gauge[mkey{"bee_localstore_reserve_size", ""}]++
	target.gauge[mkey{"bee_localstore_reserve_size_within_radius", ""}]++
	if logIt {
		s.log(ctx, up, "debug", "pushsync", "chunk pushed", 1, "chunk_address", hex.EncodeToString(chunk), "peer_address", target.overlay)
	}
}

func (s *Sim) disconnect(ctx context.Context, n, p *node, reason string) {
	n.gone[p.name] = true
	n.ctr[mkey{"bee_libp2p_disconnect_count", ""}]++
	s.log(ctx, n, "debug", "kademlia", "disconnected peer", 1, "peer_address", p.overlay, "reason", reason)
}

func (s *Sim) connect(ctx context.Context, n, p *node) {
	delete(n.gone, p.name)
	n.ctr[mkey{"bee_kademlia_total_outbound_connections", ""}]++
	n.ctr[mkey{"bee_libp2p_created_connection_count", ""}]++
	s.log(ctx, n, "debug", "kademlia", "connected to peer", 1, "peer_address", p.overlay, "proximity_order", strconv.Itoa(proximity(n.addr[:], p.addr[:])))
}

// log renders a line in Bee's key/value format and parses it back, so the
// simulated stream goes through the same parser as real pod logs.
func (s *Sim) log(ctx context.Context, n *node, level, logger, msg string, v int, kv ...string) {
	var b strings.Builder
	fmt.Fprintf(&b, `"time"=%q "level"=%q "logger"=%q`, s.now.UTC().Format("2006-01-02 15:04:05.000000"), level, "node/"+logger)
	if level == "debug" {
		fmt.Fprintf(&b, ` "v"=%d`, v)
	}
	fmt.Fprintf(&b, ` "msg"=%q`, msg)
	for i := 0; i+1 < len(kv); i += 2 {
		fmt.Fprintf(&b, ` %q=%q`, kv[i], kv[i+1])
	}
	l, _ := logs.Parse(b.String())
	l.Time = s.now
	s.lines.Add(1)
	s.emit(ctx, model.Observation{Kind: model.ObsLog, Node: n.name, Source: model.Source{Adapter: "log", Ref: "pod/" + n.name + "-0"}, Log: &l})
}

func (s *Sim) span(n *node, traceID, parent, name string, start, end time.Time, attrs map[string]string) model.Span {
	return model.Span{TraceID: traceID, SpanID: s.randHex(8), ParentSpanID: parent, Name: name, Kind: "SPAN_KIND_INTERNAL",
		Start: start, End: end, Overlay: n.overlay, Attrs: attrs}
}

func (s *Sim) emitSpans(ctx context.Context, spans ...model.Span) {
	for i := range spans {
		sp := spans[i]
		s.emit(ctx, model.Observation{Kind: model.ObsSpan, Source: model.Source{Adapter: "otlp", Ref: sp.Name}, ObservedAt: sp.End, Span: &sp})
	}
}

func (s *Sim) uploadTrace(ctx context.Context, up *node) {
	chunk := make([]byte, 32)
	for i := range chunk {
		chunk[i] = byte(s.rng.UintN(256))
	}
	target := s.closest(up, chunk)
	if target == nil {
		return
	}
	ch := hex.EncodeToString(chunk)
	tid := s.randHex(16)
	t0 := s.now.Add(-400 * time.Millisecond)
	root := s.span(up, tid, "", "bytes-upload", t0, t0.Add(310*time.Millisecond), map[string]string{"http.method": "POST"})
	push := s.span(up, tid, root.SpanID, "push-chunk-async", t0.Add(40*time.Millisecond), t0.Add(290*time.Millisecond),
		map[string]string{"swarm.chunk.address": ch, "swarm.peer.address": target.overlay})
	handler := s.span(target, tid, push.SpanID, "pushsync-handler", t0.Add(70*time.Millisecond), t0.Add(260*time.Millisecond),
		map[string]string{"swarm.chunk.address": ch})
	s.emitSpans(ctx, handler, push, root)
}

func (s *Sim) retrievalTrace(ctx context.Context, r *node) {
	p := s.peerOf(r)
	ch := s.randHex(32)
	tid := s.randHex(16)
	t0 := s.now.Add(-900 * time.Millisecond)
	root := s.span(r, tid, "", "bytes-download", t0, t0.Add(820*time.Millisecond), map[string]string{"http.method": "GET"})
	root.StatusError, root.StatusMsg = true, "joiner: retrieval failed"
	get := s.span(r, tid, root.SpanID, "get-chunk", t0.Add(10*time.Millisecond), t0.Add(810*time.Millisecond), map[string]string{"swarm.chunk.address": ch})
	ret := s.span(r, tid, get.SpanID, "retrieve-chunk", t0.Add(20*time.Millisecond), t0.Add(800*time.Millisecond), map[string]string{"swarm.chunk.address": ch})
	ret.StatusError, ret.StatusMsg = true, "context deadline exceeded"
	h := s.span(p, tid, ret.SpanID, "handle-retrieve-chunk", t0.Add(60*time.Millisecond), t0.Add(560*time.Millisecond), map[string]string{"swarm.chunk.address": ch})
	h.StatusError, h.StatusMsg = true, "read message: stream reset"
	s.emitSpans(ctx, h, ret, get, root)
}

// polls emits the API and metric observations due this second.
func (s *Sim) polls(ctx context.Context, n *node) {
	iv := s.opt.Intervals
	due := func(d time.Duration) bool {
		sec := int(d / time.Second)
		return sec > 0 && (s.tick+n.idx)%sec == 0
	}
	type poll struct {
		every time.Duration
		ref   string
		fn    func() model.Observation
	}
	list := []poll{
		{iv.Health, "GET /health", func() model.Observation {
			h := &model.Health{Status: "ok", Version: "2.7.0-sim", APIVersion: "8.2.1"}
			return model.Observation{Kind: model.ObsHealth, Health: h, Raw: raw(h)}
		}},
		{iv.Status, "GET /status", func() model.Observation {
			st := s.status(n)
			return model.Observation{Kind: model.ObsStatus, Status: st, Raw: raw(st)}
		}},
		{iv.Topology, "GET /topology", func() model.Observation {
			tp := s.topology(n)
			return model.Observation{Kind: model.ObsTopology, Topology: tp, Raw: raw(tp)}
		}},
		{iv.Metrics, "GET /metrics", func() model.Observation {
			return model.Observation{Kind: model.ObsMetrics, Metrics: s.metrics(n)}
		}},
		{iv.Tags, "GET /tags", func() model.Observation {
			tags := append([]model.Tag{}, n.tags...)
			return model.Observation{Kind: model.ObsTags, Tags: tags, Raw: raw(map[string]any{"tags": tags})}
		}},
		{iv.Stamps, "GET /stamps", func() model.Observation {
			var st []model.Stamp
			if n.stamp != nil {
				n.stamp.BatchTTL -= int64(iv.Stamps / time.Second)
				st = []model.Stamp{*n.stamp}
			}
			return model.Observation{Kind: model.ObsStamps, Stamps: st, Raw: raw(map[string]any{"stamps": st})}
		}},
		{iv.ChainState, "GET /chainstate", func() model.Observation {
			cs := &model.ChainState{ChainTip: s.block, Block: s.block, CurrentPrice: "24000"}
			return model.Observation{Kind: model.ObsChainState, ChainState: cs, Raw: raw(cs)}
		}},
		{iv.Redistribution, "GET /redistributionstate", func() model.Observation {
			r := s.redistribution(n)
			return model.Observation{Kind: model.ObsRedistribution, Redistribution: r, Raw: raw(r)}
		}},
	}
	for _, p := range list {
		if !due(p.every) {
			continue
		}
		var o model.Observation
		if n.isDown {
			msg := fmt.Sprintf(`http 503: no endpoints available for service "%s"`, n.name)
			o = model.Observation{Kind: model.ObsPollError, Err: msg, ErrCode: 503}
		} else {
			o = p.fn()
		}
		o.Node = n.name
		o.Source = model.Source{Adapter: "api", Ref: p.ref}
		if p.ref == "GET /metrics" {
			o.Source.Adapter = "metrics"
		}
		o.LatencyMS = 2 + 10*s.rng.Float64()
		s.emit(ctx, o)
	}
}

func raw(v any) model.RawJSON {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

func (s *Sim) status(n *node) *model.Status {
	ps := s.peers(n)
	hood := 0
	for _, p := range ps {
		if proximity(n.addr[:], p.addr[:]) >= int(n.radius) {
			hood++
		}
	}
	rate := 0.0
	if n.name == staller && stall.in(s.tick%Loop) {
		rate = 3.2
	}
	reserve := uint64(n.gauge[mkey{"bee_localstore_reserve_size", ""}])
	return &model.Status{
		Overlay: n.overlay, BeeMode: "full", ReserveSize: reserve, ReserveSizeWithinRadius: reserve, PullsyncRate: rate,
		StorageRadius: n.radius, CommittedDepth: n.radius, ConnectedPeers: uint64(len(ps)), NeighborhoodSize: uint64(hood),
		BatchCommitment: 1 << 22, IsReachable: true, LastSyncedBlock: s.block,
	}
}

func (s *Sim) topology(n *node) *model.Topology {
	ps := s.peers(n)
	t := &model.Topology{BaseAddr: n.overlay, Population: len(s.nodes) - 1, Connected: len(ps), Reachability: "Public", NetworkAvailability: "Available"}
	for _, p := range s.nodes {
		if p != n {
			t.BinPopulation[min(proximity(n.addr[:], p.addr[:]), 31)]++
		}
	}
	for _, p := range ps {
		bin := min(proximity(n.addr[:], p.addr[:]), 31)
		t.BinConnected[bin]++
		dir := "outbound"
		if p.idx < n.idx {
			dir = "inbound"
		}
		t.Peers = append(t.Peers, model.KadPeer{Address: p.overlay, Bin: bin, Direction: dir, LatencyMS: float64(1 + (n.idx*7+p.idx*3)%9),
			Reachability: "Public", Healthy: true, SessionS: float64(s.tick)})
	}
	sort.Slice(t.Peers, func(i, j int) bool { return t.Peers[i].Address < t.Peers[j].Address })
	for d := 31; d >= 0; d-- {
		if t.BinPopulation[d] > 0 {
			t.Depth = uint8(min(d, 2))
			break
		}
	}
	n.gauge[mkey{"bee_kademlia_currently_connected_peers", ""}] = float64(len(ps))
	n.gauge[mkey{"bee_kademlia_current_depth", ""}] = float64(t.Depth)
	n.gauge[mkey{"bee_salud_healthy", ""}] = float64(len(ps))
	return t
}

func (s *Sim) redistribution(n *node) *model.Redistribution {
	round := s.block / 38
	phase := []string{"commit", "reveal", "claim"}[(s.block%38)/13]
	healthy := !(n.name == staller && stall.in(s.tick%Loop))
	return &model.Redistribution{
		IsFullySynced: healthy, IsHealthy: healthy, HasSufficientFunds: true, Enabled: true, Phase: phase, Round: round,
		LastPlayedRound: round - 1, LastSelectedRound: round - (round+uint64(n.idx))%8, LastSampleDurationS: 1.2, Block: s.block,
	}
}

func (s *Sim) metrics(n *node) model.Metrics {
	m := model.Metrics{}
	add := func(k mkey, v float64) {
		var labels map[string]string
		if k.labels != "" {
			labels = map[string]string{}
			for _, kv := range strings.Split(k.labels, ",") {
				if a, b, ok := strings.Cut(kv, "="); ok {
					labels[a] = b
				}
			}
		}
		m[k.name] = append(m[k.name], model.MetricSample{Labels: labels, Value: v})
	}
	for k, v := range n.ctr {
		add(k, v)
	}
	for k, v := range n.gauge {
		add(k, v)
	}
	return m
}
