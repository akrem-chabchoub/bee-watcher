package logs

import (
	"strings"
	"testing"
)

// FuzzParse checks that untrusted container output never panics the parser
// and that it never reads past MaxLine.
func FuzzParse(f *testing.F) {
	for _, s := range []string{
		`"time"="2026-10-23 10:00:00.000000" "level"="debug" "logger"="node/pushsync" "v"=1 "msg"="chunk pushed" "peer_address"="ab"`,
		`"k"={"a":[1,2,{"b":"c\"d"}]} "msg"="x"`,
		`"k"=[ "msg"="x"`,
		`"\x00"="\"" "level"=`,
		`2026-10-23T10:00:00Z "level"="info" "msg"="m"`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		l, ok := Parse(s)
		if len(l.Raw) > MaxLine {
			t.Fatalf("raw longer than MaxLine: %d", len(l.Raw))
		}
		if ok && l.Msg == "" && l.Level == "" {
			t.Fatal("ok without msg or level")
		}
		_ = Classify(&l)
		_, rest := SplitKubeTimestamp(s)
		if !strings.HasSuffix(s, rest) {
			t.Fatal("SplitKubeTimestamp returned a non-suffix")
		}
	})
}
