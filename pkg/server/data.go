package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"beescope/pkg/model"
	"beescope/pkg/source/beeapi"
)

// Data tools: stamps, upload, download (docs/CONTROL-API.md).

// Upload is one file uploaded through bee-scope in this session.
type Upload struct {
	ID          string    `json:"id"`
	Node        string    `json:"node"`
	Name        string    `json:"name"`
	Size        int64     `json:"size"`
	ContentType string    `json:"contentType"`
	Batch       string    `json:"batch"`
	Reference   string    `json:"reference"`
	TagUID      uint64    `json:"tagUid"`
	At          time.Time `json:"at"`
	Encrypt     bool      `json:"encrypt"`
	Pin         bool      `json:"pin"`
}

type uploads struct {
	mu   sync.Mutex
	seq  int
	list []Upload // newest last
}

func (u *uploads) add(up Upload) Upload {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.seq++
	up.ID = "u" + strconv.Itoa(u.seq)
	u.list = append(u.list, up)
	if len(u.list) > 200 {
		u.list = u.list[len(u.list)-200:]
	}
	return up
}

func (u *uploads) all() []Upload {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make([]Upload, 0, len(u.list))
	for i := len(u.list) - 1; i >= 0; i-- {
		out = append(out, u.list[i])
	}
	return out
}

var (
	readPaths = regexp.MustCompile(`^/(stamps(/[0-9a-f]{64}(/buckets)?)?|wallet|addresses|chainstate|chequebook/balance|tags(/[0-9]{1,19})?|pins|readiness|health)$`)
	batchRe   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	refRe     = regexp.MustCompile(`^[0-9a-f]{64}([0-9a-f]{64})?$`)
	amountRe  = regexp.MustCompile(`^[1-9][0-9]{0,40}$`)
	labelRe   = regexp.MustCompile(`^[\pL\pN _.:-]{0,64}$`)
)

// dataClient resolves the node, or answers with an error.
func (s *Server) dataClient(w http.ResponseWriter, r *http.Request) (*beeapi.Client, string, bool) {
	if s.cfg.Nodes == nil || !s.cfg.Meta.Actions {
		httpError(w, http.StatusConflict, "data tools are only available in live mode (this bee-scope runs in "+s.cfg.Meta.Mode+" mode)")
		return nil, "", false
	}
	node := r.PathValue("node")
	cl, ok := s.cfg.Nodes.Client(node)
	if !ok {
		httpError(w, http.StatusNotFound, "unknown node "+strconv.Quote(node))
		return nil, "", false
	}
	return cl, node, true
}

