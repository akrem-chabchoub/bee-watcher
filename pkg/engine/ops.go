package engine

import (
	"fmt"
	"sort"
	"time"

	"beescope/pkg/component"
	"beescope/pkg/model"
)

const (
	tagStallAfter = 2 * time.Minute
	maxTraceOps   = 400
	maxTagOps     = 400
)

type tagOp struct {
	op         model.Operation
	tag        model.Tag
	lastChange time.Time
	splitDone  *time.Time
	syncDone   *time.Time
}

type traceOp struct {
	op    model.Operation
	spans map[string]model.Span
	nodes map[string]bool
}

// ops correlates operations. Upload tags are observed directly from /tags;
// traces are observed from OTLP spans that share a trace ID.
type ops struct {
	tags   map[string]*tagOp
	traces map[string]*traceOp
	sim    bool
}

func newOps() *ops {
	return &ops{tags: map[string]*tagOp{}, traces: map[string]*traceOp{}}
}

func (o *ops) prov() model.Provenance {
	if o.sim {
		return model.Simulated
	}
	return model.Observed
}

// observeTags updates upload operations and returns event summaries for new
// and finished uploads.
func (o *ops) observeTags(node string, tags []model.Tag, at time.Time) (started, finished []model.Tag) {
	for _, t := range tags {
		id := fmt.Sprintf("tag:%s:%d", node, t.UID)
		to, ok := o.tags[id]
		if !ok {
			start := t.StartedAt
			if start.IsZero() {
				start = at
			}
			to = &tagOp{tag: t, lastChange: at, op: model.Operation{
				ID: id, Kind: "upload-tag", Node: node, Nodes: []string{node}, Start: start,
				Title: fmt.Sprintf("Upload tag %d on %s", t.UID, node), Correlation: "observed",
			}}
			o.tags[id] = to
			if at.Sub(start) < 10*time.Minute {
				started = append(started, t)
			}
		} else if t.Split != to.tag.Split || t.Synced != to.tag.Synced || t.Stored != to.tag.Stored {
			to.lastChange = at
		}
		if to.splitDone == nil && to.tag.Split > 0 && t.Split == to.tag.Split && ok {
			ts := at
			to.splitDone = &ts
		}
		wasDone := to.syncDone != nil
		if to.syncDone == nil && t.Split > 0 && t.Synced >= t.Split {
			ts := at
			to.syncDone = &ts
		}
		to.tag = t
		to.build(at, o.prov())
		if !wasDone && to.syncDone != nil && ok {
			finished = append(finished, t)
		}
	}
	o.prune(at)
	return started, finished
}

func (to *tagOp) build(now time.Time, prov model.Provenance) {
	t := to.tag
	start := to.op.Start
	splitState := "running"
	if to.splitDone != nil {
		splitState = "done"
	}
	syncState := "running"
	switch {
	case to.syncDone != nil:
		syncState = "done"
	case t.Split > 0 && now.Sub(to.lastChange) > tagStallAfter:
		syncState = "stalled"
	case t.Split == 0:
		syncState = "pending"
	}
	to.op.Stages = []model.Stage{
		{Name: "Split & stamp", Component: "api", State: splitState, Start: &start, End: to.splitDone, Count: t.Split, Detail: fmt.Sprintf("%d chunks split, %d already seen (deduplicated)", t.Split, t.Seen)},
		{Name: "Sent to peers", Component: "pusher", State: "unobservable", Detail: "Bee never increments the tag's 'sent' counter, so this stage cannot be observed."},
		{Name: "Stored by closest node", Component: "pushsync", State: stageCount(t.Stored, t.Split, syncState), Count: t.Stored, Total: t.Split, Detail: "receipts where this node was the closest (stored locally)"},
		{Name: "Synced (receipt received)", Component: "pushsync", State: syncState, Start: to.splitDone, End: to.syncDone, Count: t.Synced, Total: t.Split},
	}
	to.op.Updated = to.lastChange
	to.op.Prov = prov
	to.op.Note = fmt.Sprintf("split=%d seen=%d stored=%d synced=%d (last change %s ago)", t.Split, t.Seen, t.Stored, t.Synced, now.Sub(to.lastChange).Round(time.Second))
	switch syncState {
	case "done":
		to.op.State = "done"
	case "stalled":
		to.op.State = "stalled"
	default:
		to.op.State = "running"
	}
	if t.Address != "" && t.Address != "0000000000000000000000000000000000000000000000000000000000000000" {
		to.op.Title = fmt.Sprintf("Upload tag %d on %s (%s…)", t.UID, to.op.Node, t.Address[:min(8, len(t.Address))])
	}
}

