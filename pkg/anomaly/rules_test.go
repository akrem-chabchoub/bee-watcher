package anomaly

import (
	"testing"
	"time"

	"beescope/pkg/model"
)

var t0 = time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)

func upNode(name string, connected int, radius uint8) *model.NodeState {
	return &model.NodeState{
		Name: name, Group: "bee", Phase: model.PhaseUp, Connected: connected, Mode: "full", Reachability: "Public",
		Status:  &model.Status{StorageRadius: radius, NeighborhoodSize: 3},
		Sources: map[string]*model.SourceHealth{"GET /topology": {LastOK: t0}, "GET /status": {LastOK: t0}},
	}
}

// counter builds a series rising by step every 5 s over d, ending at now.
func counter(now time.Time, d time.Duration, step float64) *Series {
	s := NewSeries(720)
	v := 0.0
	for t := now.Add(-d); !t.After(now); t = t.Add(5 * time.Second) {
		s.Add(t, v)
		v += step
	}
	return s
}

func cluster(nodes ...*model.NodeState) *Input {
	in := &Input{Now: t0, Snap: &model.Snapshot{Nodes: map[string]*model.NodeState{}}, Series: map[string]NodeSeries{}, Churn: map[string][]ChurnEvent{}, LogGroups: map[string][]ErrGroup{}}
	for _, n := range nodes {
		in.Snap.Nodes[n.Name] = n
		in.Series[n.Name] = NodeSeries{}
	}
	return in
}

func keys(fs []Finding) map[string]bool {
	m := map[string]bool{}
	for _, f := range fs {
		m[f.Key] = true
	}
	return m
}

