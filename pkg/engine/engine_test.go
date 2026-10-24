package engine

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"beescope/pkg/collect"
	"beescope/pkg/history"
	"beescope/pkg/model"
	"beescope/pkg/source/sim"
)

// harness drives the engine synchronously: observations are applied and the
// 1 Hz tick runs on a fake clock, so a six-minute scenario takes milliseconds.
type harness struct {
	t    *testing.T
	now  time.Time
	e    *Engine
	hist *history.Store
	sim  *sim.Sim
	obs  chan model.Observation
	// opened records every issue ID that was ever open, with the scenario
	// second it first appeared.
	opened map[string]int
}

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, now: time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC), obs: make(chan model.Observation, 1<<16), opened: map[string]int{}}
	clock := func() time.Time { return h.now }
	h.hist = history.New(history.DefaultLimits())
	cfg := Config{Mode: "sim", Target: "test", Intervals: collect.DefaultIntervals(), Clock: clock, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h.e = New(cfg, h.obs, h.hist, NewHub())
	h.sim = sim.New(sim.Options{Intervals: collect.DefaultIntervals(), Seed: 1, Now: clock})
	return h
}

// run advances the scenario to second `until` (exclusive).
func (h *harness) run(from, until int) {
	ctx := context.Background()
	for sec := from; sec < until; sec++ {
		h.now = h.now.Add(time.Second)
		h.sim.Step(ctx, h.obs)
		for len(h.obs) > 0 {
			h.e.observe(<-h.obs)
		}
		h.e.flushAggs(true)
		h.e.flushFlows()
		h.e.tick()
		var snap model.Snapshot
		if err := json.Unmarshal(h.e.SnapshotJSON(), &snap); err != nil {
			h.t.Fatal(err)
		}
		for _, is := range snap.Issues {
			if _, ok := h.opened[is.ID]; !ok && is.ResolvedAt == nil {
				h.opened[is.ID] = sec
			}
		}
	}
}

func (h *harness) snapshot() model.Snapshot {
	var snap model.Snapshot
	if err := json.Unmarshal(h.e.SnapshotJSON(), &snap); err != nil {
		h.t.Fatal(err)
	}
	return snap
}

func TestScenarioSteadyStateHasNoIssues(t *testing.T) {
	h := newHarness(t)
	h.run(0, 55)
	snap := h.snapshot()
	if len(snap.Nodes) != 8 {
		t.Fatalf("nodes = %d, want 8", len(snap.Nodes))
	}
	for name, n := range snap.Nodes {
		if n.Phase != model.PhaseUp {
			t.Errorf("%s phase %s", name, n.Phase)
		}
		if n.Components["netstore"].Status != model.CompUnobservable {
			t.Errorf("%s netstore = %s, want unobservable (it has no metric or log source)", name, n.Components["netstore"].Status)
		}
	}
	if len(snap.Edges) != 28 {
		t.Errorf("edges = %d, want a full mesh of 28", len(snap.Edges))
	}
	if len(h.opened) != 0 {
		t.Errorf("issues in steady state: %v", h.opened)
	}
}

func TestScenarioDetectsEveryScriptedFault(t *testing.T) {
	h := newHarness(t)
	h.run(0, sim.Loop)
	want := map[string][2]int{ // issue → window of scenario seconds in which it must open
		"failure-spike:bee-2:retrieval": {120, 180},
		"peer-churn:bee-4":              {150, 210},
		"radius:bee-6:diverge":          {150, 200},
		"pullsync-stall:bee-6":          {270, 330},
		"node-health:bee-3:unreachable": {240, 260},
		"peer-churn:target:bee-3":       {240, 260},
		"topology:bee-6:salud":          {150, 240},
	}
	for id, w := range want {
		at, ok := h.opened[id]
		if !ok {
			t.Errorf("issue %s never opened", id)
			continue
		}
		if at < w[0] || at >= w[1] {
			t.Errorf("issue %s opened at %d s, want within [%d,%d)", id, at, w[0], w[1])
		}
	}
	for id := range h.opened {
		if strings.HasPrefix(id, "outlier:") || strings.HasPrefix(id, "error-logs:") {
			t.Errorf("unexpected noisy issue %s", id)
		}
	}

	evs := h.hist.Events(history.Filter{Limit: 20000})
	kinds := map[string]int{}
	for i, ev := range evs {
		kinds[ev.Kind]++
		if ev.Provenance != model.Simulated {
			t.Fatalf("event %d (%s) provenance %s, want simulated", ev.Seq, ev.Kind, ev.Provenance)
		}
		if i > 0 && ev.Seq != evs[i-1].Seq+1 {
			t.Fatalf("seq gap between %d and %d", evs[i-1].Seq, ev.Seq)
		}
	}
	for _, k := range []string{"upload.started", "upload.synced", "node.unreachable", "node.restarted", "radius.changed", "peer.disconnected", "trace.error"} {
		if kinds[k] == 0 {
			t.Errorf("no %s event", k)
		}
	}

	// Flows come only from log lines and spans that name a peer.
	var pushFromBee1 int
	for _, fr := range h.hist.Flows(time.Time{}, h.now) {
		for _, f := range fr.Flows {
			if f.Source != "log" && f.Source != "otlp" {
				t.Fatalf("flow from source %q", f.Source)
			}
			if f.From == "bee-1" && f.Protocol == "pushsync" {
				pushFromBee1 += f.Count
			}
		}
	}
	if pushFromBee1 == 0 {
		t.Error("no pushsync flows from bee-1 during the upload")
	}

	var tagDone, traces bool
	for _, op := range h.e.Operations() {
		tagDone = tagDone || (op.Kind == "upload-tag" && op.State == "done")
		traces = traces || op.Kind == "trace"
	}
	if !tagDone || !traces {
		t.Errorf("operations: tag done=%v traces=%v", tagDone, traces)
	}

	// After the loop bee-3 is back up and the faults have cleared.
	snap := h.snapshot()
	if p := snap.Nodes["bee-3"].Phase; p != model.PhaseUp {
		t.Errorf("bee-3 phase after recovery = %s", p)
	}
	for _, is := range snap.Issues {
		if is.ResolvedAt == nil && strings.HasPrefix(is.ID, "node-health:bee-3") {
			t.Errorf("issue %s still open after recovery", is.ID)
		}
	}
	if k, ok := h.hist.KeyframeAt(h.now); !ok || h.now.Sub(k.At) > 5*time.Second {
		t.Errorf("no recent keyframe")
	}
}
