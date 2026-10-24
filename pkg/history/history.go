// Package history keeps a bounded, in-memory window of recent events,
// snapshot keyframes, per-edge flows, raw payloads and log lines, so that
// transient failures can be investigated after they happened.
package history

import (
	"sort"
	"strings"
	"sync"
	"time"

	"beescope/pkg/model"
)

// Limits bound memory use.
type Limits struct {
	MaxAge        time.Duration
	MaxEvents     int
	MaxKeyframes  int
	MaxRaw        int
	MaxLogs       int
	MaxFlowFrames int
}

// DefaultLimits keep roughly 30 minutes of a 10-20 node cluster.
func DefaultLimits() Limits {
	return Limits{
		MaxAge:        30 * time.Minute,
		MaxEvents:     200_000,
		MaxKeyframes:  400,
		MaxRaw:        4_000,
		MaxLogs:       60_000,
		MaxFlowFrames: 4_000,
	}
}

// Keyframe is a serialized snapshot at one instant.
type Keyframe struct {
	At   time.Time
	JSON []byte
}

// LogRecord is a stored log line.
type LogRecord struct {
	ID        uint64        `json:"id"`
	Node      string        `json:"node"`
	Component string        `json:"component,omitempty"`
	Line      model.LogLine `json:"line"`
}

// FlowFrame is the per-edge activity observed in one interval.
type FlowFrame struct {
	At    time.Time    `json:"at"`
	Flows []model.Flow `json:"flows"`
}

// ring is a fixed-capacity FIFO.
type ring[T any] struct {
	buf   []T
	start int
	n     int
}

func newRing[T any](capacity int) *ring[T] { return &ring[T]{buf: make([]T, capacity)} }

func (r *ring[T]) push(v T) (evicted bool) {
	if len(r.buf) == 0 {
		return true
	}
	if r.n < len(r.buf) {
		r.buf[(r.start+r.n)%len(r.buf)] = v
		r.n++
		return false
	}
	r.buf[r.start] = v
	r.start = (r.start + 1) % len(r.buf)
	return true
}

func (r *ring[T]) at(i int) *T { return &r.buf[(r.start+i)%len(r.buf)] }
func (r *ring[T]) len() int    { return r.n }
func (r *ring[T]) dropFront()  { r.start = (r.start + 1) % len(r.buf); r.n-- }

// Store is safe for concurrent use: one writer (the engine), many readers.
type Store struct {
	lim    Limits
	mu     sync.RWMutex
	events *ring[model.Event]
	keys   *ring[Keyframe]
	flows  *ring[FlowFrame]
	logs   *ring[LogRecord]
	logSeq uint64
	raw    map[uint64][]byte
	rawIDs *ring[uint64]
	rawSeq uint64
}

// New returns an empty store.
func New(lim Limits) *Store {
	return &Store{
		lim:    lim,
		events: newRing[model.Event](lim.MaxEvents),
		keys:   newRing[Keyframe](lim.MaxKeyframes),
		flows:  newRing[FlowFrame](lim.MaxFlowFrames),
		logs:   newRing[LogRecord](lim.MaxLogs),
		raw:    map[uint64][]byte{},
		rawIDs: newRing[uint64](lim.MaxRaw),
	}
}

// AddEvent stores an event. Events must arrive in Seq order.
func (s *Store) AddEvent(e model.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events.push(e)
	cutoff := e.IngestedAt.Add(-s.lim.MaxAge)
	for s.events.len() > 0 && s.events.at(0).IngestedAt.Before(cutoff) {
		s.events.dropFront()
	}
}

// AddKeyframe stores a serialized snapshot.
func (s *Store) AddKeyframe(at time.Time, js []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys.push(Keyframe{At: at, JSON: js})
	cutoff := at.Add(-s.lim.MaxAge)
	for s.keys.len() > 1 && s.keys.at(0).At.Before(cutoff) {
		s.keys.dropFront()
	}
}

// AddFlows stores a flow frame.
func (s *Store) AddFlows(f FlowFrame) {
	if len(f.Flows) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flows.push(f)
}

// AddLog stores a log line and returns its id.
func (s *Store) AddLog(node, comp string, l model.LogLine) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logSeq++
	s.logs.push(LogRecord{ID: s.logSeq, Node: node, Component: comp, Line: l})
	return s.logSeq
}

