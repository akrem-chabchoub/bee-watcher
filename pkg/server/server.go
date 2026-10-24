// Package server is bee-scope's HTTP layer: the SSE stream, the read-only
// query API, the explicit user-confirmed actions and the embedded UI. See
// docs/API.md for the contract.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"beescope/pkg/anomaly"
	"beescope/pkg/component"
	"beescope/pkg/engine"
	"beescope/pkg/history"
	"beescope/pkg/model"
	"beescope/pkg/source/beeapi"
)

// NodeAPI gives access to a node's Bee API for actions.
type NodeAPI interface {
	Client(node string) (*beeapi.Client, bool)
}

// Meta is GET /api/meta. It never carries credentials or API base URLs.
type Meta struct {
	Mode      string             `json:"mode"`
	Target    string             `json:"target"`
	Version   string             `json:"version"`
	StartedAt time.Time          `json:"startedAt"`
	OTLP      string             `json:"otlp"`
	Logs      bool               `json:"logs"`
	Actions   bool               `json:"actions"`
	Intervals map[string]float64 `json:"intervals"`
	HistoryS  float64            `json:"historyS"`
}

// Config wires the server.
type Config struct {
	Engine *engine.Engine
	Hist   *history.Store
	Hub    *engine.Hub
	Meta   Meta
	Nodes  NodeAPI // nil disables actions
	UI     fs.FS   // built UI (web/dist); nil serves nothing at /
	Dev    string  // when set, non-API requests are proxied here (Vite)
	Log    *slog.Logger
}

// Server serves the API and the UI.
type Server struct {
	cfg Config
	mux *http.ServeMux
}

// New builds the handler tree.
func New(cfg Config) *Server {
	s := &Server{cfg: cfg, mux: http.NewServeMux()}
	m := s.mux
	m.HandleFunc("GET /api/stream", s.stream)
	m.HandleFunc("GET /api/meta", s.meta)
	m.HandleFunc("GET /api/snapshot", s.snapshot)
	m.HandleFunc("GET /api/state", s.state)
	m.HandleFunc("GET /api/window", s.window)
	m.HandleFunc("GET /api/events", s.events)
	m.HandleFunc("GET /api/event/{seq}", s.event)
	m.HandleFunc("GET /api/density", s.density)
	m.HandleFunc("GET /api/flows", s.flows)
	m.HandleFunc("GET /api/raw/{id}", s.raw)
	m.HandleFunc("GET /api/node/{name}", s.node)
	m.HandleFunc("GET /api/components", s.components)
	m.HandleFunc("GET /api/rules", s.rules)
	m.HandleFunc("GET /api/logs", s.logs)
	m.HandleFunc("GET /api/chunk/{addr}", s.chunk)
	m.HandleFunc("GET /api/operations", s.operations)
	m.HandleFunc("GET /api/operations/{id...}", s.operation)
	m.HandleFunc("GET /api/search", s.search)
	m.HandleFunc("GET /api/export", s.export)
	m.HandleFunc("POST /api/actions/{name}", s.action)
	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { httpError(w, http.StatusNotFound, "unknown API route") })
	m.Handle("/", s.ui())
	return s
}

// ServeHTTP applies the host check and dispatches.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !hostOK(r.Host) {
		// Defends the loopback listener against DNS rebinding: a page on
		// another origin must not reach the API through a name it controls.
		httpError(w, http.StatusForbidden, "bee-scope only answers requests addressed to localhost or an IP address")
		return
	}
	s.mux.ServeHTTP(w, r)
}

func hostOK(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	h = strings.Trim(h, "[]")
	return h == "localhost" || strings.HasSuffix(h, ".localhost") || net.ParseIP(h) != nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Debug("write response", "error", err)
	}
}

func writeRaw(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg, "code": code})
}

// parseTime accepts RFC 3339 or Unix milliseconds.
func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.UnixMilli(ms), nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("bad time %q: use RFC 3339 or Unix milliseconds", s)
	}
	return t, nil
}

