package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"beescope/pkg/history"
)

func TestControlDisabledOutsideLive(t *testing.T) {
	e := newEnv(t, 10, Meta{Mode: "sim"}, nil)
	resp, err := http.Get(e.srv.URL + "/api/control/status")
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if resp.StatusCode != 200 || st["enabled"] != false || st["disabledReason"] == "" {
		t.Fatalf("status in sim mode: %d %v", resp.StatusCode, st)
	}
	if code, _ := post(t, e.srv.URL+"/api/control/actions/cluster-delete", "application/json", "", `{"confirm":true}`); code != http.StatusConflict {
		t.Fatalf("control action in sim mode: %d", code)
	}
	if code, _ := post(t, e.srv.URL+"/api/data/bee-1/stamps", "application/json", "", `{"confirm":true,"amount":"1","depth":20}`); code != http.StatusConflict {
		t.Fatalf("data action in sim mode: %d", code)
	}
}

func TestDataEndpoints(t *testing.T) {
	type call struct {
		method, path, query string
		hdr                 http.Header
		body                string
	}
	var calls []call
	bee := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, call{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), string(b)})
		switch {
		case r.URL.Path == "/stamps" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"stamps":[]}`))
		case r.URL.Path == "/wallet":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"kind":"Status","message":"no endpoints available for service \"bee-1:api\"","code":503}`))
		case strings.HasPrefix(r.URL.Path, "/stamps/"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"batchID":"` + strings.Repeat("b", 64) + `","txHash":"0x1"}`))
		case r.URL.Path == "/bzz" && r.Method == http.MethodPost:
			w.Header().Set("Swarm-Tag", "42")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"reference":"` + strings.Repeat("c", 64) + `"}`))
		case strings.HasPrefix(r.URL.Path, "/bzz/"):
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Disposition", `inline; filename="hello.txt"`)
			_, _ = w.Write([]byte("hello swarm"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer bee.Close()
	e := newEnv(t, 1000, Meta{Mode: "live", Actions: true}, fakeNodes{bee.URL})
	base := e.srv.URL + "/api/data/bee-1"

	get := func(path string) (int, string) {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get("/get?path=/stamps"); code != 200 || body != `{"stamps":[]}` {
		t.Fatalf("stamps passthrough: %d %s", code, body)
	}
	if code, _ := get("/get?path=/status/peers"); code != 400 {
		t.Fatalf("expensive endpoint must be refused: %d", code)
	}
	if code, body := get("/get?path=/wallet"); code != 503 || !strings.Contains(body, `"error":"no endpoints available`) {
		t.Fatalf("kube status errors become plain messages: %d %s", code, body)
	}

	if code, m := post(t, base+"/stamps", "application/json", "", `{"amount":"100","depth":20}`); code != 400 {
		t.Fatalf("unconfirmed buy: %d %v", code, m)
	}
	if code, _ := post(t, base+"/stamps", "application/json", "", `{"confirm":true,"amount":"1e9","depth":20}`); code != 400 {
		t.Fatalf("bad amount: %d", code)
	}
	if code, m := post(t, base+"/stamps", "application/json", e.srv.URL, `{"confirm":true,"amount":"100000","depth":20,"label":"test run","immutable":true}`); code != 200 || m["batchID"] == nil {
		t.Fatalf("buy: %d %v", code, m)
	}
	last := calls[len(calls)-1]
	if last.method != http.MethodPost || last.path != "/stamps/100000/20" || last.query != "label=test+run" || last.hdr.Get("Immutable") != "true" {
		t.Fatalf("bee saw %+v", last)
	}

	upload := func(hdr map[string]string, query string) (int, map[string]any) {
		req, _ := http.NewRequest(http.MethodPost, base+"/upload?"+query, strings.NewReader("hello"))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}
	batch := strings.Repeat("a", 64)
	if code, _ := upload(map[string]string{"Content-Type": "text/plain"}, "batch="+batch); code != http.StatusForbidden {
		t.Fatalf("upload without guard header: %d", code)
	}
	if code, _ := upload(map[string]string{"X-Bee-Scope": "upload", "Origin": "http://evil.example"}, "batch="+batch); code != http.StatusForbidden {
		t.Fatalf("cross-origin upload: %d", code)
	}
	if code, _ := upload(map[string]string{"X-Bee-Scope": "upload"}, "batch=xyz"); code != 400 {
		t.Fatalf("bad batch: %d", code)
	}
	code, m := upload(map[string]string{"X-Bee-Scope": "upload", "Content-Type": "text/plain"}, "batch="+batch+"&name=../../etc/hello.txt&pin=1&encrypt=0&deferred=0&redundancy=2")
	if code != 200 || m["reference"] != strings.Repeat("c", 64) || m["tagUid"] != float64(42) {
		t.Fatalf("upload: %d %v", code, m)
	}
	last = calls[len(calls)-1]
	if last.path != "/bzz" || last.query != "name=hello.txt" || last.body != "hello" ||
		last.hdr.Get("Swarm-Postage-Batch-Id") != batch || last.hdr.Get("Swarm-Pin") != "true" || last.hdr.Get("Swarm-Encrypt") != "false" ||
		last.hdr.Get("Swarm-Deferred-Upload") != "false" || last.hdr.Get("Swarm-Redundancy-Level") != "2" || last.hdr.Get("Content-Type") != "text/plain" {
		t.Fatalf("bee saw %+v", last)
	}
	resp, err := http.Get(e.srv.URL + "/api/data/uploads")
	if err != nil {
		t.Fatal(err)
	}
	var ups []Upload
	_ = json.NewDecoder(resp.Body).Decode(&ups)
	resp.Body.Close()
	if len(ups) != 1 || ups[0].Name != "hello.txt" || ups[0].Size != 5 || ups[0].TagUID != 42 {
		t.Fatalf("uploads: %+v", ups)
	}

	resp, err = http.Get(base + "/download/" + strings.Repeat("c", 64) + "?path=docs/a%20b.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "hello swarm" || !strings.Contains(resp.Header.Get("Content-Disposition"), `attachment; filename=hello.txt`) {
		t.Fatalf("download: %q %q", b, resp.Header.Get("Content-Disposition"))
	}
	if last = calls[len(calls)-1]; last.path != "/bzz/"+strings.Repeat("c", 64)+"/docs/a b.txt" {
		t.Fatalf("download path %q", last.path)
	}
	if code, _ := get("/download/" + strings.Repeat("c", 64) + "?path=../x"); code != 400 {
		t.Fatalf("path traversal: %d", code)
	}
	if code, _ := get("/download/nothex"); code != 400 {
		t.Fatalf("bad reference: %d", code)
	}

	if evs := e.hist.Events(history.Filter{Kinds: []string{"action.data"}}); len(evs) < 3 {
		t.Fatalf("data actions must be on the timeline, got %d", len(evs))
	}
}
