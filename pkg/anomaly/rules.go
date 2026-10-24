package anomaly

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"beescope/pkg/model"
)

// ChurnEvent is one peer connect/disconnect seen on a node.
type ChurnEvent struct {
	T         time.Time
	Peer      string
	PeerNode  string
	Connected bool
	Seq       uint64
}

// ErrGroup aggregates warning/error log lines with the same logger and message.
type ErrGroup struct {
	Level   string
	Logger  string
	Msg     string
	Count   int
	First   time.Time
	Last    time.Time
	LastSeq uint64
	Example string
}

// Input is everything the rules may read. It is built by the engine once
// per evaluation and must not be retained.
type Input struct {
	Now        time.Time
	Snap       *model.Snapshot
	Series     map[string]NodeSeries
	Churn      map[string][]ChurnEvent
	LogGroups  map[string][]ErrGroup // per node, last 2 minutes
	Ops        []model.Operation
	StaleAfter func(ref string) time.Duration
}

// Finding is one rule hit.
type Finding struct {
	Key        string
	Rule       string
	Title      string
	Class      model.IssueClass
	Severity   model.Severity
	Nodes      []string
	Components []string
	Since      time.Time
	What       string
	Where      string
	Evidence   []model.Evidence
	Hypothesis string
	NextSteps  []string
}

// Rule is one detector.
type Rule struct {
	ID   string
	Name string
	Eval func(in *Input) []Finding
}

// Rules is the ordered rule set.
var Rules = []Rule{
	{ID: "node-health", Name: "Node unreachable, gated or stale", Eval: nodeHealth},
	{ID: "peer-churn", Name: "Peer churn", Eval: peerChurn},
	{ID: "radius", Name: "Storage radius divergence or flapping", Eval: radius},
	{ID: "pullsync-stall", Name: "Historical pullsync stalled", Eval: pullsyncStall},
	{ID: "push-backlog", Name: "Push failures and shallow receipts", Eval: pushBacklog},
	{ID: "failure-spike", Name: "Retrieval / API failure spike", Eval: failureSpike},
	{ID: "error-logs", Name: "Error log burst", Eval: errorLogs},
	{ID: "outlier", Name: "Node behaves differently from its peers", Eval: outlier},
	{ID: "topology", Name: "Topology and redistribution health", Eval: topologyHealth},
	{ID: "stamps-tags", Name: "Unusable stamps and stuck uploads", Eval: stampsTags},
}