func stageCount(n, total uint64, syncState string) string {
	if total > 0 && n >= total {
		return "done"
	}
	if syncState == "done" {
		return "done"
	}
	return "running"
}

// refresh recomputes time-based state (stall detection).
func (o *ops) refresh(now time.Time) {
	for _, to := range o.tags {
		if to.op.State == "running" {
			to.build(now, o.prov())
		}
	}
}

// observeSpan adds a span to its trace operation.
func (o *ops) observeSpan(sp *model.Span, node string) *model.Operation {
	id := "trace:" + sp.TraceID
	tr, ok := o.traces[id]
	if !ok {
		tr = &traceOp{spans: map[string]model.Span{}, nodes: map[string]bool{}, op: model.Operation{
			ID: id, Kind: "trace", Correlation: "observed", Start: sp.Start,
		}}
		o.traces[id] = tr
	}
	tr.spans[sp.SpanID] = *sp
	if node != "" {
		tr.nodes[node] = true
	}
	tr.rebuild(o.prov())
	return &tr.op
}

func (tr *traceOp) rebuild(prov model.Provenance) {
	spans := make([]model.Span, 0, len(tr.spans))
	for _, s := range tr.spans {
		spans = append(spans, s)
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].Start.Before(spans[j].Start) })
	op := &tr.op
	op.Stages = make([]model.Stage, 0, len(spans))
	op.State = "done"
	op.Start = spans[0].Start
	op.Updated = spans[0].End
	var root *model.Span
	for i := range spans {
		if _, ok := tr.spans[spans[i].ParentSpanID]; spans[i].ParentSpanID == "" || !ok {
			root = &spans[i]
			break
		}
	}
	for i := range spans {
		s := spans[i]
		st := "done"
		if s.StatusError {
			st = "failed"
			op.State = "failed"
		}
		start, end := s.Start, s.End
		if end.After(op.Updated) {
			op.Updated = end
		}
		op.Stages = append(op.Stages, model.Stage{
			Name: s.Name, Node: s.Attrs["bee.node"], Component: component.ForSpan(s.Name), State: st,
			Start: &start, End: &end, SpanID: s.SpanID, Parent: s.ParentSpanID, Attrs: s.Attrs, Detail: s.StatusMsg,
		})
	}
	op.Nodes = make([]string, 0, len(tr.nodes))
	for n := range tr.nodes {
		op.Nodes = append(op.Nodes, n)
	}
	sort.Strings(op.Nodes)
	if root != nil {
		op.Title = root.Name
		if c := root.Attrs["swarm.chunk.address"]; c != "" {
			op.Title += " " + c[:min(8, len(c))] + "…"
		}
		op.Node = root.Attrs["bee.node"]
	}
	op.Title = fmt.Sprintf("%s (%d spans, %d nodes)", op.Title, len(spans), len(op.Nodes))
	op.Prov = prov
	if root != nil && root.ParentSpanID != "" {
		op.Note = "The root span was not received (the originating node may not export traces); the tree is partial."
	} else {
		op.Note = ""
	}
}

func (o *ops) prune(now time.Time) {
	if len(o.tags) > maxTagOps {
		trimOldest(o.tags, len(o.tags)-maxTagOps, func(t *tagOp) time.Time { return t.op.Updated })
	}
	if len(o.traces) > maxTraceOps {
		trimOldest(o.traces, len(o.traces)-maxTraceOps, func(t *traceOp) time.Time { return t.op.Updated })
	}
}

func trimOldest[T any](m map[string]T, n int, at func(T) time.Time) {
	type kv struct {
		k string
		t time.Time
	}
	all := make([]kv, 0, len(m))
	for k, v := range m {
		all = append(all, kv{k, at(v)})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].t.Before(all[j].t) })
	for i := 0; i < n && i < len(all); i++ {
		delete(m, all[i].k)
	}
}

// list returns all operations, newest first.
func (o *ops) list() []model.Operation {
	out := make([]model.Operation, 0, len(o.tags)+len(o.traces))
	for _, t := range o.tags {
		out = append(out, t.op)
	}
	for _, t := range o.traces {
		out = append(out, t.op)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}

func (o *ops) get(id string) (model.Operation, bool) {
	if t, ok := o.tags[id]; ok {
		return t.op, true
	}
	if t, ok := o.traces[id]; ok {
		return t.op, true
	}
	return model.Operation{}, false
}
