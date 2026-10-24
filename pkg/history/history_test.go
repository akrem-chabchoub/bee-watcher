package history

import (
	"testing"
	"time"

	"beescope/pkg/model"
)

func addN(s *Store, from, n int, at time.Time) {
	for i := 0; i < n; i++ {
		seq := uint64(from + i)
		s.AddEvent(model.Event{Seq: seq, ObservedAt: at.Add(time.Duration(i) * time.Millisecond), IngestedAt: at, Kind: "k", Severity: model.SevInfo, Node: "bee-1"})
	}
}

func TestRingEvictsOldestAndKeepsSeqContiguous(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxEvents = 10
	s := New(lim)
	now := time.Now()
	addN(s, 1, 25, now)
	if got := s.OldestSeq(); got != 16 {
		t.Fatalf("oldest = %d, want 16", got)
	}
	evs := s.Events(Filter{})
	if len(evs) != 10 {
		t.Fatalf("len = %d", len(evs))
	}
	for i := 1; i < len(evs); i++ {
		if evs[i].Seq != evs[i-1].Seq+1 {
			t.Fatalf("gap at %d", i)
		}
	}
	if after := s.Events(Filter{AfterSeq: 20}); len(after) != 5 || after[0].Seq != 21 {
		t.Fatalf("after 20: %d events starting %v", len(after), after)
	}
	if _, ok := s.Event(15); ok {
		t.Fatal("evicted event still returned")
	}
	if e, ok := s.Event(18); !ok || e.Seq != 18 {
		t.Fatalf("event 18: %v %v", e, ok)
	}
	if newest := s.Events(Filter{Newest: true, Limit: 3}); len(newest) != 3 || newest[2].Seq != 25 || newest[0].Seq != 23 {
		t.Fatalf("newest: %+v", newest)
	}
}

func TestMaxAgeEviction(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxAge = time.Minute
	s := New(lim)
	t0 := time.Now()
	addN(s, 1, 5, t0)
	addN(s, 6, 1, t0.Add(2*time.Minute))
	if got := s.OldestSeq(); got != 6 {
		t.Fatalf("oldest = %d, want 6", got)
	}
}

func TestFilterAndDensity(t *testing.T) {
	s := New(DefaultLimits())
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.AddEvent(model.Event{Seq: 1, ObservedAt: t0, IngestedAt: t0, Node: "a", Kind: "peer.connected", Severity: model.SevInfo, Corr: &model.Correlation{Chunk: "abcd"}})
	s.AddEvent(model.Event{Seq: 2, ObservedAt: t0.Add(time.Second), IngestedAt: t0, Node: "b", Kind: "log.error", Severity: model.SevError, Summary: "Disk full"})
	s.AddEvent(model.Event{Seq: 3, ObservedAt: t0.Add(2 * time.Second), IngestedAt: t0, Node: "a", Kind: "peer.disconnected", Severity: model.SevWarn, Corr: &model.Correlation{PeerNode: "b"}})
	cases := map[string]struct {
		f    Filter
		want []uint64
	}{
		"kind prefix":     {Filter{Kinds: []string{"peer."}}, []uint64{1, 3}},
		"min severity":    {Filter{MinSev: model.SevWarn}, []uint64{2, 3}},
		"node or peer":    {Filter{Nodes: map[string]bool{"b": true}}, []uint64{2, 3}},
		"text query":      {Filter{Query: "disk"}, []uint64{2}},
		"corr exact":      {Filter{Corr: "abcd"}, []uint64{1}},
		"corr prefix (q)": {Filter{Query: "ab"}, []uint64{1}},
		"time range":      {Filter{From: t0.Add(500 * time.Millisecond), To: t0.Add(1500 * time.Millisecond)}, []uint64{2}},
		"before seq":      {Filter{BeforeSeq: 2}, []uint64{1}},
	}
	for name, c := range cases {
		got := s.Events(c.f)
		var seqs []uint64
		for _, e := range got {
			seqs = append(seqs, e.Seq)
		}
		if len(seqs) != len(c.want) {
			t.Errorf("%s: got %v want %v", name, seqs, c.want)
			continue
		}
		for i := range seqs {
			if seqs[i] != c.want[i] {
				t.Errorf("%s: got %v want %v", name, seqs, c.want)
			}
		}
	}
	b := s.Density(t0, t0.Add(3*time.Second), 3, Filter{})
	if len(b) != 3 || b[0].Count["info"] != 1 || b[1].Count["error"] != 1 || b[2].Count["warn"] != 1 {
		t.Fatalf("density %+v", b)
	}
}

func TestKeyframesRawAndLogs(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxRaw = 2
	s := New(lim)
	t0 := time.Now()
	for i := 0; i < 4; i++ {
		s.AddKeyframe(t0.Add(time.Duration(i)*5*time.Second), []byte{byte('0' + i)})
	}
	if k, ok := s.KeyframeAt(t0.Add(12 * time.Second)); !ok || string(k.JSON) != "2" {
		t.Fatalf("keyframe at +12s = %q", k.JSON)
	}
	if k, ok := s.KeyframeAt(t0.Add(-time.Hour)); !ok || string(k.JSON) != "0" {
		t.Fatalf("keyframe before range = %q", k.JSON)
	}
	a, b, c := s.AddRaw([]byte("a")), s.AddRaw([]byte("b")), s.AddRaw([]byte("c"))
	if _, ok := s.Raw(a); ok {
		t.Fatal("raw a should be evicted")
	}
	if v, ok := s.Raw(b); !ok || string(v) != "b" {
		t.Fatal("raw b missing")
	}
	if v, ok := s.Raw(c); !ok || string(v) != "c" {
		t.Fatal("raw c missing")
	}
	s.AddLog("bee-1", "pushsync", model.LogLine{Level: "debug", Raw: "chunk pushed abc", Time: t0})
	s.AddLog("bee-2", "api", model.LogLine{Level: "error", Raw: "boom", Time: t0})
	if got := s.Logs(LogQuery{MinLevel: "warning"}); len(got) != 1 || got[0].Node != "bee-2" {
		t.Fatalf("logs by level: %+v", got)
	}
	if got := s.Logs(LogQuery{Query: "ABC"}); len(got) != 1 || got[0].Component != "pushsync" {
		t.Fatalf("logs by text: %+v", got)
	}
}
