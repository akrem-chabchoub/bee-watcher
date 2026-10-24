package engine

import (
	"fmt"
	"time"

	"beescope/pkg/anomaly"
	"beescope/pkg/component"
	"beescope/pkg/model"
)

// rateWindow is the number of 1 s buckets averaged for log and span rates.
const rateWindow = 5

type metricPart struct {
	at                time.Time
	have              bool
	hasActivityMetric bool
	activity          float64
	errRate           float64
	queue             *float64
	signals           []model.Signal
}

type nodeRT struct {
	st          *model.NodeState
	topo        *model.Topology
	peers       map[string]model.KadPeer
	metrics     model.Metrics
	metricsAt   time.Time
	metricParts map[string]*metricPart
	flowRates   map[string]float64
	series      anomaly.NodeSeries
	raw         map[string]uint64

	logCount    map[string]int // current second, by component
	logErrCount map[string]int
	spanCount   map[string]int
	logHist     []map[string]int // last rateWindow seconds
	logErrHist  []map[string]int
	spanHist    []map[string]int
	loggerSeen  map[string]time.Time
	spanSeen    map[string]time.Time
	kadLogAt    time.Time
	stampsSeen  bool
}

func (e *Engine) node(name string) *nodeRT {
	n, ok := e.nodes[name]
	if ok {
		return n
	}
	n = &nodeRT{
		st: &model.NodeState{
			Name: name, Phase: model.PhaseUnknown, PhaseSince: e.now(), LogStream: "off",
			Components: map[string]*model.ComponentState{}, Sources: map[string]*model.SourceHealth{},
		},
		metricParts: map[string]*metricPart{},
		flowRates:   map[string]float64{},
		series:      anomaly.NodeSeries{},
		raw:         map[string]uint64{},
		logCount:    map[string]int{},
		logErrCount: map[string]int{},
		spanCount:   map[string]int{},
		loggerSeen:  map[string]time.Time{},
		spanSeen:    map[string]time.Time{},
	}
	for _, s := range component.Specs {
		n.st.Components[s.ID] = &model.ComponentState{ID: s.ID, Status: model.CompUnknown}
	}
	e.nodes[name] = n
	return n
}

func (n *nodeRT) src(ref string, iv time.Duration) *model.SourceHealth {
	s, ok := n.st.Sources[ref]
	if !ok {
		s = &model.SourceHealth{Ref: ref, Interval: iv.Seconds()}
		n.st.Sources[ref] = s
	}
	return s
}

// kadLogs reports whether peer connect/disconnect lines were seen recently,
// in which case the log (exact) is preferred over /topology diffs (sampled).
func (n *nodeRT) kadLogs(now time.Time) bool {
	return !n.kadLogAt.IsZero() && now.Sub(n.kadLogAt) < 10*time.Minute
}

func pushHist(h []map[string]int, cur map[string]int) []map[string]int {
	h = append(h, cur)
	if len(h) > rateWindow {
		h = h[len(h)-rateWindow:]
	}
	return h
}

func avg(h []map[string]int, key string) float64 {
	if len(h) == 0 {
		return 0
	}
	s := 0
	for _, m := range h {
		s += m[key]
	}
	return float64(s) / float64(len(h))
}

// refreshNode updates freshness, rates and component states.
func (e *Engine) refreshNode(n *nodeRT, now time.Time) {
	n.logHist = pushHist(n.logHist, n.logCount)
	n.logErrHist = pushHist(n.logErrHist, n.logErrCount)
	n.spanHist = pushHist(n.spanHist, n.spanCount)
	n.logCount, n.logErrCount, n.spanCount = map[string]int{}, map[string]int{}, map[string]int{}

	for _, s := range n.st.Sources {
		iv := time.Duration(s.Interval * float64(time.Second))
		s.Stale = !s.LastTry.IsZero() && (s.LastOK.IsZero() || now.Sub(s.LastOK) > iv*5/2)
	}
	if h := n.st.Sources["GET /health"]; h != nil && n.st.Phase == model.PhaseUp && h.Stale && now.Sub(h.LastOK) > 3*time.Duration(h.Interval*float64(time.Second)) {
		e.setPhase(n, model.PhaseUnreachable, fmt.Sprintf("no successful /health for %s", now.Sub(h.LastOK).Round(time.Second)), now, model.Source{Adapter: "engine", Ref: "staleness"}, model.Derived)
	}
	n.st.FlowRates = n.flowRates
	metricsSrc := n.st.Sources["GET /metrics"]
	metricsStale := metricsSrc != nil && metricsSrc.Stale

	for _, spec := range component.Specs {
		cs := n.st.Components[spec.ID]
		mp := n.metricParts[spec.ID]
		logRate := avg(n.logHist, spec.ID)
		logErrRate := avg(n.logErrHist, spec.ID)
		spanRate := avg(n.spanHist, spec.ID)
		cs.LogRate = logRate
		cs.Observable = cs.Observable[:0]
		cs.Signals = nil
		cs.Queue = nil
		cs.Prov = model.Derived
		cs.Stale = false
		fresh := time.Time{}
		if mp != nil && mp.have {
			cs.Observable = append(cs.Observable, "metrics")
			cs.Signals = mp.signals
			cs.Queue = mp.queue
			fresh = mp.at
			cs.Stale = metricsStale
		}
		if t, ok := n.loggerSeen[spec.ID]; ok && now.Sub(t) < 10*time.Minute && n.st.LogStream == "following" {
			cs.Observable = append(cs.Observable, "logs")
			if t.After(fresh) {
				fresh = t
			}
		}
		if t, ok := n.spanSeen[spec.ID]; ok && now.Sub(t) < 10*time.Minute {
			cs.Observable = append(cs.Observable, "spans")
		}
		if apiFresh := e.componentAPI(n, spec.ID); !apiFresh.IsZero() {
			cs.Observable = append(cs.Observable, "api")
			if apiFresh.After(fresh) {
				fresh = apiFresh
			}
		}
		cs.FreshAt = fresh

		cs.Activity, cs.ErrorRate = 0, logErrRate
		switch {
		case mp != nil && mp.hasActivityMetric:
			cs.Activity = mp.activity
		case logRate > 0:
			cs.Activity, cs.Prov = logRate, model.Observed
		case spanRate > 0:
			cs.Activity, cs.Prov = spanRate, model.Observed
		}
		if mp != nil {
			cs.ErrorRate += mp.errRate
		}
		cs.Status, cs.Reason = e.componentStatus(n, spec, cs)
		cs.Prov = e.prov(cs.Prov)
	}
}