func TestRules(t *testing.T) {
	r := uint8(2)
	cases := []struct {
		name  string
		rule  func(*Input) []Finding
		setup func() *Input
		want  []string // finding keys; empty means no finding
	}{
		{"unreachable", nodeHealth, func() *Input {
			n := upNode("bee-1", 3, 2)
			n.Phase, n.PhaseReason = model.PhaseUnreachable, "dial tcp"
			return cluster(n)
		}, []string{"node-health:bee-1:unreachable"}},
		{"stale source", nodeHealth, func() *Input {
			n := upNode("bee-1", 3, 2)
			n.Sources["GET /metrics"] = &model.SourceHealth{Stale: true, LastOK: t0.Add(-time.Minute)}
			return cluster(n)
		}, []string{"node-health:bee-1:stale"}},
		{"healthy node", nodeHealth, func() *Input { return cluster(upNode("bee-1", 3, 2)) }, nil},
		{"churn", peerChurn, func() *Input {
			in := cluster(upNode("bee-1", 3, 2))
			for i := 0; i < 3; i++ {
				in.Churn["bee-1"] = append(in.Churn["bee-1"],
					ChurnEvent{T: t0.Add(-time.Duration(10*i) * time.Second), PeerNode: "bee-2", Connected: false},
					ChurnEvent{T: t0.Add(-time.Duration(10*i+5) * time.Second), PeerNode: "bee-2", Connected: true})
			}
			return in
		}, []string{"peer-churn:bee-1"}},
		{"old churn ignored", peerChurn, func() *Input {
			in := cluster(upNode("bee-1", 3, 2))
			for i := 0; i < 6; i++ {
				in.Churn["bee-1"] = append(in.Churn["bee-1"], ChurnEvent{T: t0.Add(-10 * time.Minute), Connected: i%2 == 0})
			}
			return in
		}, nil},
		{"radius diverges", radius, func() *Input {
			in := cluster(upNode("bee-1", 3, 2), upNode("bee-2", 3, 3))
			in.Snap.NetworkRadius = &r
			return in
		}, []string{"radius:bee-2:diverge"}},
		{"radius ignores bootnode", radius, func() *Input {
			b := upNode("bootnode-0", 3, 0)
			b.Group = "bootnode"
			in := cluster(upNode("bee-1", 3, 2), b)
			in.Snap.NetworkRadius = &r
			return in
		}, nil},
		{"pullsync stalled", pullsyncStall, func() *Input {
			in := cluster(upNode("bee-1", 3, 2))
			in.Series["bee-1"].Get(SPullsyncRate).Add(t0, 3)
			in.Series["bee-1"][SSyncedHist] = counter(t0, 2*time.Minute, 0)
			return in
		}, []string{"pullsync-stall:bee-1"}},
		{"pullsync progressing", pullsyncStall, func() *Input {
			in := cluster(upNode("bee-1", 3, 2))
			in.Series["bee-1"].Get(SPullsyncRate).Add(t0, 3)
			in.Series["bee-1"][SSyncedHist] = counter(t0, 2*time.Minute, 10)
			return in
		}, nil},
		{"push failures", pushBacklog, func() *Input {
			in := cluster(upNode("bee-1", 3, 2))
			in.Series["bee-1"][SPushAttempts] = counter(t0, time.Minute, 10)
			in.Series["bee-1"][SPushFailed] = counter(t0, time.Minute, 5)
			return in
		}, []string{"push-backlog:bee-1:failed"}},
		{"retrieval failures", failureSpike, func() *Input {
			in := cluster(upNode("bee-1", 3, 2))
			in.Series["bee-1"][SRetrReq] = counter(t0, time.Minute, 10)
			in.Series["bee-1"][SRetrFail] = counter(t0, time.Minute, 3)
			return in
		}, []string{"failure-spike:bee-1:retrieval"}},
		{"error log burst", errorLogs, func() *Input {
			in := cluster(upNode("bee-1", 3, 2))
			in.LogGroups["bee-1"] = []ErrGroup{{Level: "error", Logger: "node/api", Msg: "x", Count: 5}, {Level: "warning", Logger: "node/api", Msg: "y", Count: 19}}
			return in
		}, []string{"error-logs:bee-1:node/api:x"}},
		{"outlier needs a real difference", outlier, func() *Input {
			return cluster(upNode("a", 7, 2), upNode("b", 7, 2), upNode("c", 7, 2), upNode("d", 7, 2), upNode("e", 7, 2), upNode("f", 6, 2))
		}, nil},
		{"outlier", outlier, func() *Input {
			return cluster(upNode("a", 20, 2), upNode("b", 20, 2), upNode("c", 20, 2), upNode("d", 20, 2), upNode("e", 20, 2), upNode("f", 2, 2))
		}, []string{"outlier:f:connected_peers"}},
		{"isolated and frozen", topologyHealth, func() *Input {
			n := upNode("bee-1", 0, 2)
			n.Redistribution = &model.Redistribution{IsFrozen: true}
			return cluster(n)
		}, []string{"topology:bee-1:isolated", "topology:bee-1:frozen"}},
		{"unusable stamp and stalled tag", stampsTags, func() *Input {
			n := upNode("bee-1", 3, 2)
			n.Stamps = []model.Stamp{{BatchID: "b1", Exists: true, Usable: false}}
			in := cluster(n)
			in.Ops = []model.Operation{{ID: "tag:bee-1:1", Kind: "upload-tag", State: "stalled", Node: "bee-1"}}
			return in
		}, []string{"stamps-tags:bee-1:b1", "stamps-tags:tag:bee-1:1"}},
	}
	for _, c := range cases {
		got := keys(c.rule(c.setup()))
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
			continue
		}
		for _, k := range c.want {
			if !got[k] {
				t.Errorf("%s: missing %s (got %v)", c.name, k, got)
			}
		}
	}
}

func TestTrackerLifecycle(t *testing.T) {
	tr := NewTracker(20*time.Second, time.Minute, false)
	n := upNode("bee-1", 0, 2)
	in := cluster(n)
	trans := tr.Update(in)
	if len(trans) != 1 || !trans[0].Opened {
		t.Fatalf("open: %+v", trans)
	}
	// The condition clears; the issue resolves only after the quiet period.
	n.Connected = 3
	in.Now = t0.Add(10 * time.Second)
	if trans := tr.Update(in); len(trans) != 0 {
		t.Fatalf("resolved too early: %+v", trans)
	}
	in.Now = t0.Add(25 * time.Second)
	trans = tr.Update(in)
	if len(trans) != 1 || trans[0].Opened || trans[0].Issue.ResolvedAt == nil {
		t.Fatalf("resolve: %+v", trans)
	}
	if is := tr.Issues(); len(is) != 1 || is[0].ResolvedAt == nil {
		t.Fatalf("resolved issue should stay listed: %+v", is)
	}
	in.Now = t0.Add(2 * time.Minute)
	tr.Update(in)
	if is := tr.Issues(); len(is) != 0 {
		t.Fatalf("resolved issue kept past keep: %+v", is)
	}
}