func list(q url.Values, key string) []string {
	var out []string
	for _, v := range q[key] {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func set(vals []string) map[string]bool {
	if len(vals) == 0 {
		return nil
	}
	m := make(map[string]bool, len(vals))
	for _, v := range vals {
		m[v] = true
	}
	return m
}

func filterFrom(q url.Values) (history.Filter, error) {
	var f history.Filter
	var err error
	if f.From, err = parseTime(q.Get("from")); err != nil {
		return f, err
	}
	if f.To, err = parseTime(q.Get("to")); err != nil {
		return f, err
	}
	if v := q.Get("after"); v != "" {
		if f.AfterSeq, err = strconv.ParseUint(v, 10, 64); err != nil {
			return f, fmt.Errorf("bad after: %w", err)
		}
	}
	if v := q.Get("before"); v != "" {
		if f.BeforeSeq, err = strconv.ParseUint(v, 10, 64); err != nil {
			return f, fmt.Errorf("bad before: %w", err)
		}
	}
	f.Nodes = set(list(q, "node"))
	f.Components = set(list(q, "component"))
	f.Kinds = list(q, "kind")
	f.MinSev = model.Severity(q.Get("minSev"))
	if p := list(q, "prov"); len(p) > 0 {
		f.Prov = map[model.Provenance]bool{}
		for _, v := range p {
			f.Prov[model.Provenance(v)] = true
		}
	}
	f.Query = q.Get("q")
	f.Corr = q.Get("corr")
	f.Limit = 1000
	if v := q.Get("limit"); v != "" {
		if f.Limit, err = strconv.Atoi(v); err != nil {
			return f, fmt.Errorf("bad limit: %w", err)
		}
	}
	f.Newest = q.Get("newest") == "1" || q.Get("newest") == "true"
	return f, nil
}

func (s *Server) meta(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.cfg.Meta) }

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	writeRaw(w, s.cfg.Engine.SnapshotJSON())
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	at, err := parseTime(r.URL.Query().Get("at"))
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	k, ok := s.cfg.Hist.KeyframeAt(at)
	if !ok {
		httpError(w, http.StatusNotFound, "no keyframe recorded yet")
		return
	}
	w.Header().Set("X-Keyframe-At", k.At.Format(time.RFC3339Nano))
	writeRaw(w, k.JSON)
}

func (s *Server) window(w http.ResponseWriter, r *http.Request) {
	from, to := s.cfg.Hist.Window()
	writeJSON(w, map[string]any{"from": from, "to": to, "oldestSeq": s.cfg.Hist.OldestSeq(), "lastSeq": s.cfg.Engine.LastSeq()})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	f, err := filterFrom(r.URL.Query())
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	evs := s.cfg.Hist.Events(f)
	if evs == nil {
		evs = []model.Event{}
	}
	writeJSON(w, evs)
}

func (s *Server) event(w http.ResponseWriter, r *http.Request) {
	seq, err := strconv.ParseUint(r.PathValue("seq"), 10, 64)
	if err != nil {
		httpError(w, http.StatusBadRequest, "bad seq")
		return
	}
	ev, ok := s.cfg.Hist.Event(seq)
	if !ok {
		httpError(w, http.StatusNotFound, "event not in history (evicted or never existed)")
		return
	}
	writeJSON(w, ev)
}

func (s *Server) density(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f, err := filterFrom(q)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	from, to := f.From, f.To
	f.From, f.To = time.Time{}, time.Time{}
	if from.IsZero() || to.IsZero() {
		wf, wt := s.cfg.Hist.Window()
		if from.IsZero() {
			from = wf
		}
		if to.IsZero() {
			to = wt
		}
	}
	n := 200
	if v := q.Get("n"); v != "" {
		if n, err = strconv.Atoi(v); err != nil {
			httpError(w, http.StatusBadRequest, "bad n")
			return
		}
	}
	b := s.cfg.Hist.Density(from, to, n, f)
	if b == nil {
		b = []history.Bucket{}
	}
	writeJSON(w, b)
}

func (s *Server) flows(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, err1 := parseTime(q.Get("from"))
	to, err2 := parseTime(q.Get("to"))
	if err := errors.Join(err1, err2); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	if to.IsZero() {
		to = time.Now()
	}
	if from.IsZero() {
		from = to.Add(-time.Minute)
	}
	fr := s.cfg.Hist.Flows(from, to)
	if fr == nil {
		fr = []history.FlowFrame{}
	}
	writeJSON(w, fr)
}

