package otlp

import (
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// FuzzDecode feeds untrusted bodies (as a Bee pod or anything else on the
// network could send) to the decoder; it must never panic.
func FuzzDecode(f *testing.F) {
	pb, _ := proto.Marshal(sample())
	js, _ := protojson.Marshal(sample())
	f.Add(pb, false)
	f.Add(js, true)
	f.Add([]byte("{}"), true)
	f.Add([]byte{0x0a, 0xff, 0xff, 0xff, 0x0f}, false)
	f.Fuzz(func(t *testing.T, body []byte, isJSON bool) {
		spans, err := Decode(body, isJSON)
		if err != nil {
			return
		}
		if len(spans) > maxSpans {
			t.Fatalf("%d spans exceeds the bound", len(spans))
		}
		for _, s := range spans {
			if s.TraceID == "" || s.SpanID == "" || len(s.Attrs) > maxAttrs {
				t.Fatalf("bad span %+v", s)
			}
		}
	})
}