func sortedNodes(in *Input) []*model.NodeState {
	out := make([]*model.NodeState, 0, len(in.Snap.Nodes))
	for _, n := range in.Snap.Nodes {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func ev(text string, node string, src model.Source, prov model.Provenance, at time.Time) model.Evidence {
	return model.Evidence{Text: text, Node: node, Source: src, Prov: prov, At: at}
}

func apiSrc(ref string) model.Source { return model.Source{Adapter: "api", Ref: ref} }
func metricSrc(name string) model.Source {
	return model.Source{Adapter: "metrics", Ref: name}
}

// 1. Node unreachable, gated or stale.
func nodeHealth(in *Input) []Finding {
	var out []Finding
	for _, n := range sortedNodes(in) {
		switch n.Phase {
		case model.PhaseUnreachable:
			out = append(out, Finding{
				Key: "node-health:" + n.Name + ":unreachable", Rule: "node-health", Class: model.Confirmed, Severity: model.SevError,
				Title: fmt.Sprintf("%s API is unreachable", n.Name), Nodes: []string{n.Name}, Components: []string{"api"}, Since: n.PhaseSince,
				What:     "bee-scope cannot reach the node's HTTP API. Everything shown for this node is frozen at the last successful poll.",
				Where:    n.Name,
				Evidence: []model.Evidence{ev("last error: "+n.PhaseReason, n.Name, apiSrc("GET /health"), model.Observed, n.PhaseSince)},
				NextSteps: []string{
					fmt.Sprintf("kubectl -n local get pod %s -o wide   (is it running / restarting?)", podOr(n)),
					fmt.Sprintf("kubectl -n local logs %s --tail=100   (crash or panic at startup?)", podOr(n)),
					"Check whether peers still list this node in /topology (edges on the map).",
				},
			})
		case model.PhaseGated:
			out = append(out, Finding{
				Key: "node-health:" + n.Name + ":gated", Rule: "node-health", Class: model.Suspicious, Severity: model.SevWarn,
				Title: fmt.Sprintf("%s API answers 503", n.Name), Nodes: []string{n.Name}, Components: []string{"api"}, Since: n.PhaseSince,
				What:      "The API is up but refuses requests with 503. Bee does this while it is still starting or while routes are blocked during sync (checkRouteAvailability).",
				Where:     n.Name,
				Evidence:  []model.Evidence{ev(n.PhaseReason, n.Name, apiSrc("GET /status"), model.Observed, n.PhaseSince)},
				NextSteps: []string{"Watch the node's log for warmup / sync progress in the timeline.", "If it lasts longer than the warmup time, check chain connectivity (/chainstate) and postage sync."},
			})
		case model.PhaseGone:
			out = append(out, Finding{
				Key: "node-health:" + n.Name + ":gone", Rule: "node-health", Class: model.Confirmed, Severity: model.SevError,
				Title: fmt.Sprintf("%s disappeared from the cluster", n.Name), Nodes: []string{n.Name}, Since: n.PhaseSince,
				What:      "The node's Kubernetes service or pod is no longer discovered.",
				Where:     n.Name,
				Evidence:  []model.Evidence{ev("not returned by discovery", n.Name, model.Source{Adapter: "kube", Ref: "discovery"}, model.Observed, n.PhaseSince)},
				NextSteps: []string{"kubectl -n local get sts,pods   (scaled to 0? evicted?)"},
			})
		case model.PhaseUp:
			var stale []string
			var since time.Time
			for _, ref := range []string{"GET /status", "GET /topology", "GET /metrics"} {
				if s := n.Sources[ref]; s != nil && s.Stale {
					stale = append(stale, fmt.Sprintf("%s (last ok %s ago: %s)", ref, ago(in.Now, s.LastOK), s.LastErr))
					if since.IsZero() || s.LastOK.Before(since) {
						since = s.LastOK
					}
				}
			}
			if len(stale) > 0 {
				out = append(out, Finding{
					Key: "node-health:" + n.Name + ":stale", Rule: "node-health", Class: model.Suspicious, Severity: model.SevWarn,
					Title: fmt.Sprintf("Stale telemetry from %s", n.Name), Nodes: []string{n.Name}, Since: since,
					What:      "The node answers /health but some endpoints are failing or timing out, so parts of its picture are old.",
					Where:     n.Name,
					Evidence:  []model.Evidence{ev(strings.Join(stale, "; "), n.Name, apiSrc("poller"), model.Observed, in.Now)},
					NextSteps: []string{"A slow /topology or /metrics often means the node is overloaded: check its CPU (kubectl top pod).", "Open the node and look at the API component's latency."},
				})
			}
		}
	}
	return out
}

func podOr(n *model.NodeState) string {
	if n.Pod != "" {
		return n.Pod
	}
	return n.Name + "-0"
}

func ago(now, t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return now.Sub(t).Round(time.Second).String()
}

// 2. Peer churn.
func peerChurn(in *Input) []Finding {
	const window = 3 * time.Minute
	var out []Finding
	byPeer := map[string]map[string]bool{} // disconnected peer node -> nodes reporting
	for _, n := range sortedNodes(in) {
		var disc, conn int
		var first time.Time
		var evid []model.Evidence
		flap := map[string]int{}
		for _, c := range in.Churn[n.Name] {
			if c.T.Before(in.Now.Add(-window)) {
				continue
			}
			if first.IsZero() {
				first = c.T
			}
			label := shortAddr(c.Peer)
			if c.PeerNode != "" {
				label = c.PeerNode
			}
			if c.Connected {
				conn++
			} else {
				disc++
				flap[label]++
				if c.PeerNode != "" {
					if byPeer[c.PeerNode] == nil {
						byPeer[c.PeerNode] = map[string]bool{}
					}
					byPeer[c.PeerNode][n.Name] = true
				}
			}
			if len(evid) < 8 {
				verb := "disconnected"
				if c.Connected {
					verb = "connected"
				}
				evid = append(evid, model.Evidence{Text: verb + " " + label, EventSeq: c.Seq, Node: n.Name, At: c.T, Prov: model.Observed, Source: model.Source{Adapter: "event", Ref: "peer churn"}})
			}
		}
		if disc >= 3 && conn+disc >= 5 {
			var flappers []string
			for p, k := range flap {
				if k >= 2 {
					flappers = append(flappers, fmt.Sprintf("%s ×%d", p, k))
				}
			}
			sort.Strings(flappers)
			f := Finding{
				Key: "peer-churn:" + n.Name, Rule: "peer-churn", Class: model.Confirmed, Severity: model.SevWarn,
				Title: fmt.Sprintf("Peer churn on %s: %d disconnects, %d connects in %s", n.Name, disc, conn, window), Nodes: []string{n.Name},
				Components: []string{"kademlia", "libp2p"}, Since: first,
				What:  "The node keeps losing and re-establishing peer connections. Sync and push routes change each time a peer drops.",
				Where: n.Name, Evidence: evid,
				NextSteps: []string{"Filter the timeline to peer.* events for this node.", "Look for accounting disconnects (overdraw) or blocklisting in the node's log.", "Check whether the same peer flaps on several nodes (it would be the peer's problem)."},
			}
			if len(flappers) > 0 {
				f.Hypothesis = "Repeatedly dropped peers: " + strings.Join(flappers, ", ") + ". A peer that flaps on several nodes is usually restarting or overloaded."
			}
			out = append(out, f)
		}
	}
	for peer, reporters := range byPeer {
		if len(reporters) < 3 {
			continue
		}
		var names []string
		for r := range reporters {
			names = append(names, r)
		}
		sort.Strings(names)
		out = append(out, Finding{
			Key: "peer-churn:target:" + peer, Rule: "peer-churn", Class: model.Suspicious, Severity: model.SevWarn,
			Title: fmt.Sprintf("%s is being disconnected by %d nodes", peer, len(names)), Nodes: append([]string{peer}, names...),
			Components: []string{"kademlia"}, Since: in.Now.Add(-window),
			What:       "Several nodes dropped their connection to the same peer within a few minutes.",
			Where:      peer,
			Evidence:   []model.Evidence{{Text: "reported by " + strings.Join(names, ", "), Node: peer, At: in.Now, Prov: model.Derived, Source: model.Source{Adapter: "engine", Ref: "churn correlation"}}},
			Hypothesis: "The problem is likely on " + peer + " itself (restart, overload, network) rather than on the reporting nodes.",
			NextSteps:  []string{"Open " + peer + " and check its phase and recent errors.", "kubectl -n local describe pod for restarts."},
		})
	}
	return out
}

func shortAddr(a string) string {
	if len(a) > 8 {
		return a[:8] + "…"
	}
	return a
}

// 3. Storage radius divergence and flapping.
func radius(in *Input) []Finding {
	var out []Finding
	nr := in.Snap.NetworkRadius
	for _, n := range sortedNodes(in) {
		if n.Phase != model.PhaseUp || n.Status == nil || n.Mode == "light" || n.Group == "bootnode" {
			continue
		}
		if nr != nil && n.Status.StorageRadius != *nr && !n.Status.IsWarmingUp {
			out = append(out, Finding{
				Key: "radius:" + n.Name + ":diverge", Rule: "radius", Class: model.Suspicious, Severity: model.SevWarn,
				Title: fmt.Sprintf("%s storage radius %d differs from the cluster's %d", n.Name, n.Status.StorageRadius, *nr),
				Nodes: []string{n.Name}, Components: []string{"reserve", "puller"}, Since: seriesSince(in.Series[n.Name][SRadius], float64(n.Status.StorageRadius)),
				What:  "This node believes it is responsible for a different address range than most nodes. It will sync and store a different set of chunks than its neighbors.",
				Where: n.Name,
				Evidence: []model.Evidence{
					ev(fmt.Sprintf("storageRadius=%d, committedDepth=%d, reserveSizeWithinRadius=%d", n.Status.StorageRadius, n.Status.CommittedDepth, n.Status.ReserveSizeWithinRadius), n.Name, apiSrc("GET /status"), model.Observed, lastOK(n, "GET /status")),
					ev(fmt.Sprintf("modal radius of up full nodes = %d", *nr), "", model.Source{Adapter: "engine", Ref: "modal radius"}, model.Derived, in.Now),
				},
				Hypothesis: "A higher radius usually means the reserve filled up (radius increases to shed data); a lower one means the reserve shrank (expired batches) or the node is behind on batch events.",
				NextSteps:  []string{"Compare reserveSizeWithinRadius with its neighbors (Compare view).", "Check /chainstate block vs chain tip: a node behind on postage events computes a different reserve.", "Look for puller.radius_decrease events."},
			})
		}
		if c, first := in.Series[n.Name][SRadius].Changes(in.Now, 15*time.Minute); c >= 2 {
			out = append(out, Finding{
				Key: "radius:" + n.Name + ":flap", Rule: "radius", Class: model.Suspicious, Severity: model.SevWarn,
				Title: fmt.Sprintf("%s storage radius changed %d times in 15 min", n.Name, c), Nodes: []string{n.Name}, Components: []string{"reserve", "puller"}, Since: first,
				What:       "The radius moved back and forth. Every change restarts sync workers for the affected bins and may evict or re-sync data.",
				Where:      n.Name,
				Evidence:   []model.Evidence{ev(seriesText(in.Series[n.Name][SRadius], in.Now, 15*time.Minute), n.Name, apiSrc("GET /status"), model.Derived, in.Now)},
				Hypothesis: "Oscillation suggests reserve size hovering around the capacity threshold, or a radius calculation fed by inconsistent batch state.",
				NextSteps:  []string{"Jump to the timeline around the first change.", "Check reserve size vs capacity on the Reserve component."},
			})
		}
	}
	return out
}

func seriesSince(s *Series, v float64) time.Time {
	pts := s.Points()
	t := time.Time{}
	for i := len(pts) - 1; i >= 0; i-- {
		if pts[i].V != v {
			break
		}
		t = pts[i].T
	}
	return t
}

func seriesText(s *Series, now time.Time, w time.Duration) string {
	pts := s.Since(now.Add(-w))
	var parts []string
	for i, p := range pts {
		if i == 0 || p.V != pts[i-1].V {
			parts = append(parts, fmt.Sprintf("%s→%g", p.T.Format("15:04:05"), p.V))
		}
	}
	return strings.Join(parts, ", ")
}

// 4. Historical pullsync stalled.
func pullsyncStall(in *Input) []Finding {
	var out []Finding
	for _, n := range sortedNodes(in) {
		if n.Phase != model.PhaseUp || n.Status == nil {
			continue
		}
		ns := in.Series[n.Name]
		rate, ok := ns[SPullsyncRate].Last()
		if !ok || rate.V <= 0 {
			continue
		}
		d, span, ok := ns[SSyncedHist].Delta(in.Now, 3*time.Minute)
		if !ok || span < 2*time.Minute || d > 0 {
			continue
		}
		workers, _ := ns[SPullerWorkers].Last()
		out = append(out, Finding{
			Key: "pullsync-stall:" + n.Name, Rule: "pullsync-stall", Class: model.Suspicious, Severity: model.SevWarn,
			Title: fmt.Sprintf("%s historical sync is not progressing", n.Name), Nodes: []string{n.Name}, Components: []string{"puller", "pullsync", "reserve"},
			Since: in.Now.Add(-span),
			What:  "The node reports it is still catching up (pullsyncRate > 0), but no historical chunk was synced in the last 3 minutes.",
			Where: n.Name,
			Evidence: []model.Evidence{
				ev(fmt.Sprintf("pullsyncRate=%.2f chunks/s (10 min window, 0 means synced)", rate.V), n.Name, apiSrc("GET /status"), model.Observed, rate.T),
				ev(fmt.Sprintf("bee_puller_synced_chunks{type=historical} +0 over %s", span.Round(time.Second)), n.Name, metricSrc("bee_puller_synced_chunks"), model.Derived, in.Now),
				ev(fmt.Sprintf("bee_puller_worker=%g active workers", workers.V), n.Name, metricSrc("bee_puller_worker"), model.Observed, workers.T),
			},
			Hypothesis: "Workers that exist but deliver nothing usually wait on a lock (reserve write contention) or on peers that stopped answering; zero workers means sync was never (re)started for the current radius.",
			NextSteps:  []string{"Look for puller.interval_failed / sync_failed events for this node.", "Check pullsync stream_limit_exceeded on neighbors.", "Take a goroutine dump: /debug/pprof/goroutine?debug=2 on the node."},
		})
	}
	return out
}

// 5. Push failures and shallow receipts.
func pushBacklog(in *Input) []Finding {
	const w = 2 * time.Minute
	var out []Finding
	for _, n := range sortedNodes(in) {
		if n.Phase != model.PhaseUp {
			continue
		}
		ns := in.Series[n.Name]
		att, span, ok1 := ns[SPushAttempts].Delta(in.Now, w)
		fail, _, ok2 := ns[SPushFailed].Delta(in.Now, w)
		if ok1 && ok2 && att >= 10 && fail/att >= 0.3 {
			out = append(out, Finding{
				Key: "push-backlog:" + n.Name + ":failed", Rule: "push-backlog", Class: model.Suspicious, Severity: model.SevWarn,
				Title: fmt.Sprintf("%s: %.0f%% of push attempts fail", n.Name, 100*fail/att), Nodes: []string{n.Name}, Components: []string{"pushsync", "pusher"},
				Since:     in.Now.Add(-span),
				What:      "Many pushsync send attempts to peers fail, so chunks are retried against other peers and uploads slow down.",
				Where:     n.Name,
				Evidence:  []model.Evidence{ev(fmt.Sprintf("%.0f failed of %.0f attempts in %s", fail, att, span.Round(time.Second)), n.Name, metricSrc("bee_pushsync_total_failed_send_attempts"), model.Derived, in.Now)},
				NextSteps: []string{"Filter the timeline to pushsync.push_failed for this node to see which peers refuse.", "Check accounting: overdraft refreshes stall pushes."},
			})
		}
		sh, sspan, ok := ns[SShallow].Delta(in.Now, w)
		if ok && sh >= 3 {
			out = append(out, Finding{
				Key: "push-backlog:" + n.Name + ":shallow", Rule: "push-backlog", Class: model.Suspicious, Severity: model.SevWarn,
				Title: fmt.Sprintf("%s received %.0f shallow receipts", n.Name, sh), Nodes: []string{n.Name}, Components: []string{"pushsync"},
				Since:      in.Now.Add(-sspan),
				What:       "Storer peers returned receipts from outside the chunk's neighborhood (proximity below their storage radius).",
				Where:      n.Name,
				Evidence:   []model.Evidence{ev(fmt.Sprintf("+%.0f bee_pushsync_shallow_receipt in %s", sh, sspan.Round(time.Second)), n.Name, metricSrc("bee_pushsync_shallow_receipt"), model.Derived, in.Now)},
				Hypothesis: "Shallow receipts point at a radius disagreement between this node and the storer, or at a neighborhood with too few reachable peers.",
				NextSteps:  []string{"Open pushsync.shallow_receipt events: they carry peer_radius vs self_radius.", "Check the radius rule for divergence."},
			})
		}
		tp, tspan, ok1 := ns[SToPush].Delta(in.Now, w)
		syn, _, ok2 := ns[SPushSynced].Delta(in.Now, w)
		perr, _, _ := ns[SPushErrors].Delta(in.Now, w)
		if ok1 && ok2 && tp >= 20 && syn == 0 {
			out = append(out, Finding{
				Key: "push-backlog:" + n.Name + ":stuck", Rule: "push-backlog", Class: model.Suspicious, Severity: model.SevError,
				Title: fmt.Sprintf("%s pushes chunks but none complete", n.Name), Nodes: []string{n.Name}, Components: []string{"pusher", "pushsync"},
				Since:     in.Now.Add(-tspan),
				What:      "The pusher handed chunks to pushsync but no chunk was reported synced in the window.",
				Where:     n.Name,
				Evidence:  []model.Evidence{ev(fmt.Sprintf("to_push +%.0f, synced +0, errors +%.0f in %s", tp, perr, tspan.Round(time.Second)), n.Name, metricSrc("bee_pusher_total_to_push"), model.Derived, in.Now)},
				NextSteps: []string{"Check whether the stamp batch is usable (Stamps).", "Look for pushsync.push_failed and no_peers_left events."},
			})
		}
	}
	return out
}

// 6. Retrieval and API failure spikes.
func failureSpike(in *Input) []Finding {
	const w = 2 * time.Minute
	var out []Finding
	for _, n := range sortedNodes(in) {
		ns := in.Series[n.Name]
		req, span, ok1 := ns[SRetrReq].Delta(in.Now, w)
		fail, _, ok2 := ns[SRetrFail].Delta(in.Now, w)
		if ok1 && ok2 && req >= 5 && fail/req >= 0.2 {
			out = append(out, Finding{
				Key: "failure-spike:" + n.Name + ":retrieval", Rule: "failure-spike", Class: model.Confirmed, Severity: model.SevWarn,
				Title: fmt.Sprintf("%s: %.0f%% of retrieval requests fail", n.Name, 100*fail/req), Nodes: []string{n.Name}, Components: []string{"retrieval"},
				Since:      in.Now.Add(-span),
				What:       "Requests for chunks to peers fail at a high rate.",
				Where:      n.Name,
				Evidence:   []model.Evidence{ev(fmt.Sprintf("%.0f failures of %.0f requests in %s", fail, req, span.Round(time.Second)), n.Name, metricSrc("bee_retrieval_request_failure_count"), model.Derived, in.Now)},
				Hypothesis: "If only some chunks fail they may not exist in the network (never synced); if all fail, the node's peers in the chunk's neighborhood are unreachable.",
				NextSteps:  []string{"Filter timeline to retrieval.* for this node and look at the chunk addresses.", "Check the upload that produced those chunks in Operations."},
			})
		}
		e5, span5, ok := ns[SAPI5xx].Delta(in.Now, w)
		if ok && e5 >= 3 {
			r, _, _ := ns[SAPIReq].Delta(in.Now, w)
			out = append(out, Finding{
				Key: "failure-spike:" + n.Name + ":api5xx", Rule: "failure-spike", Class: model.Confirmed, Severity: model.SevError,
				Title: fmt.Sprintf("%s returned %.0f HTTP 5xx responses", n.Name, e5), Nodes: []string{n.Name}, Components: []string{"api"},
				Since:     in.Now.Add(-span5),
				What:      "The node's API answered requests with server errors.",
				Where:     n.Name,
				Evidence:  []model.Evidence{ev(fmt.Sprintf("+%.0f 5xx of +%.0f requests in %s", e5, r, span5.Round(time.Second)), n.Name, metricSrc("bee_api_response_code_count"), model.Derived, in.Now)},
				NextSteps: []string{"bee_api_response_code_count has no route label: look at api log errors in the timeline to find the endpoint.", "Uploads failing with 5xx often mean an unusable batch or a full upload store."},
			})
		}
	}
	return out
}

// 7. Error log bursts.
func errorLogs(in *Input) []Finding {
	var out []Finding
	for _, n := range sortedNodes(in) {
		for _, g := range in.LogGroups[n.Name] {
			isErr := g.Level == "error"
			if (isErr && g.Count < 5) || (!isErr && g.Count < 20) {
				continue
			}
			class, sev := model.Confirmed, model.SevError
			if !isErr {
				class, sev = model.Suspicious, model.SevWarn
			}
			out = append(out, Finding{
				Key: "error-logs:" + n.Name + ":" + g.Logger + ":" + g.Msg, Rule: "error-logs", Class: class, Severity: sev,
				Title: fmt.Sprintf("%s logged %q %d times", n.Name, g.Msg, g.Count), Nodes: []string{n.Name}, Components: compOfLogger(g.Logger),
				Since:     g.First,
				What:      fmt.Sprintf("Repeated %s-level log from %s within two minutes.", g.Level, g.Logger),
				Where:     n.Name + " / " + g.Logger,
				Evidence:  []model.Evidence{{Text: g.Example, EventSeq: g.LastSeq, Node: n.Name, At: g.Last, Prov: model.Observed, Source: model.Source{Adapter: "log", Ref: g.Logger}}},
				NextSteps: []string{"Open the newest occurrence for its full fields.", "Search the log for the same message on other nodes: is it local or cluster-wide?"},
			})
		}
	}
	return out
}

var loggerComp func(string) string

// SetLoggerMapper wires the logger→component mapping (avoids an import cycle).
func SetLoggerMapper(f func(string) string) { loggerComp = f }

func compOfLogger(l string) []string {
	if loggerComp == nil {
		return nil
	}
	if c := loggerComp(l); c != "" {
		return []string{c}
	}
	return nil
}

// 8. Outliers among peers (hypothesis only).
func outlier(in *Input) []Finding {
	type sample struct {
		node string
		v    float64
	}
	check := func(name, label, comp string, vals []sample, lowIsBad bool) []Finding {
		if len(vals) < 4 {
			return nil
		}
		var mean, sd float64
		for _, s := range vals {
			mean += s.v
		}
		mean /= float64(len(vals))
		for _, s := range vals {
			sd += (s.v - mean) * (s.v - mean)
		}
		sd = math.Sqrt(sd / float64(len(vals)))
		if sd == 0 {
			return nil
		}
		var out []Finding
		for _, s := range vals {
			z := (s.v - mean) / sd
			if (lowIsBad && z < -2.2) || (!lowIsBad && z > 2.2) {
				out = append(out, Finding{
					Key: "outlier:" + s.node + ":" + name, Rule: "outlier", Class: model.Hypothesis, Severity: model.SevInfo,
					Title: fmt.Sprintf("%s: %s is unusual (%.4g vs cluster mean %.4g)", s.node, label, s.v, mean),
					Nodes: []string{s.node}, Components: []string{comp}, Since: in.Now,
					What:      "This node's value is far from what its peers report (z-score " + fmt.Sprintf("%.1f", z) + "). It may be legitimate (different neighborhood) or an early sign of a problem.",
					Where:     s.node,
					Evidence:  []model.Evidence{ev(fmt.Sprintf("%s=%.4g, mean=%.4g, stddev=%.4g over %d nodes", name, s.v, mean, sd, len(vals)), s.node, model.Source{Adapter: "engine", Ref: name}, model.Derived, in.Now)},
					NextSteps: []string{"Use Compare to put this node next to a healthy one."},
				})
			}
		}
		return out
	}
	var peers, errs []sample
	for _, n := range sortedNodes(in) {
		if n.Phase != model.PhaseUp || n.Group == "bootnode" {
			continue
		}
		peers = append(peers, sample{n.Name, float64(n.Connected)})
		if d, span, ok := in.Series[n.Name][SLogErrors].Delta(in.Now, 5*time.Minute); ok && span > 0 {
			errs = append(errs, sample{n.Name, d})
		}
	}
	out := check("connected_peers", "connected peers", "kademlia", peers, true)
	out = append(out, check("log_errors_5m", "error log lines (5 min)", "api", errs, false)...)
	return out
}

// 9. Topology and redistribution health.
func topologyHealth(in *Input) []Finding {
	var out []Finding
	for _, n := range sortedNodes(in) {
		if n.Phase != model.PhaseUp {
			continue
		}
		topoAt := time.Time{}
		if s := n.Sources["GET /topology"]; s != nil {
			topoAt = s.LastOK
		}
		if !topoAt.IsZero() && n.Connected == 0 {
			out = append(out, Finding{
				Key: "topology:" + n.Name + ":isolated", Rule: "topology", Class: model.Confirmed, Severity: model.SevError,
				Title: fmt.Sprintf("%s has no connected peers", n.Name), Nodes: []string{n.Name}, Components: []string{"kademlia", "libp2p"}, Since: topoAt,
				What:      "The node is isolated: it cannot sync, push or retrieve anything.",
				Where:     n.Name,
				Evidence:  []model.Evidence{ev("connected=0, population="+fmt.Sprint(n.Population), n.Name, apiSrc("GET /topology"), model.Observed, topoAt)},
				NextSteps: []string{"Check bootnode reachability and the node's underlay addresses (/addresses).", "Look for libp2p handshake errors in the log."},
			})
		}
		if n.Unhealthy > 0 && n.Connected > 0 {
			out = append(out, Finding{
				Key: "topology:" + n.Name + ":unhealthy", Rule: "topology", Class: model.Suspicious, Severity: model.SevInfo,
				Title: fmt.Sprintf("%s marks %d of %d peers unhealthy", n.Name, n.Unhealthy, n.Connected), Nodes: []string{n.Name}, Components: []string{"salud", "kademlia"}, Since: topoAt,
				What:      "Salud flagged peers as unhealthy (their reported radius or reserve disagrees with the network). Unhealthy peers are skipped for some operations.",
				Where:     n.Name,
				Evidence:  []model.Evidence{ev("peers with healthy=false in /topology", n.Name, apiSrc("GET /topology"), model.Observed, topoAt)},
				NextSteps: []string{"Open the node's Topology tab: unhealthy peers are listed with their bin.", "Compare the unhealthy peers' radius with the network radius."},
			})
		}
		if n.Status != nil && n.Mode != "light" && n.Group != "bootnode" && n.Status.NeighborhoodSize == 0 && !n.Status.IsWarmingUp {
			out = append(out, Finding{
				Key: "topology:" + n.Name + ":noneighbors", Rule: "topology", Class: model.Suspicious, Severity: model.SevWarn,
				Title: fmt.Sprintf("%s has no neighbors within its storage radius", n.Name), Nodes: []string{n.Name}, Components: []string{"kademlia", "puller"},
				Since:      lastOK(n, "GET /status"),
				What:       "No peer shares the node's neighborhood, so nothing can be pull-synced and the reserve has no replicas.",
				Where:      n.Name,
				Evidence:   []model.Evidence{ev(fmt.Sprintf("neighborhoodSize=0, storageRadius=%d", n.Status.StorageRadius), n.Name, apiSrc("GET /status"), model.Observed, lastOK(n, "GET /status"))},
				Hypothesis: "Normal in very small clusters when the radius is too high for the node count.",
				NextSteps:  []string{"Compare the radius with the number of nodes: 2^radius neighborhoods for N nodes."},
			})
		}
		if n.Reachability != "" && !strings.EqualFold(n.Reachability, "public") && !strings.EqualFold(n.Reachability, "unknown") {
			out = append(out, Finding{
				Key: "topology:" + n.Name + ":reach", Rule: "topology", Class: model.Suspicious, Severity: model.SevInfo,
				Title: fmt.Sprintf("%s reachability is %s", n.Name, n.Reachability), Nodes: []string{n.Name}, Components: []string{"libp2p"}, Since: topoAt,
				What:      "Peers may not be able to dial this node.",
				Where:     n.Name,
				Evidence:  []model.Evidence{ev("reachability="+n.Reachability, n.Name, apiSrc("GET /topology"), model.Observed, topoAt)},
				NextSteps: []string{"Check NAT / announced addresses (/addresses)."},
			})
		}
		if r := n.Redistribution; r != nil {
			at := lastOK(n, "GET /redistributionstate")
			if r.IsFrozen {
				out = append(out, Finding{
					Key: "topology:" + n.Name + ":frozen", Rule: "topology", Class: model.Confirmed, Severity: model.SevError,
					Title: fmt.Sprintf("%s is frozen in the redistribution game", n.Name), Nodes: []string{n.Name}, Components: []string{"storageincentives", "reserve"}, Since: at,
					What:       "The node committed a reserve sample that disagreed with the truth and is excluded from play for a number of rounds.",
					Where:      n.Name,
					Evidence:   []model.Evidence{ev(fmt.Sprintf("isFrozen=true, lastFrozenRound=%d, round=%d", r.LastFrozenRound, r.Round), n.Name, apiSrc("GET /redistributionstate"), model.Observed, at)},
					Hypothesis: "Freezing follows a reserve that differs from the neighborhood: check the radius rule and sync completeness for this node.",
					NextSteps:  []string{"Compare reserveSizeWithinRadius with the node's neighbors.", "Check whether historical pullsync was finished before the round."},
				})
			} else if !r.IsHealthy && r.Enabled {
				out = append(out, Finding{
					Key: "topology:" + n.Name + ":salud", Rule: "topology", Class: model.Suspicious, Severity: model.SevWarn,
					Title: fmt.Sprintf("%s reports itself unhealthy for redistribution", n.Name), Nodes: []string{n.Name}, Components: []string{"storageincentives", "salud"}, Since: at,
					What:      "The node will sit out rounds while unhealthy.",
					Where:     n.Name,
					Evidence:  []model.Evidence{ev(fmt.Sprintf("isHealthy=false, isFullySynced=%t, hasSufficientFunds=%t", r.IsFullySynced, r.HasSufficientFunds), n.Name, apiSrc("GET /redistributionstate"), model.Observed, at)},
					NextSteps: []string{"isFullySynced=false → historical sync not done; hasSufficientFunds=false → fund the node."},
				})
			}
		}
	}
	return out
}

// 10. Unusable stamps and stuck uploads.
func stampsTags(in *Input) []Finding {
	var out []Finding
	for _, n := range sortedNodes(in) {
		at := time.Time{}
		if s := n.Sources["GET /stamps"]; s != nil {
			at = s.LastOK
		}
		for _, s := range n.Stamps {
			if s.Exists && !s.Usable {
				out = append(out, Finding{
					Key: "stamps-tags:" + n.Name + ":" + s.BatchID, Rule: "stamps-tags", Class: model.Confirmed, Severity: model.SevWarn,
					Title: fmt.Sprintf("Batch %s on %s is not usable", shortAddr(s.BatchID), n.Name), Nodes: []string{n.Name}, Components: []string{"postage"}, Since: at,
					What:       "Uploads stamped with this batch will be rejected.",
					Where:      n.Name,
					Evidence:   []model.Evidence{ev(fmt.Sprintf("usable=false, ttl=%ds, utilization=%d", s.BatchTTL, s.Utilization), n.Name, apiSrc("GET /stamps"), model.Observed, at)},
					Hypothesis: "A new batch is unusable until enough blocks confirm it; an old one becomes unusable when expired or full.",
					NextSteps:  []string{"Check /chainstate block vs chain tip (is the node synced with batch events?)."},
				})
			} else if s.Usable && s.UtilizationRatio >= 0.9 {
				out = append(out, Finding{
					Key: "stamps-tags:" + n.Name + ":" + s.BatchID + ":full", Rule: "stamps-tags", Class: model.Suspicious, Severity: model.SevInfo,
					Title: fmt.Sprintf("Batch %s on %s is %.0f%% full", shortAddr(s.BatchID), n.Name, 100*s.UtilizationRatio), Nodes: []string{n.Name}, Components: []string{"postage"}, Since: at,
					What:      "Immutable batches stop accepting chunks when a bucket fills; mutable ones overwrite old chunks.",
					Where:     n.Name,
					Evidence:  []model.Evidence{ev(fmt.Sprintf("utilizationRatio=%.2f depth=%d bucketDepth=%d", s.UtilizationRatio, s.Depth, s.BucketDepth), n.Name, apiSrc("GET /stamps"), model.Observed, at)},
					NextSteps: []string{"Dilute or top up the batch before uploads start failing."},
				})
			}
		}
	}
	for _, op := range in.Ops {
		if op.Kind != "upload-tag" || op.State != "stalled" {
			continue
		}
		out = append(out, Finding{
			Key: "stamps-tags:" + op.ID, Rule: "stamps-tags", Class: model.Suspicious, Severity: model.SevWarn,
			Title: op.Title + " is not making progress", Nodes: []string{op.Node}, Components: []string{"pusher", "pushsync"}, Since: op.Updated,
			What:       "An upload's chunks were split and stored locally, but the synced counter stopped moving before reaching the total.",
			Where:      op.Node,
			Evidence:   []model.Evidence{ev(op.Note, op.Node, apiSrc("GET /tags"), model.Observed, op.Updated)},
			Hypothesis: "Chunks whose batch expired are dropped without updating the tag (ChunkCouldNotSync), so synced can stay below split forever; otherwise pushes keep failing.",
			NextSteps:  []string{"Open the operation to see which stage stopped.", "Check push failures on the uploading node."},
		})
	}
	return out
}

func lastOK(n *model.NodeState, ref string) time.Time {
	if s := n.Sources[ref]; s != nil {
		return s.LastOK
	}
	return time.Time{}
}
