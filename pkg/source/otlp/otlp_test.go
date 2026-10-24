package otlp

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"testing"

	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"beescope/pkg/model"
)

func str(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func sample() *coltrace.ExportTraceServiceRequest {
	return &coltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{str("service.instance.id", "abcd")}},
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{
			{TraceId: bytes.Repeat([]byte{1}, 16), SpanId: bytes.Repeat([]byte{2}, 8), Name: "pushsync-handler",
				StartTimeUnixNano: 1e18, EndTimeUnixNano: 1e18 + 5e6,
				Attributes: []*commonpb.KeyValue{str("swarm.chunk.address", "cc")},
				Status:     &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR, Message: "boom"}},
			{Name: "no ids are dropped"},
		}}},
	}}}
}

func TestDecodeProtoAndJSON(t *testing.T) {
	pb, _ := proto.Marshal(sample())
	js, _ := protojson.Marshal(sample())
	for name, c := range map[string]struct {
		body []byte
		json bool
	}{"proto": {pb, false}, "json": {js, true}} {
		spans, err := Decode(c.body, c.json)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(spans) != 1 {
			t.Fatalf("%s: %d spans", name, len(spans))
		}
		s := spans[0]
		if s.TraceID != "01010101010101010101010101010101" || s.Overlay != "abcd" || !s.StatusError || s.StatusMsg != "boom" || s.Attrs["swarm.chunk.address"] != "cc" {
			t.Fatalf("%s: %+v", name, s)
		}
		if s.End.Sub(s.Start).Milliseconds() != 5 {
			t.Fatalf("%s: duration %s", name, s.End.Sub(s.Start))
		}
	}
}

func TestHandlerGzip(t *testing.T) {
	out := make(chan model.Observation, 4)
	pb, _ := proto.Marshal(sample())
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write(pb)
	_ = gz.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/traces", &buf)
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "gzip")
	rec := httptest.NewRecorder()
	Handler(out).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || len(out) != 1 {
		t.Fatalf("code %d, %d observations", rec.Code, len(out))
	}
	if o := <-out; o.Kind != model.ObsSpan || o.Span.Name != "pushsync-handler" {
		t.Fatalf("observation %+v", o)
	}
	rec = httptest.NewRecorder()
	Handler(out).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader([]byte("not protobuf \xff\xff"))))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("garbage: code %d", rec.Code)
	}
}