// componentAPI returns when an API endpoint last revealed this component's
// state (zero if none).
func (e *Engine) componentAPI(n *nodeRT, id string) time.Time {
	ref := map[string]string{
		"api": "GET /health", "reserve": "GET /status", "puller": "GET /status", "kademlia": "GET /topology",
		"postage": "GET /stamps", "storageincentives": "GET /redistributionstate", "chain": "GET /chainstate", "pusher": "GET /tags",
	}[id]
	if ref == "" {
		return time.Time{}
	}
	if s := n.st.Sources[ref]; s != nil && !s.LastOK.IsZero() {
		return s.LastOK
	}
	return time.Time{}
}

func (e *Engine) componentStatus(n *nodeRT, spec component.Spec, cs *model.ComponentState) (model.ComponentStatus, string) {
	switch n.st.Phase {
	case model.PhaseUp:
	case model.PhaseGated:
		if spec.ID == "api" {
			return model.CompDegraded, "API answers 503"
		}
		return model.CompUnknown, "node API gated (503): no fresh data"
	default:
		return model.CompUnknown, "node is " + string(n.st.Phase)
	}
	if len(cs.Observable) == 0 {
		reason := "no metric, log or span source currently feeds this component"
		if len(spec.Blind) > 0 {
			reason = spec.Blind[0]
		}
		return model.CompUnobservable, reason
	}
	// Component-specific facts take precedence over generic rates.
	switch spec.ID {
	case "kademlia":
		if n.topo != nil && n.st.Connected == 0 {
			return model.CompFailed, "no connected peers"
		}
	case "storageincentives":
		if r := n.st.Redistribution; r != nil {
			if r.IsFrozen {
				return model.CompFailed, fmt.Sprintf("frozen since round %d", r.LastFrozenRound)
			}
			if r.Enabled && !r.IsHealthy {
				return model.CompDegraded, "reports unhealthy: will not play"
			}
		}
	case "postage":
		for _, s := range n.st.Stamps {
			if s.Exists && !s.Usable {
				return model.CompDegraded, "batch " + short(s.BatchID) + " not usable"
			}
		}
	case "chain":
		if s := n.st.Sources["GET /chainstate"]; s != nil && s.LastErr != "" {
			return model.CompDegraded, "chainstate failing: " + s.LastErr
		}
	case "api":
		if s := n.st.Sources["GET /health"]; s != nil && s.LatencyMS > 2000 {
			return model.CompDegraded, fmt.Sprintf("slow: /health took %.0f ms", s.LatencyMS)
		}
	}
	switch {
	case cs.ErrorRate > 0 && cs.Activity == 0:
		return model.CompFailed, fmt.Sprintf("%.2f errors/s and no successful activity", cs.ErrorRate)
	case cs.ErrorRate > 0:
		return model.CompDegraded, fmt.Sprintf("%.2f errors/s alongside %.2f ops/s", cs.ErrorRate, cs.Activity)
	}
	if spec.ID == "puller" && n.st.Status != nil && n.st.Status.PullsyncRate > 0 {
		return model.CompActive, fmt.Sprintf("historical sync catching up (%.2f chunks/s)", n.st.Status.PullsyncRate)
	}
	if cs.Queue != nil && *cs.Queue > 0 {
		return model.CompActive, fmt.Sprintf("%.0f in flight", *cs.Queue)
	}
	if cs.Activity > 0.01 {
		return model.CompActive, fmt.Sprintf("%.2f ops/s", cs.Activity)
	}
	return model.CompIdle, "no activity in the last interval"
}
