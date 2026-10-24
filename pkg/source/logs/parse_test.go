package logs

import (
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	line := `"time"="2026-10-23 10:00:00.123456" "level"="debug" "logger"="node/pushsync" "v"=1 "caller"=" pkg/pushsync/pushsync.go:412" "msg"="chunk pushed" "chunk_address"="aa" "peer_address"="bb" "count"=3 "ok"=true "list"=["a","b"] "obj"={"k":"v","n":{"x":1}}`
	l, ok := Parse(line)
	if !ok {
		t.Fatal("not parsed")
	}
	want := time.Date(2026, 10, 23, 10, 0, 0, 123456000, time.UTC)
	if !l.Time.Equal(want) || l.Level != "debug" || l.Logger != "node/pushsync" || l.V != 1 || l.Msg != "chunk pushed" {
		t.Fatalf("header fields: %+v", l)
	}
	if l.Caller != "pkg/pushsync/pushsync.go:412" {
		t.Errorf("caller %q", l.Caller)
	}
	for k, v := range map[string]string{"chunk_address": "aa", "peer_address": "bb", "count": "3", "ok": "true", "list": `["a","b"]`, "obj": `{"k":"v","n":{"x":1}}`} {
		if l.Fields[k] != v {
			t.Errorf("field %s = %q, want %q", k, l.Fields[k], v)
		}
	}
}

func TestParseEscapesAndError(t *testing.T) {
	l, ok := Parse(`"time"="2026-10-23 10:00:00.000000" "level"="error" "logger"="node/api" "msg"="say \"hi\"" "error"="dial tcp: refused"`)
	if !ok || l.Msg != `say "hi"` || l.Error != "dial tcp: refused" {
		t.Fatalf("got %+v ok=%v", l, ok)
	}
}

func TestParseRejectsOtherFormats(t *testing.T) {
	for _, s := range []string{"", "panic: runtime error", "goroutine 1 [running]:", `"unterminated`, `"k"`} {
		if _, ok := Parse(s); ok {
			t.Errorf("Parse(%q) ok, want not ok", s)
		}
	}
}

func TestParseTruncatesLongLines(t *testing.T) {
	long := `"level"="info" "msg"="` + string(make([]byte, MaxLine)) + `"`
	l, _ := Parse(long)
	if !l.Partial || len(l.Raw) != MaxLine {
		t.Fatalf("partial=%v len=%d", l.Partial, len(l.Raw))
	}
}

func TestSplitKubeTimestamp(t *testing.T) {
	ts, rest := SplitKubeTimestamp(`2026-10-23T10:00:00.5Z "level"="info"`)
	if ts.IsZero() || rest != `"level"="info"` {
		t.Fatalf("ts=%v rest=%q", ts, rest)
	}
	if ts, rest := SplitKubeTimestamp(`"level"="info"`); !ts.IsZero() || rest != `"level"="info"` {
		t.Fatalf("no timestamp: ts=%v rest=%q", ts, rest)
	}
}

func TestClassify(t *testing.T) {
	peer := "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
	cases := []struct {
		line      string
		kind      string
		comp      string
		flowProto string
		inbound   bool
	}{
		{`"level"="debug" "logger"="node/kademlia" "msg"="connected to peer" "peer_address"="` + peer + `"`, "peer.connected", "kademlia", "", false},
		{`"level"="debug" "logger"="node/pushsync" "msg"="chunk pushed" "peer_address"="` + peer + `"`, "", "pushsync", "pushsync", false},
		{`"level"="debug" "logger"="node/retrieval" "msg"="failed to get chunk" "peer_address"="` + peer + `"`, "retrieval.peer_failed", "retrieval", "retrieval", true},
		{`"level"="error" "logger"="node/storer" "msg"="boom"`, "log.error", "reserve", "", false},
		{`"level"="debug" "logger"="node/hive" "msg"="noise"`, "", "hive", "", false},
		{`"level"="warning" "logger"="node/pushsync" "msg"="no peers left"`, "pushsync.no_peers_left", "pushsync", "", false},
	}
	for _, c := range cases {
		l, ok := Parse(c.line)
		if !ok {
			t.Fatalf("parse %q", c.line)
		}
		cl := Classify(&l)
		if cl.Kind != c.kind || cl.Component != c.comp {
			t.Errorf("%s: kind=%q comp=%q, want %q %q", l.Msg, cl.Kind, cl.Component, c.kind, c.comp)
		}
		if (cl.Flow != nil) != (c.flowProto != "") {
			t.Errorf("%s: flow=%+v", l.Msg, cl.Flow)
		} else if cl.Flow != nil && (cl.Flow.Protocol != c.flowProto || cl.Flow.Inbound != c.inbound || cl.Flow.Peer != peer) {
			t.Errorf("%s: flow=%+v", l.Msg, cl.Flow)
		}
	}
}