// AddRaw stores a raw payload and returns its reference.
func (s *Store) AddRaw(b []byte) uint64 {
	if len(b) == 0 {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rawSeq++
	if s.rawIDs.len() == len(s.rawIDs.buf) {
		delete(s.raw, *s.rawIDs.at(0))
	}
	s.rawIDs.push(s.rawSeq)
	s.raw[s.rawSeq] = b
	return s.rawSeq
}

// Raw returns a stored payload.
func (s *Store) Raw(id uint64) ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.raw[id]
	return b, ok
}

// Filter selects events.
type Filter struct {
	From, To   time.Time
	AfterSeq   uint64
	BeforeSeq  uint64
	Nodes      map[string]bool
	Components map[string]bool
	Kinds      []string // prefix match
	MinSev     model.Severity
	Prov       map[model.Provenance]bool
	Query      string
	Corr       string // matches any correlation id or peer
	Limit      int
	Newest     bool // return the newest Limit matches instead of the oldest
}

var sevRank = map[model.Severity]int{model.SevDebug: 0, model.SevInfo: 1, model.SevWarn: 2, model.SevError: 3}

// SevRank orders severities.
func SevRank(s model.Severity) int { return sevRank[s] }

func (f *Filter) match(e *model.Event) bool {
	if f.AfterSeq > 0 && e.Seq <= f.AfterSeq {
		return false
	}
	if f.BeforeSeq > 0 && e.Seq >= f.BeforeSeq {
		return false
	}
	if !f.From.IsZero() && e.ObservedAt.Before(f.From) {
		return false
	}
	if !f.To.IsZero() && e.ObservedAt.After(f.To) {
		return false
	}
	if len(f.Nodes) > 0 && !f.Nodes[e.Node] && !(e.Corr != nil && f.Nodes[e.Corr.PeerNode]) {
		return false
	}
	if len(f.Components) > 0 && !f.Components[e.Component] {
		return false
	}
	if len(f.Kinds) > 0 {
		ok := false
		for _, k := range f.Kinds {
			if strings.HasPrefix(e.Kind, k) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if f.MinSev != "" && sevRank[e.Severity] < sevRank[f.MinSev] {
		return false
	}
	if len(f.Prov) > 0 && !f.Prov[e.Provenance] {
		return false
	}
	if f.Corr != "" {
		c := e.Corr
		if c == nil || !(c.TraceID == f.Corr || c.Chunk == f.Corr || c.Peer == f.Corr || c.BatchID == f.Corr || c.PeerNode == f.Corr) {
			return false
		}
	}
	if f.Query != "" {
		q := strings.ToLower(f.Query)
		if !strings.Contains(strings.ToLower(e.Summary), q) && !strings.Contains(e.Kind, q) && !strings.Contains(strings.ToLower(e.Node), q) {
			hit := false
			if e.Corr != nil {
				hit = strings.HasPrefix(e.Corr.Chunk, q) || strings.HasPrefix(e.Corr.Peer, q) || strings.HasPrefix(e.Corr.TraceID, q) || strings.HasPrefix(e.Corr.BatchID, q)
			}
			for _, v := range e.Attrs {
				if hit {
					break
				}
				hit = strings.Contains(strings.ToLower(v), q)
			}
			if !hit {
				return false
			}
		}
	}
	return true
}

// Events returns matching events in Seq order.
func (s *Store) Events(f Filter) []model.Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	limit := f.Limit
	if limit <= 0 || limit > 20_000 {
		limit = 20_000
	}
	var out []model.Event
	if f.Newest {
		for i := s.events.len() - 1; i >= 0 && len(out) < limit; i-- {
			if e := s.events.at(i); f.match(e) {
				out = append(out, *e)
			}
		}
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
		return out
	}
	start := 0
	if f.AfterSeq > 0 && s.events.len() > 0 {
		first := s.events.at(0).Seq
		if f.AfterSeq >= first {
			start = int(f.AfterSeq - first + 1)
		}
	}
	for i := start; i < s.events.len() && len(out) < limit; i++ {
		if e := s.events.at(i); f.match(e) {
			out = append(out, *e)
		}
	}
	return out
}

// Event returns one event by sequence number.
func (s *Store) Event(seq uint64) (model.Event, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.events.len() == 0 {
		return model.Event{}, false
	}
	first := s.events.at(0).Seq
	if seq < first || seq-first >= uint64(s.events.len()) {
		return model.Event{}, false
	}
	return *s.events.at(int(seq - first)), true
}

// OldestSeq returns the oldest retained event sequence (0 if empty).
func (s *Store) OldestSeq() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.events.len() == 0 {
		return 0
	}
	return s.events.at(0).Seq
}