func (s *Server) raw(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		httpError(w, http.StatusBadRequest, "bad id")
		return
	}
	b, ok := s.cfg.Hist.Raw(id)
	if !ok {
		httpError(w, http.StatusNotFound, "raw payload evicted or unknown")
		return
	}
	if !json.Valid(b) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(b)
		return
	}
	writeRaw(w, b)
}

func (s *Server) node(w http.ResponseWriter, r *http.Request) {
	d, ok, err := s.cfg.Engine.NodeDetail(r.Context(), r.PathValue("name"))
	if err != nil {
		httpError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if !ok {
		httpError(w, http.StatusNotFound, "unknown node")
		return
	}
	if d.Peers == nil {
		d.Peers = []engine.PeerView{}
	}
	writeJSON(w, d)
}

// ComponentSpec is the JSON form of component.Spec.
type ComponentSpec struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Lane        string   `json:"lane"`
	Pkg         string   `json:"pkg"`
	Description string   `json:"description"`
	Metrics     []string `json:"metrics"`
	Loggers     []string `json:"loggers"`
	Spans       []string `json:"spans"`
	Endpoints   []string `json:"endpoints"`
	Blind       []string `json:"blind"`
	X           float64  `json:"x"`
	Y           float64  `json:"y"`
}

// FlowSpec is the JSON form of component.FlowSpec.
type FlowSpec struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Path   string `json:"path"`
	Label  string `json:"label"`
	Signal string `json:"signal"`
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (s *Server) components(w http.ResponseWriter, r *http.Request) {
	specs := make([]ComponentSpec, 0, len(component.Specs))
	for _, sp := range component.Specs {
		var ms []string
		seen := map[string]bool{}
		addM := func(name string) {
			if !seen[name] {
				seen[name] = true
				ms = append(ms, name)
			}
		}
		for _, m := range sp.Activity {
			addM(m.Name)
		}
		for _, m := range sp.Errors {
			addM(m.Name)
		}
		if sp.Queue != nil {
			addM(sp.Queue.Name)
		}
		for _, m := range sp.Gauges {
			addM(m.Name)
		}
		specs = append(specs, ComponentSpec{ID: sp.ID, Label: sp.Label, Lane: sp.Lane, Pkg: sp.Pkg, Description: sp.Description,
			Metrics: nonNil(ms), Loggers: nonNil(sp.Loggers), Spans: nonNil(sp.Spans), Endpoints: nonNil(sp.Endpoints), Blind: nonNil(sp.Blind), X: sp.X, Y: sp.Y})
	}
	flows := make([]FlowSpec, 0, len(component.Flows))
	for _, f := range component.Flows {
		fs := FlowSpec{From: f.From, To: f.To, Path: f.Path, Label: f.Label}
		if f.Signal != nil {
			fs.Signal = f.Signal.Name
		}
		flows = append(flows, fs)
	}
	writeJSON(w, map[string]any{"specs": specs, "flows": flows})
}

func (s *Server) rules(w http.ResponseWriter, r *http.Request) {
	out := make([]map[string]string, 0, len(anomaly.Rules))
	for _, rl := range anomaly.Rules {
		out = append(out, map[string]string{"id": rl.ID, "name": rl.Name})
	}
	writeJSON(w, out)
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, err1 := parseTime(q.Get("from"))
	to, err2 := parseTime(q.Get("to"))
	if err := errors.Join(err1, err2); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	recs := s.cfg.Hist.Logs(history.LogQuery{Node: q.Get("node"), Component: q.Get("component"), Query: q.Get("q"), MinLevel: q.Get("level"), From: from, To: to, Limit: limit})
	if recs == nil {
		recs = []history.LogRecord{}
	}
	writeJSON(w, recs)
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return s != ""
}

func (s *Server) chunk(w http.ResponseWriter, r *http.Request) {
	addr := strings.ToLower(r.PathValue("addr"))
	if !isHex(addr) || len(addr) < 4 || len(addr) > 64 {
		httpError(w, http.StatusBadRequest, "chunk address must be 4 to 64 hex characters")
		return
	}
	f := history.Filter{Limit: 500, Newest: true}
	if len(addr) == 64 {
		f.Corr = addr
	} else {
		f.Query = addr
	}
	evs := s.cfg.Hist.Events(f)
	if evs == nil {
		evs = []model.Event{}
	}
	recs := s.cfg.Hist.Logs(history.LogQuery{Query: addr, Limit: 500})
	if recs == nil {
		recs = []history.LogRecord{}
	}
	writeJSON(w, map[string]any{"address": addr, "correlation": "approximate", "events": evs, "logs": recs})
}

