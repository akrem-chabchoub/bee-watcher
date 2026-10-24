package anomaly

import (
	"sort"
	"time"

	"beescope/pkg/history"
	"beescope/pkg/model"
)

// Tracker gives findings a lifecycle: an issue opens on the first finding,
// stays open while findings repeat, and resolves after a quiet period.
type Tracker struct {
	open      map[string]*model.Issue
	resolved  []model.Issue
	quiet     time.Duration
	keep      time.Duration
	simulated bool
}

// NewTracker returns a tracker. Issues resolve after quiet without a
// matching finding and stay listed as resolved for keep.
func NewTracker(quiet, keep time.Duration, simulated bool) *Tracker {
	return &Tracker{open: map[string]*model.Issue{}, quiet: quiet, keep: keep, simulated: simulated}
}

// Transition reports an issue state change.
type Transition struct {
	Issue  model.Issue
	Opened bool
}

// Update evaluates all rules against in.
func (t *Tracker) Update(in *Input) []Transition {
	var trans []Transition
	var opened []string
	seen := map[string]bool{}
	for _, r := range Rules {
		for _, f := range r.Eval(in) {
			seen[f.Key] = true
			is, ok := t.open[f.Key]
			if !ok {
				since := f.Since
				if since.IsZero() || since.After(in.Now) {
					since = in.Now
				}
				is = &model.Issue{ID: f.Key, Rule: f.Rule, StartedAt: since, Simulated: t.simulated}
				t.open[f.Key] = is
				opened = append(opened, f.Key)
			}
			is.Title, is.Class, is.Severity = f.Title, f.Class, f.Severity
			is.Nodes, is.Components = f.Nodes, f.Components
			is.What, is.Where, is.Evidence, is.Hypothesis, is.NextSteps = f.What, f.Where, f.Evidence, f.Hypothesis, f.NextSteps
			is.LastSeenAt = in.Now
		}
	}
	for _, key := range opened {
		trans = append(trans, Transition{Issue: *t.open[key], Opened: true})
	}
	for key, is := range t.open {
		if seen[key] || in.Now.Sub(is.LastSeenAt) < t.quiet {
			continue
		}
		now := in.Now
		is.ResolvedAt = &now
		t.resolved = append(t.resolved, *is)
		delete(t.open, key)
		trans = append(trans, Transition{Issue: *is})
	}
	cut := 0
	for cut < len(t.resolved) && in.Now.Sub(*t.resolved[cut].ResolvedAt) > t.keep {
		cut++
	}
	t.resolved = t.resolved[cut:]
	return trans
}

var classRank = map[model.IssueClass]int{model.Confirmed: 0, model.Suspicious: 1, model.Hypothesis: 2}

// Issues returns open issues (most severe first) followed by recently
// resolved ones.
func (t *Tracker) Issues() []model.Issue {
	out := make([]model.Issue, 0, len(t.open)+len(t.resolved))
	for _, is := range t.open {
		out = append(out, *is)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if history.SevRank(a.Severity) != history.SevRank(b.Severity) {
			return history.SevRank(a.Severity) > history.SevRank(b.Severity)
		}
		if classRank[a.Class] != classRank[b.Class] {
			return classRank[a.Class] < classRank[b.Class]
		}
		return a.StartedAt.Before(b.StartedAt)
	})
	for i := len(t.resolved) - 1; i >= 0; i-- {
		out = append(out, t.resolved[i])
	}
	return out
}
