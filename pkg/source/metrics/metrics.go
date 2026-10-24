// Package metrics parses Bee's Prometheus /metrics text and turns counter
// pairs into rates.
package metrics

import (
	"bytes"
	"fmt"
	"strings"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	pmodel "github.com/prometheus/common/model"

	"beescope/pkg/component"
	"beescope/pkg/model"
)

// Parse decodes Prometheus text exposition into model.Metrics. Histograms and
// summaries are reduced to <name>_count and <name>_sum.
func Parse(body []byte) (model.Metrics, error) {
	p := expfmt.NewTextParser(pmodel.UTF8Validation)
	fams, err := p.TextToMetricFamilies(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse metrics: %w", err)
	}
	out := make(model.Metrics, len(fams))
	for name, f := range fams {
		for _, mt := range f.GetMetric() {
			labels := labelMap(mt.GetLabel())
			switch f.GetType() {
			case dto.MetricType_COUNTER:
				out[name] = append(out[name], model.MetricSample{Labels: labels, Value: mt.GetCounter().GetValue()})
			case dto.MetricType_GAUGE:
				out[name] = append(out[name], model.MetricSample{Labels: labels, Value: mt.GetGauge().GetValue()})
			case dto.MetricType_UNTYPED:
				out[name] = append(out[name], model.MetricSample{Labels: labels, Value: mt.GetUntyped().GetValue()})
			case dto.MetricType_HISTOGRAM, dto.MetricType_GAUGE_HISTOGRAM:
				h := mt.GetHistogram()
				out[name+"_count"] = append(out[name+"_count"], model.MetricSample{Labels: labels, Value: float64(h.GetSampleCount())})
				out[name+"_sum"] = append(out[name+"_sum"], model.MetricSample{Labels: labels, Value: h.GetSampleSum()})
			case dto.MetricType_SUMMARY:
				s := mt.GetSummary()
				out[name+"_count"] = append(out[name+"_count"], model.MetricSample{Labels: labels, Value: float64(s.GetSampleCount())})
				out[name+"_sum"] = append(out[name+"_sum"], model.MetricSample{Labels: labels, Value: s.GetSampleSum()})
			}
		}
	}
	return out, nil
}

func labelMap(lp []*dto.LabelPair) map[string]string {
	if len(lp) == 0 {
		return nil
	}
	m := make(map[string]string, len(lp))
	for _, l := range lp {
		m[l.GetName()] = l.GetValue()
	}
	return m
}

// Value sums the samples selected by ref. ok is false when the family is
// absent from the scrape (which bee-scope treats as "unobservable").
func Value(m model.Metrics, ref component.MetricRef) (float64, bool) {
	samples, ok := m[ref.Name]
	if !ok {
		return 0, false
	}
	var v float64
outer:
	for _, s := range samples {
		for k, want := range ref.Match {
			if s.Labels[k] != want {
				continue outer
			}
		}
		for k, prefix := range ref.LabelPrefix {
			if !strings.HasPrefix(s.Labels[k], prefix) {
				continue outer
			}
		}
		v += s.Value
	}
	return v, true
}

// Rate returns the per-second rate of a counter between two scrapes taken
// dt seconds apart. A counter reset (process restart) yields ok=false.
func Rate(prev, cur model.Metrics, ref component.MetricRef, dt float64) (float64, bool) {
	if prev == nil || dt <= 0 {
		return 0, false
	}
	a, ok1 := Value(prev, ref)
	b, ok2 := Value(cur, ref)
	if !ok1 || !ok2 || b < a {
		return 0, false
	}
	return (b - a) / dt, true
}
