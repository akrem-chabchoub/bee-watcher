package record

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"beescope/pkg/model"
)

func TestRecordReplayRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rec.ndjson")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	in, out := make(chan model.Observation, 8), make(chan model.Observation, 8)
	sent := []model.Observation{
		{Kind: model.ObsHealth, Node: "bee-1", ObservedAt: t0, Health: &model.Health{Version: "2.7.0"}, Source: model.Source{Adapter: "api", Ref: "GET /health"}},
		{Kind: model.ObsLog, Node: "bee-1", ObservedAt: t0.Add(time.Second), Log: &model.LogLine{Msg: "hi", Level: "info"}, Simulated: true},
	}
	for _, o := range sent {
		in <- o
	}
	close(in)
	if err := Tee(context.Background(), in, out, f); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if len(out) != 2 {
		t.Fatalf("tee forwarded %d", len(out))
	}
	clock := &Clock{}
	got := make(chan model.Observation, 8)
	if err := Replay(context.Background(), path, 0, got, clock); err != nil {
		t.Fatal(err)
	}
	var all []model.Observation
	for o := range got {
		all = append(all, o)
	}
	if len(all) != 2 || all[0].Health.Version != "2.7.0" || all[1].Log.Msg != "hi" || !all[1].Simulated {
		t.Fatalf("replayed %+v", all)
	}
	if !clock.Now().Equal(t0.Add(time.Second)) {
		t.Fatalf("clock = %v", clock.Now())
	}
}