func (s *Server) operations(w http.ResponseWriter, r *http.Request) {
	ops := s.cfg.Engine.Operations()
	if ops == nil {
		ops = []model.Operation{}
	}
	writeJSON(w, ops)
}

func (s *Server) operation(w http.ResponseWriter, r *http.Request) {
	op, ok := s.cfg.Engine.Operation(r.PathValue("id"))
	if !ok {
		httpError(w, http.StatusNotFound, "unknown operation")
		return
	}
	writeJSON(w, op)
}

// SearchResult is one hit of GET /api/search.
type SearchResult struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	out := []SearchResult{}
	if q == "" {
		writeJSON(w, out)
		return
	}
	const max = 40
	seen := map[string]bool{}
	add := func(res SearchResult) {
		k := res.Type + "|" + res.ID
		if len(out) < max && !seen[k] {
			seen[k] = true
			out = append(out, res)
		}
	}
	var snap model.Snapshot
	_ = json.Unmarshal(s.cfg.Engine.SnapshotJSON(), &snap)
	for _, n := range snap.Nodes {
		if strings.Contains(strings.ToLower(n.Name), q) || strings.Contains(strings.ToLower(n.Pod), q) {
			add(SearchResult{Type: "node", ID: n.Name, Label: n.Name, Detail: fmt.Sprintf("%s · %s", n.Phase, short(n.Overlay))})
		}
		if len(q) >= 4 && strings.HasPrefix(n.Overlay, q) {
			add(SearchResult{Type: "node", ID: n.Name, Label: n.Name, Detail: "overlay " + n.Overlay})
		}
	}
	for _, sp := range component.Specs {
		if strings.Contains(sp.ID, q) || strings.Contains(strings.ToLower(sp.Label), q) {
			add(SearchResult{Type: "component", ID: sp.ID, Label: sp.Label, Detail: sp.Pkg})
		}
	}
	for _, is := range snap.Issues {
		if strings.Contains(strings.ToLower(is.Title), q) || strings.Contains(strings.ToLower(is.ID), q) {
			state := "open"
			if is.ResolvedAt != nil {
				state = "resolved"
			}
			add(SearchResult{Type: "issue", ID: is.ID, Label: is.Title, Detail: string(is.Class) + " · " + state})
		}
	}
	for _, op := range s.cfg.Engine.Operations() {
		if strings.Contains(strings.ToLower(op.ID), q) || strings.Contains(strings.ToLower(op.Title), q) {
			typ := "operation"
			if op.Kind == "upload-tag" {
				typ = "tag"
			}
			add(SearchResult{Type: typ, ID: op.ID, Label: op.Title, Detail: op.State})
		}
	}
	if len(q) >= 4 && isHex(q) {
		for _, ev := range s.cfg.Hist.Events(history.Filter{Query: q, Newest: true, Limit: 300}) {
			c := ev.Corr
			if c == nil {
				continue
			}
			if strings.HasPrefix(c.Chunk, q) {
				add(SearchResult{Type: "chunk", ID: c.Chunk, Label: "chunk " + short(c.Chunk), Detail: "seen on " + ev.Node + " (approximate)"})
			}
			if strings.HasPrefix(c.BatchID, q) {
				add(SearchResult{Type: "batch", ID: c.BatchID, Label: "batch " + short(c.BatchID), Detail: ev.Node})
			}
			if strings.HasPrefix(c.TraceID, q) {
				add(SearchResult{Type: "trace", ID: "trace:" + c.TraceID, Label: "trace " + short(c.TraceID), Detail: ev.Summary})
			}
			if strings.HasPrefix(c.Peer, q) {
				label := "peer " + short(c.Peer)
				if c.PeerNode != "" {
					label += " (" + c.PeerNode + ")"
				}
				add(SearchResult{Type: "peer", ID: c.Peer, Label: label, Detail: "seen by " + ev.Node})
			}
		}
	}
	writeJSON(w, out)
}

func short(a string) string {
	if len(a) > 8 {
		return a[:8] + "…"
	}
	return a
}