// Bucket is one time slice of the density track.
type Bucket struct {
	T     time.Time      `json:"t"`
	Count map[string]int `json:"count"` // by severity
}

// Density counts events per severity in n buckets over [from,to].
func (s *Store) Density(from, to time.Time, n int, f Filter) []Bucket {
	if n <= 0 || n > 2000 || !to.After(from) {
		return nil
	}
	out := make([]Bucket, n)
	step := to.Sub(from) / time.Duration(n)
	for i := range out {
		out[i] = Bucket{T: from.Add(time.Duration(i) * step), Count: map[string]int{}}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := 0; i < s.events.len(); i++ {
		e := s.events.at(i)
		if e.ObservedAt.Before(from) || !e.ObservedAt.Before(to) || !f.match(e) {
			continue
		}
		b := int(e.ObservedAt.Sub(from) / step)
		if b >= 0 && b < n {
			out[b].Count[string(e.Severity)]++
		}
	}
	return out
}

// KeyframeAt returns the newest keyframe at or before t.
func (s *Store) KeyframeAt(t time.Time) (Keyframe, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := s.keys.len()
	i := sort.Search(n, func(i int) bool { return s.keys.at(i).At.After(t) })
	if i == 0 {
		if n > 0 {
			return *s.keys.at(0), true
		}
		return Keyframe{}, false
	}
	return *s.keys.at(i - 1), true
}

// Keyframes returns keyframe times in [from,to].
func (s *Store) Keyframes(from, to time.Time) []Keyframe {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Keyframe
	for i := 0; i < s.keys.len(); i++ {
		k := s.keys.at(i)
		if (from.IsZero() || !k.At.Before(from)) && (to.IsZero() || !k.At.After(to)) {
			out = append(out, *k)
		}
	}
	return out
}

// Window returns the time range covered by stored keyframes.
func (s *Store) Window() (time.Time, time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.keys.len() == 0 {
		return time.Time{}, time.Time{}
	}
	return s.keys.at(0).At, s.keys.at(s.keys.len() - 1).At
}

// Flows returns flow frames in [from,to].
func (s *Store) Flows(from, to time.Time) []FlowFrame {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []FlowFrame
	for i := 0; i < s.flows.len(); i++ {
		f := s.flows.at(i)
		if !f.At.Before(from) && !f.At.After(to) {
			out = append(out, *f)
		}
	}
	return out
}

// LogQuery selects log lines.
type LogQuery struct {
	Node      string
	Component string
	Query     string
	MinLevel  string
	From, To  time.Time
	Limit     int
}

var levelRank = map[string]int{"debug": 0, "info": 1, "warning": 2, "error": 3}

// Logs returns the newest matching log lines, oldest first.
func (s *Store) Logs(q LogQuery) []LogRecord {
	limit := q.Limit
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	ql := strings.ToLower(q.Query)
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []LogRecord
	for i := s.logs.len() - 1; i >= 0 && len(out) < limit; i-- {
		r := s.logs.at(i)
		if q.Node != "" && r.Node != q.Node {
			continue
		}
		if q.Component != "" && r.Component != q.Component {
			continue
		}
		if q.MinLevel != "" && levelRank[r.Line.Level] < levelRank[q.MinLevel] {
			continue
		}
		if !q.From.IsZero() && r.Line.Time.Before(q.From) {
			continue
		}
		if !q.To.IsZero() && r.Line.Time.After(q.To) {
			continue
		}
		if ql != "" && !strings.Contains(strings.ToLower(r.Line.Raw), ql) {
			continue
		}
		out = append(out, *r)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// Stats reports occupancy.
func (s *Store) Stats() (events int, oldest time.Time, keyframes int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.events.len() > 0 {
		oldest = s.events.at(0).ObservedAt
	}
	return s.events.len(), oldest, s.keys.len()
}
