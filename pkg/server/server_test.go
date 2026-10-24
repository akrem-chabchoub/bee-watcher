package server

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"beescope/pkg/collect"
	"beescope/pkg/engine"
	"beescope/pkg/history"
	"beescope/pkg/model"
	"beescope/pkg/source/beeapi"
)

type fakeNodes struct{ base string }

func (f fakeNodes) Client(node string) (*beeapi.Client, bool) {
	if node != "bee-1" {
		return nil, false
	}
	return beeapi.New(f.base, http.DefaultClient), true
}

type env struct {
	srv  *httptest.Server
	eng  *engine.Engine
	hist *history.Store
}

func newEnv(t *testing.T, maxEvents int, meta Meta, nodes NodeAPI) *env {
	t.Helper()
	lim := history.DefaultLimits()
	lim.MaxEvents = maxEvents
	hist := history.New(lim)
	hub := engine.NewHub()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := engine.New(engine.Config{Mode: meta.Mode, Intervals: collect.DefaultIntervals(), Log: log}, make(chan model.Observation), hist, hub)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go eng.Run(ctx)
	ui := fstest.MapFS{"index.html": {Data: []byte("<html>app</html>")}, "assets/app.js": {Data: []byte("js")}}
	srv := httptest.NewServer(New(Config{Engine: eng, Hist: hist, Hub: hub, Meta: meta, Nodes: nodes, UI: ui, Log: log}))
	t.Cleanup(srv.Close)
	return &env{srv: srv, eng: eng, hist: hist}
}

func (e *env) record(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := e.eng.Record(context.Background(), model.Event{Kind: "test", Summary: "e"}); err != nil {
			t.Fatal(err)
		}
	}
}

type frame struct {
	typ, id, data string
}

// readFrames reads SSE frames until stop returns true or the deadline hits.
func readFrames(t *testing.T, url, lastID string, stop func([]frame) bool) []frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/stream", nil)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	var out []frame
	var cur frame
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if cur.typ != "" {
				out = append(out, cur)
				if stop(out) {
					return out
				}
			}
			cur = frame{}
		case strings.HasPrefix(line, "event: "):
			cur.typ = line[7:]
		case strings.HasPrefix(line, "id: "):
			cur.id = line[4:]
		case strings.HasPrefix(line, "data: "):
			cur.data = line[6:]
		}
	}
	t.Fatalf("stream ended before the condition was met; got %d frames", len(out))
	return nil
}

func seqs(t *testing.T, fs []frame) []uint64 {
	var out []uint64
	for _, f := range fs {
		if f.typ != "events" {
			continue
		}
		var evs []model.Event
		if err := json.Unmarshal([]byte(f.data), &evs); err != nil {
			t.Fatal(err)
		}
		for _, e := range evs {
			out = append(out, e.Seq)
		}
	}
	return out
}

func hasSeq(t *testing.T, fs []frame, seq uint64) bool {
	for _, s := range seqs(t, fs) {
		if s == seq {
			return true
		}
	}
	return false
}

func TestStreamResumeAndGap(t *testing.T) {
	e := newEnv(t, 50, Meta{Mode: "live"}, nil)
	e.record(t, 80) // keeps seq 31..80

	fs := readFrames(t, e.srv.URL, "10", func(fs []frame) bool { return hasSeq(t, fs, 80) })
	if fs[0].typ != "hello" || fs[1].typ != "snapshot" {
		t.Fatalf("first frames %s, %s", fs[0].typ, fs[1].typ)
	}
	var gap map[string]uint64
	for _, f := range fs {
		if f.typ == "gap" {
			_ = json.Unmarshal([]byte(f.data), &gap)
		}
	}
	if gap["requested"] != 10 || gap["oldest"] != 31 {
		t.Fatalf("gap frame %v", gap)
	}
	got := seqs(t, fs)
	if got[0] != 31 {
		t.Fatalf("replay starts at %d, want 31", got[0])
	}

	fs = readFrames(t, e.srv.URL, "70", func(fs []frame) bool { return hasSeq(t, fs, 80) })
	for _, f := range fs {
		if f.typ == "gap" {
			t.Fatal("unexpected gap frame")
		}
	}
	if got := seqs(t, fs); got[0] != 71 {
		t.Fatalf("resume starts at %d, want 71", got[0])
	}
}

