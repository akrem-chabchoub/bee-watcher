package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"beescope/pkg/control"
)

// Cluster control: docs/CONTROL-API.md.

func (s *Server) controlOff(w http.ResponseWriter) bool {
	if s.cfg.Control == nil {
		httpError(w, http.StatusConflict, "cluster control is only available in live mode (this bee-scope runs in "+s.cfg.Meta.Mode+" mode)")
		return true
	}
	return false
}

// mutationOK enforces the rules for state-changing requests: same origin
// and, unless the caller uses its own guard header, a JSON body.
func mutationOK(w http.ResponseWriter, r *http.Request, needJSON bool) bool {
	if needJSON {
		// A JSON content type forces a CORS preflight for cross-site
		// callers, which this server never approves.
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			httpError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
			return false
		}
	}
	if o := r.Header.Get("Origin"); o != "" {
		if u, err := url.Parse(o); err != nil || u.Host != r.Host {
			httpError(w, http.StatusForbidden, "cross-origin request refused")
			return false
		}
	}
	return true
}

// decodeConfirmed decodes a JSON body into v and requires "confirm": true.
func decodeConfirmed(w http.ResponseWriter, r *http.Request, v any) bool {
	if !mutationOK(w, r, true) {
		return false
	}
	raw, err := readLimited(w, r, 64<<10)
	if err != nil {
		httpError(w, http.StatusBadRequest, "read body: "+err.Error())
		return false
	}
	var c struct {
		Confirm bool `json:"confirm"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		httpError(w, http.StatusBadRequest, "bad JSON body: "+err.Error())
		return false
	}
	if !c.Confirm {
		httpError(w, http.StatusBadRequest, "confirmation required (\"confirm\": true)")
		return false
	}
	if err := json.Unmarshal(raw, v); err != nil {
		httpError(w, http.StatusBadRequest, "bad JSON body: "+err.Error())
		return false
	}
	return true
}

func (s *Server) controlStatus(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Control == nil {
		writeJSON(w, map[string]any{
			"enabled":        false,
			"disabledReason": "Cluster control and data tools work in live mode. This bee-scope runs in " + s.cfg.Meta.Mode + " mode; start it without --sim / --replay.",
		})
		return
	}
	writeJSON(w, s.cfg.Control.Status(r.Context()))
}

func (s *Server) controlSettings(w http.ResponseWriter, r *http.Request) {
	if s.controlOff(w) {
		return
	}
	writeJSON(w, s.cfg.Control.Settings())
}

func (s *Server) controlPutSettings(w http.ResponseWriter, r *http.Request) {
	if s.controlOff(w) {
		return
	}
	var in control.Settings
	if !decodeConfirmed(w, r, &in) {
		return
	}
	out, err := s.cfg.Control.UpdateSettings(in)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, out)
}

func (s *Server) controlAction(w http.ResponseWriter, r *http.Request) {
	if s.controlOff(w) {
		return
	}
	var req control.ActionRequest
	if !decodeConfirmed(w, r, &req) {
		return
	}
	j, err := s.cfg.Control.Run(r.PathValue("name"), req)
	switch {
	case errors.Is(err, control.ErrBusy):
		httpError(w, http.StatusConflict, "another job is running; wait for it or cancel it")
		return
	case err != nil:
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"job": j.View()})
}

func (s *Server) controlJobs(w http.ResponseWriter, r *http.Request) {
	if s.controlOff(w) {
		return
	}
	writeJSON(w, s.cfg.Control.Jobs())
}

func (s *Server) controlJobLog(w http.ResponseWriter, r *http.Request) {
	if s.controlOff(w) {
		return
	}
	j, ok := s.cfg.Control.Job(r.PathValue("id"))
	if !ok {
		httpError(w, http.StatusNotFound, "unknown job")
		return
	}
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	lines, next := j.Log(after)
	writeJSON(w, map[string]any{"job": j.View(), "lines": lines, "next": next})
}

func (s *Server) controlJobCancel(w http.ResponseWriter, r *http.Request) {
	if s.controlOff(w) {
		return
	}
	if !mutationOK(w, r, true) {
		return
	}
	j, ok := s.cfg.Control.Job(r.PathValue("id"))
	if !ok {
		httpError(w, http.StatusNotFound, "unknown job")
		return
	}
	j.Cancel()
	writeJSON(w, map[string]any{"job": j.View()})
}

// quit stops bee-scope, for users who started it from the app bundle and
// have no terminal to press Ctrl+C in. The cluster keeps running.
func (s *Server) quit(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Quit == nil {
		httpError(w, http.StatusNotImplemented, "quit is not available")
		return
	}
	var req struct{}
	if !decodeConfirmed(w, r, &req) {
		return
	}
	writeJSON(w, map[string]any{"ok": true})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		s.cfg.Quit()
	}()
}
