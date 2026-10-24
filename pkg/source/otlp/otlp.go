// Package otlp is a minimal OTLP/HTTP trace receiver. Bee exports spans with
// the OpenTelemetry SDK (otlptracehttp, protobuf, gzip) when started with
// --tracing-enable; pointing --tracing-endpoint at bee-scope turns spans into
// observations without running a separate collector.
package otlp

import (
	"compress/gzip"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"beescope/pkg/model"
)

const (
	maxBody         = 8 << 20
	maxDecompressed = 32 << 20
	maxAttrs        = 32
	maxSpans        = 10_000
)

// Decode parses an ExportTraceServiceRequest (protobuf or JSON) into spans.
// Input is untrusted; sizes are bounded and malformed data returns an error.
func Decode(body []byte, jsonEncoding bool) ([]model.Span, error) {
	var req coltrace.ExportTraceServiceRequest
	var err error
	if jsonEncoding {
		err = protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(body, &req)
	} else {
		err = proto.Unmarshal(body, &req)
	}
	if err != nil {
		return nil, fmt.Errorf("decode otlp: %w", err)
	}
	var out []model.Span
	for _, rs := range req.GetResourceSpans() {
		res := attrMap(rs.GetResource().GetAttributes())
		overlay := res["service.instance.id"]
		for _, ss := range rs.GetScopeSpans() {
			for _, s := range ss.GetSpans() {
				if len(out) >= maxSpans {
					return out, nil
				}
				sp := model.Span{
					TraceID:      hex.EncodeToString(s.GetTraceId()),
					SpanID:       hex.EncodeToString(s.GetSpanId()),
					ParentSpanID: hex.EncodeToString(s.GetParentSpanId()),
					Name:         s.GetName(),
					Kind:         s.GetKind().String(),
					Start:        unixNano(s.GetStartTimeUnixNano()),
					End:          unixNano(s.GetEndTimeUnixNano()),
					Overlay:      overlay,
					Attrs:        attrMap(s.GetAttributes()),
				}
				if st := s.GetStatus(); st != nil && st.GetCode() == tracepb.Status_STATUS_CODE_ERROR {
					sp.StatusError, sp.StatusMsg = true, st.GetMessage()
				}
				for i, ev := range s.GetEvents() {
					if i >= 16 {
						break
					}
					sp.Events = append(sp.Events, model.SpanEvent{Time: unixNano(ev.GetTimeUnixNano()), Name: ev.GetName()})
				}
				if sp.TraceID == "" || sp.SpanID == "" {
					continue
				}
				out = append(out, sp)
			}
		}
	}
	return out, nil
}

func unixNano(ns uint64) time.Time {
	if ns == 0 || ns > 1<<62 {
		return time.Time{}
	}
	return time.Unix(0, int64(ns))
}

func attrMap(kvs []*commonpb.KeyValue) map[string]string {
	if len(kvs) == 0 {
		return nil
	}
	m := make(map[string]string, min(len(kvs), maxAttrs))
	for _, kv := range kvs {
		if len(m) >= maxAttrs {
			break
		}
		m[kv.GetKey()] = anyString(kv.GetValue())
	}
	return m
}

func anyString(v *commonpb.AnyValue) string {
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return x.StringValue
	case *commonpb.AnyValue_BoolValue:
		return strconv.FormatBool(x.BoolValue)
	case *commonpb.AnyValue_IntValue:
		return strconv.FormatInt(x.IntValue, 10)
	case *commonpb.AnyValue_DoubleValue:
		return strconv.FormatFloat(x.DoubleValue, 'g', -1, 64)
	case *commonpb.AnyValue_BytesValue:
		return hex.EncodeToString(x.BytesValue)
	}
	return ""
}

// Handler returns the /v1/traces handler. Spans are emitted as observations;
// when the engine is behind, the request is rejected with 503 so the
// exporter retries instead of spans being lost silently.
func Handler(out chan<- model.Observation) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var rd io.Reader = http.MaxBytesReader(w, r.Body, maxBody)
		if r.Header.Get("Content-Encoding") == "gzip" {
			gz, err := gzip.NewReader(rd)
			if err != nil {
				http.Error(w, "bad gzip", http.StatusBadRequest)
				return
			}
			defer gz.Close()
			rd = io.LimitReader(gz, maxDecompressed)
		}
		body, err := io.ReadAll(rd)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		isJSON := r.Header.Get("Content-Type") == "application/json"
		spans, err := Decode(body, isJSON)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		for i := range spans {
			sp := spans[i]
			o := model.Observation{Kind: model.ObsSpan, Source: model.Source{Adapter: "otlp", Ref: sp.Name}, ObservedAt: sp.End, Span: &sp}
			select {
			case out <- o:
			case <-ctx.Done():
				http.Error(w, "busy", http.StatusServiceUnavailable)
				return
			}
		}
		resp, _ := proto.Marshal(&coltrace.ExportTraceServiceResponse{})
		if isJSON {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{}"))
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write(resp)
	})
}