func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	evs := s.cfg.Hist.Events(history.Filter{Newest: true, Limit: 20_000})
	if evs == nil {
		evs = []model.Event{}
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="bee-scope-%s.json"`, now.UTC().Format("20060102-150405")))
	// The snapshot and events never contain kubeconfig data, Authorization
	// headers or API base URLs: the engine does not store any of them.
	writeJSON(w, map[string]any{
		"exportedAt": now,
		"meta":       s.cfg.Meta,
		"snapshot":   json.RawMessage(s.cfg.Engine.SnapshotJSON()),
		"events":     evs,
		"operations": s.cfg.Engine.Operations(),
	})
}

// stream is the SSE endpoint.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	var after uint64
	resume := false
	for _, v := range []string{r.Header.Get("Last-Event-ID"), r.URL.Query().Get("after")} {
		if v == "" {
			continue
		}
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			httpError(w, http.StatusBadRequest, "bad Last-Event-ID / after")
			return
		}
		after, resume = n, true
		break
	}
	// Subscribe before reading history so nothing published in between is
	// lost; duplicates are removed by the client using seq.
	sub := s.cfg.Hub.Subscribe()
	defer s.cfg.Hub.Unsubscribe(sub)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	write := func(typ string, id uint64, data []byte) error {
		b := make([]byte, 0, len(data)+64)
		b = append(b, "event: "...)
		b = append(b, typ...)
		b = append(b, '\n')
		if id > 0 {
			b = strconv.AppendUint(append(b, "id: "...), id, 10)
			b = append(b, '\n')
		}
		b = append(b, "data: "...)
		b = append(b, data...)
		b = append(b, "\n\n"...)
		_, err := w.Write(b)
		return err
	}
	writeV := func(typ string, id uint64, v any) error {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return write(typ, id, data)
	}

	oldest := s.cfg.Hist.OldestSeq()
	if err := writeV("hello", 0, map[string]any{"lastSeq": s.cfg.Engine.LastSeq(), "oldestSeq": oldest, "mode": s.cfg.Meta.Mode}); err != nil {
		return
	}
	if err := write("snapshot", 0, s.cfg.Engine.SnapshotJSON()); err != nil {
		return
	}
	if resume {
		if oldest > after+1 {
			if err := writeV("gap", 0, map[string]uint64{"requested": after, "oldest": oldest}); err != nil {
				return
			}
		}
		cursor := after
		for {
			batch := s.cfg.Hist.Events(history.Filter{AfterSeq: cursor, Limit: 1000})
			if len(batch) == 0 {
				break
			}
			cursor = batch[len(batch)-1].Seq
			if err := writeV("events", cursor, batch); err != nil {
				return
			}
			if len(batch) < 1000 {
				break
			}
		}
	}
	if err := rc.Flush(); err != nil {
		return
	}
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-sub.Lagged:
			// Too slow: close; the browser reconnects with Last-Event-ID.
			return
		case <-ping.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
		case f := <-sub.C:
			if _, err := w.Write(f); err != nil {
				return
			}
			// Drain what is already queued before flushing once.
			for drained := false; !drained; {
				select {
				case f := <-sub.C:
					if _, err := w.Write(f); err != nil {
						return
					}
				default:
					drained = true
				}
			}
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}

func (s *Server) ui() http.Handler {
	if s.cfg.Dev != "" {
		u, err := url.Parse(s.cfg.Dev)
		if err == nil {
			return httputil.NewSingleHostReverseProxy(u)
		}
		s.cfg.Log.Error("bad --dev URL", "url", s.cfg.Dev, "error", err)
	}
	if s.cfg.UI == nil {
		return http.NotFoundHandler()
	}
	files := http.FileServerFS(s.cfg.UI)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if _, err := fs.Stat(s.cfg.UI, p); err != nil {
				// Client-side routes fall back to the app shell.
				r = r.Clone(r.Context())
				r.URL.Path = "/"
			} else if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
		}
		files.ServeHTTP(w, r)
	})
}

// --- actions -----------------------------------------------------------

type actionReq struct {
	Confirm    bool   `json:"confirm"`
	Node       string `json:"node"`
	Expression string `json:"expression"`
	Verbosity  string `json:"verbosity"`
	Chunk      string `json:"chunk"`
}

var consequences = map[string]string{
	"loggers":         "Changes the log verbosity of matching loggers on the node until it restarts. 'debug' and 'all' increase log volume and CPU use.",
	"status-peers":    "Makes the node query /status of every peer it knows. It shares a semaphore of 1 with /status/neighborhoods, so concurrent callers get 429.",
	"debugstore":      "Runs a full scan of the node's local store. On a large store this takes seconds and competes with normal I/O.",
	"probe-retrieval": "Asks the node to retrieve the chunk, fetching it from the network if it is not stored locally. It is a real retrieval (it is paid for and may be cached).",
}

var verbosities = map[string]bool{"none": true, "error": true, "warning": true, "info": true, "debug": true, "all": true}

func (s *Server) action(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	consequence, known := consequences[name]
	if !known {
		httpError(w, http.StatusNotFound, "unknown action")
		return
	}
	if !s.cfg.Meta.Actions || s.cfg.Nodes == nil {
		httpError(w, http.StatusConflict, "actions are only available against a live cluster (not in "+s.cfg.Meta.Mode+" mode)")
		return
	}
	// A JSON content type forces a CORS preflight for cross-site callers,
	// which this server never approves; the Origin check covers the rest.
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		httpError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	if o := r.Header.Get("Origin"); o != "" {
		if u, err := url.Parse(o); err != nil || u.Host != r.Host {
			httpError(w, http.StatusForbidden, "cross-origin action refused")
			return
		}
	}
	var req actionReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	if err := dec.Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "bad JSON body: "+err.Error())
		return
	}
	if !req.Confirm {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "confirmation required", "consequence": consequence, "code": 400})
		return
	}
	cl, ok := s.cfg.Nodes.Client(req.Node)
	if !ok {
		httpError(w, http.StatusNotFound, "unknown node "+strconv.Quote(req.Node))
		return
	}
	var (
		method = http.MethodGet
		path   string
		hdr    http.Header
		trace  string
		detail string
	)
	switch name {
	case "loggers":
		if req.Expression == "" || !verbosities[req.Verbosity] {
			httpError(w, http.StatusBadRequest, "need expression and verbosity (none|error|warning|info|debug|all)")
			return
		}
		method = http.MethodPut
		path = "/loggers/" + base64URL(req.Expression) + "/" + req.Verbosity
		detail = fmt.Sprintf("set loggers %q to %s", req.Expression, req.Verbosity)
	case "status-peers":
		path, detail = "/status/peers", "GET /status/peers"
	case "debugstore":
		path, detail = "/debugstore", "GET /debugstore"
	case "probe-retrieval":
		c := strings.ToLower(req.Chunk)
		if len(c) != 64 || !isHex(c) {
			httpError(w, http.StatusBadRequest, "chunk must be a 64-character hex address")
			return
		}
		trace = randHex(16)
		hdr = http.Header{"Traceparent": {"00-" + trace + "-" + randHex(8) + "-01"}}
		path, detail = "/chunks/"+c, "probe retrieval of "+c[:8]+"… (trace "+trace+")"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	start := time.Now()
	code, body, err := cl.Request(ctx, method, path, hdr)
	ms := float64(time.Since(start).Microseconds()) / 1000
	sev := model.SevInfo
	if err != nil || code >= 400 {
		sev = model.SevWarn
	}
	_ = s.cfg.Engine.Record(r.Context(), model.Event{Node: req.Node, Kind: "action." + name, Severity: sev, Provenance: model.Observed,
		Source: model.Source{Adapter: "bee-scope", Ref: "action " + name}, Summary: fmt.Sprintf("user action: %s → %d (%.0f ms)", detail, code, ms)})
	resp := map[string]any{"ok": err == nil && code < 400, "status": code, "ms": ms}
	if err != nil {
		resp["error"] = err.Error()
	}
	if name == "probe-retrieval" {
		resp["bytes"] = len(body)
		resp["traceId"] = trace
		if code >= 400 {
			resp["body"] = bodyValue(body)
		}
	} else {
		resp["body"] = bodyValue(body)
	}
	writeJSON(w, resp)
}

func bodyValue(b []byte) any {
	const max = 1 << 20
	if len(b) > max {
		return string(b[:max]) + "\n… truncated"
	}
	if json.Valid(b) {
		return json.RawMessage(b)
	}
	return string(b)
}