func (s *Server) dataGet(w http.ResponseWriter, r *http.Request) {
	cl, _, ok := s.dataClient(w, r)
	if !ok {
		return
	}
	p := r.URL.Query().Get("path")
	if !readPaths.MatchString(p) {
		httpError(w, http.StatusBadRequest, "path not allowed: "+p)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	code, body, err := cl.Request(ctx, http.MethodGet, p, nil)
	if err != nil {
		httpError(w, http.StatusBadGateway, err.Error())
		return
	}
	if code >= 400 {
		// Bee errors are {"message": …}; the kube proxy answers with a
		// Status object when the node has no ready pod.
		httpError(w, code, beeMessage(body))
		return
	}
	writeRaw(w, body)
}

// beeCall performs a state-changing call and relays Bee's answer.
func (s *Server) beeCall(w http.ResponseWriter, r *http.Request, cl *beeapi.Client, node, method, p string, hdr http.Header, timeout time.Duration, what string) {
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	start := time.Now()
	code, body, err := cl.Request(ctx, method, p, hdr)
	sev := model.SevInfo
	summary := fmt.Sprintf("user action: %s → %d (%s)", what, code, time.Since(start).Round(time.Millisecond))
	if err != nil {
		sev, summary = model.SevWarn, fmt.Sprintf("user action: %s failed: %v", what, err)
	} else if code >= 400 {
		sev = model.SevWarn
	}
	_ = s.cfg.Engine.Record(context.WithoutCancel(r.Context()), model.Event{Node: node, Kind: "action.data", Severity: sev, Provenance: model.Observed,
		Source: model.Source{Adapter: "bee-scope", Ref: method + " " + p}, Summary: summary})
	if err != nil {
		httpError(w, http.StatusBadGateway, err.Error())
		return
	}
	if code >= 400 {
		httpError(w, code, beeMessage(body))
		return
	}
	writeRaw(w, body)
}

// beeMessage extracts Bee's {"message": …} error text.
func beeMessage(b []byte) string {
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &m) == nil && m.Message != "" {
		return m.Message
	}
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

func (s *Server) dataBuyStamp(w http.ResponseWriter, r *http.Request) {
	cl, node, ok := s.dataClient(w, r)
	if !ok {
		return
	}
	var req struct {
		Amount    string `json:"amount"`
		Depth     int    `json:"depth"`
		Label     string `json:"label"`
		Immutable bool   `json:"immutable"`
	}
	if !decodeConfirmed(w, r, &req) {
		return
	}
	if !amountRe.MatchString(req.Amount) || req.Depth < 17 || req.Depth > 40 || !labelRe.MatchString(req.Label) {
		httpError(w, http.StatusBadRequest, "need a positive integer amount, depth 17–40 and a short label")
		return
	}
	p := "/stamps/" + req.Amount + "/" + strconv.Itoa(req.Depth)
	if req.Label != "" {
		p += "?label=" + url.QueryEscape(req.Label)
	}
	hdr := http.Header{}
	if req.Immutable {
		hdr.Set("Immutable", "true")
	}
	s.beeCall(w, r, cl, node, http.MethodPost, p, hdr, 5*time.Minute, fmt.Sprintf("buy stamp depth %d amount %s", req.Depth, req.Amount))
}

func (s *Server) dataTopup(w http.ResponseWriter, r *http.Request) {
	cl, node, ok := s.dataClient(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	var req struct {
		Amount string `json:"amount"`
	}
	if !decodeConfirmed(w, r, &req) {
		return
	}
	if !batchRe.MatchString(id) || !amountRe.MatchString(req.Amount) {
		httpError(w, http.StatusBadRequest, "need a batch id and a positive integer amount")
		return
	}
	s.beeCall(w, r, cl, node, http.MethodPatch, "/stamps/topup/"+id+"/"+req.Amount, nil, 5*time.Minute, "top up batch "+id[:8]+"… by "+req.Amount)
}

func (s *Server) dataDilute(w http.ResponseWriter, r *http.Request) {
	cl, node, ok := s.dataClient(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	var req struct {
		Depth int `json:"depth"`
	}
	if !decodeConfirmed(w, r, &req) {
		return
	}
	if !batchRe.MatchString(id) || req.Depth < 18 || req.Depth > 41 {
		httpError(w, http.StatusBadRequest, "need a batch id and a depth 18–41")
		return
	}
	s.beeCall(w, r, cl, node, http.MethodPatch, "/stamps/dilute/"+id+"/"+strconv.Itoa(req.Depth), nil, 5*time.Minute, fmt.Sprintf("dilute batch %s… to depth %d", id[:8], req.Depth))
}

const maxUpload = 4 << 30

func (s *Server) dataUpload(w http.ResponseWriter, r *http.Request) {
	cl, node, ok := s.dataClient(w, r)
	if !ok {
		return
	}
	// A custom header cannot be sent cross-site without a preflight,
	// which this server never approves; it replaces the JSON rule here.
	if r.Header.Get("X-Bee-Scope") != "upload" {
		httpError(w, http.StatusForbidden, "missing X-Bee-Scope: upload header")
		return
	}
	if !mutationOK(w, r, false) {
		return
	}
	q := r.URL.Query()
	batch := strings.ToLower(q.Get("batch"))
	if !batchRe.MatchString(batch) {
		httpError(w, http.StatusBadRequest, "choose a postage batch (64 hex characters)")
		return
	}
	name := path.Base(strings.ReplaceAll(q.Get("name"), "\\", "/"))
	if name == "." || name == "/" {
		name = "file"
	}
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}
	flag := func(k string) bool { v := q.Get(k); return v == "1" || v == "true" }
	redundancy, _ := strconv.Atoi(q.Get("redundancy"))
	if redundancy < 0 || redundancy > 4 {
		httpError(w, http.StatusBadRequest, "redundancy must be 0–4")
		return
	}
	hdr := http.Header{
		"Content-Type":           {ct},
		"Swarm-Postage-Batch-Id": {batch},
		"Swarm-Pin":              {strconv.FormatBool(flag("pin"))},
		"Swarm-Encrypt":          {strconv.FormatBool(flag("encrypt"))},
		"Swarm-Deferred-Upload":  {strconv.FormatBool(q.Get("deferred") != "0")},
	}
	if redundancy > 0 {
		hdr.Set("Swarm-Redundancy-Level", strconv.Itoa(redundancy))
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	start := time.Now()
	resp, err := cl.Do(ctx, http.MethodPost, "/bzz?name="+url.QueryEscape(name), hdr, http.MaxBytesReader(w, r.Body, maxUpload), r.ContentLength)
	if err != nil {
		s.recordData(r, node, model.SevWarn, "upload "+name, fmt.Sprintf("user action: upload %s failed: %v", name, err))
		httpError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		msg := beeMessage(body)
		s.recordData(r, node, model.SevWarn, "upload "+name, fmt.Sprintf("user action: upload %s → %d %s", name, resp.StatusCode, msg))
		httpError(w, resp.StatusCode, msg)
		return
	}
	var out struct {
		Reference string `json:"reference"`
	}
	_ = json.Unmarshal(body, &out)
	tag, _ := strconv.ParseUint(resp.Header.Get("Swarm-Tag"), 10, 64)
	up := s.uploads.add(Upload{Node: node, Name: name, Size: r.ContentLength, ContentType: ct, Batch: batch, Reference: out.Reference,
		TagUID: tag, At: time.Now(), Encrypt: flag("encrypt"), Pin: flag("pin")})
	s.recordData(r, node, model.SevInfo, "upload "+name, fmt.Sprintf("user action: uploaded %s (%s) → %s… tag %d in %s", name, humanBytes(r.ContentLength), short(out.Reference), tag, time.Since(start).Round(time.Millisecond)))
	writeJSON(w, map[string]any{"reference": out.Reference, "tagUid": tag, "upload": up})
}

func (s *Server) recordData(r *http.Request, node string, sev model.Severity, ref, summary string) {
	_ = s.cfg.Engine.Record(context.WithoutCancel(r.Context()), model.Event{Node: node, Kind: "action.data", Severity: sev, Provenance: model.Observed,
		Source: model.Source{Adapter: "bee-scope", Ref: ref}, Summary: summary})
}

func humanBytes(n int64) string {
	switch {
	case n < 0:
		return "? B"
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	case n < 1<<30:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.2f GiB", float64(n)/(1<<30))
}

func (s *Server) dataUploads(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.uploads.all())
}

func (s *Server) dataDownload(w http.ResponseWriter, r *http.Request) {
	cl, node, ok := s.dataClient(w, r)
	if !ok {
		return
	}
	ref := strings.ToLower(r.PathValue("ref"))
	if !refRe.MatchString(ref) {
		httpError(w, http.StatusBadRequest, "reference must be 64 or 128 hex characters")
		return
	}
	p, err := bzzPath(ref, r.URL.Query().Get("path"))
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	resp, err := cl.Do(ctx, http.MethodGet, p, nil, nil, -1)
	if err != nil {
		httpError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		s.recordData(r, node, model.SevWarn, "download "+short(ref), fmt.Sprintf("user action: download %s… → %d %s", short(ref), resp.StatusCode, beeMessage(body)))
		httpError(w, resp.StatusCode, beeMessage(body))
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil {
			name = params["filename"]
		}
	}
	if name == "" {
		name = short(ref)
	}
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	for _, k := range []string{"Content-Type", "Content-Length"} {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	n, _ := io.Copy(w, resp.Body)
	s.recordData(r, node, model.SevInfo, "download "+short(ref), fmt.Sprintf("user action: downloaded %s… (%s)", short(ref), humanBytes(n)))
}

// dataProbe retrieves a reference through one node and discards the
// content, to answer "can this node get it?" without a browser download.
func (s *Server) dataProbe(w http.ResponseWriter, r *http.Request) {
	cl, node, ok := s.dataClient(w, r)
	if !ok {
		return
	}
	ref := strings.ToLower(r.PathValue("ref"))
	if !refRe.MatchString(ref) {
		httpError(w, http.StatusBadRequest, "reference must be 64 or 128 hex characters")
		return
	}
	p, err := bzzPath(ref, r.URL.Query().Get("path"))
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	start := time.Now()
	out := map[string]any{"node": node, "reference": ref}
	resp, err := cl.Do(ctx, http.MethodGet, p, nil, nil, -1)
	if err != nil {
		out["ok"], out["error"], out["ms"] = false, err.Error(), time.Since(start).Milliseconds()
		writeJSON(w, out)
		return
	}
	defer resp.Body.Close()
	out["status"] = resp.StatusCode
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		out["ok"], out["error"], out["ms"] = false, beeMessage(body), time.Since(start).Milliseconds()
	} else {
		n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<30))
		out["bytes"], out["ms"] = n, time.Since(start).Milliseconds()
		out["ok"] = err == nil
		if err != nil {
			out["error"] = err.Error()
		}
	}
	sev, verdict := model.SevInfo, "retrieved"
	if out["ok"] != true {
		sev, verdict = model.SevWarn, fmt.Sprintf("could not retrieve (%v)", out["error"])
	}
	s.recordData(r, node, sev, "probe "+short(ref), fmt.Sprintf("user check: %s %s %s… in %d ms", node, verdict, short(ref), out["ms"]))
	writeJSON(w, out)
}

// bzzPath builds /bzz/<ref>/<escaped path>, refusing traversal.
func bzzPath(ref, sub string) (string, error) {
	p := "/bzz/" + ref + "/"
	if sub = strings.Trim(sub, "/"); sub != "" {
		var segs []string
		for _, seg := range strings.Split(sub, "/") {
			if seg == "" || seg == "." || seg == ".." {
				return "", fmt.Errorf("invalid path")
			}
			segs = append(segs, url.PathEscape(seg))
		}
		p += strings.Join(segs, "/")
	}
	return p, nil
}