func TestStreamDeliversLiveEvents(t *testing.T) {
	e := newEnv(t, 1000, Meta{Mode: "live"}, nil)
	e.record(t, 3)
	var once sync.Once
	fs := readFrames(t, e.srv.URL, "", func(fs []frame) bool {
		if len(fs) >= 2 {
			once.Do(func() {
				go func() { _ = e.eng.Record(context.Background(), model.Event{Kind: "test", Summary: "live"}) }()
			})
		}
		return hasSeq(t, fs, 4)
	})
	for _, f := range fs {
		if f.typ == "events" && f.id == "" {
			t.Fatal("events frame without id")
		}
	}
}

func post(t *testing.T, url, ct, origin, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func TestActions(t *testing.T) {
	var gotMethod, gotPath string
	bee := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer bee.Close()
	e := newEnv(t, 1000, Meta{Mode: "live", Actions: true}, fakeNodes{bee.URL})
	url := e.srv.URL + "/api/actions/loggers"

	if code, m := post(t, url, "application/json", "", `{"node":"bee-1","expression":"node/pushsync","verbosity":"debug"}`); code != 400 || m["consequence"] == nil {
		t.Fatalf("unconfirmed: %d %v", code, m)
	}
	if code, _ := post(t, url, "text/plain", "", `{"confirm":true}`); code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain: %d", code)
	}
	if code, _ := post(t, url, "application/json", "http://evil.example", `{"confirm":true}`); code != http.StatusForbidden {
		t.Fatalf("cross origin: %d", code)
	}
	code, m := post(t, url, "application/json", e.srv.URL, `{"confirm":true,"node":"bee-1","expression":"node/pushsync","verbosity":"debug"}`)
	if code != 200 || m["ok"] != true {
		t.Fatalf("confirmed: %d %v", code, m)
	}
	want := "/loggers/" + base64.URLEncoding.EncodeToString([]byte("node/pushsync")) + "/debug"
	if gotMethod != http.MethodPut || gotPath != want {
		t.Fatalf("bee saw %s %s, want PUT %s", gotMethod, gotPath, want)
	}
	if evs := e.hist.Events(history.Filter{Kinds: []string{"action."}}); len(evs) != 1 {
		t.Fatalf("audit events: %d", len(evs))
	}
	if code, _ := post(t, e.srv.URL+"/api/actions/probe-retrieval", "application/json", "", `{"confirm":true,"node":"bee-1","chunk":"zz"}`); code != 400 {
		t.Fatalf("bad chunk: %d", code)
	}

	sim := newEnv(t, 10, Meta{Mode: "sim"}, nil)
	if code, _ := post(t, sim.srv.URL+"/api/actions/debugstore", "application/json", "", `{"confirm":true,"node":"bee-1"}`); code != http.StatusConflict {
		t.Fatalf("sim mode: %d", code)
	}
}

func TestHostCheckAndUI(t *testing.T) {
	e := newEnv(t, 10, Meta{Mode: "live"}, nil)
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/api/meta", nil)
	req.Host = "attacker.example:7070"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("rebinding host: %d", resp.StatusCode)
	}
	for path, want := range map[string]string{"/": "<html>app</html>", "/timeline": "<html>app</html>", "/assets/app.js": "js"} {
		resp, err := http.Get(e.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(b) != want {
			t.Errorf("%s = %q", path, b)
		}
	}
	resp, err = http.Get(e.srv.URL + "/api/nope")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("unknown API route: %d", resp.StatusCode)
	}
}

func TestComponentsAndEventsQuery(t *testing.T) {
	e := newEnv(t, 100, Meta{Mode: "live"}, nil)
	e.record(t, 5)
	var comps struct {
		Specs []ComponentSpec `json:"specs"`
		Flows []FlowSpec      `json:"flows"`
	}
	resp, err := http.Get(e.srv.URL + "/api/components")
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewDecoder(resp.Body).Decode(&comps)
	resp.Body.Close()
	if len(comps.Specs) < 10 || len(comps.Flows) < 10 || comps.Specs[0].ID != "api" {
		t.Fatalf("components: %d specs, %d flows", len(comps.Specs), len(comps.Flows))
	}
	resp, err = http.Get(e.srv.URL + "/api/events?after=2&limit=2")
	if err != nil {
		t.Fatal(err)
	}
	var evs []model.Event
	_ = json.NewDecoder(resp.Body).Decode(&evs)
	resp.Body.Close()
	if len(evs) != 2 || evs[0].Seq != 3 {
		t.Fatalf("events: %+v", evs)
	}
	resp, err = http.Get(e.srv.URL + "/api/events?from=yesterday")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("bad time accepted: %d", resp.StatusCode)
	}
}
