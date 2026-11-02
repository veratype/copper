package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"copper_control_plane/internal/jobs"
	"copper_control_plane/internal/store"
)

func (s *Server) handleJobsPage(w http.ResponseWriter, _ *http.Request) {
	s.render(w, "jobs.html", pageData{Title: "Jobs", Authed: true})
}

func (s *Server) handleAPIJobGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("job_id"))
	if id == "" {
		s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "job_id required"})
		return
	}
	if s.jobs == nil {
		s.writeAPIJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "jobs not available"})
		return
	}
	job, ok := s.jobs.Get(id)
	if !ok {
		s.writeAPIJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "job not found"})
		return
	}
	s.writeAPIJSON(w, http.StatusOK, map[string]any{"ok": true, "job": job})
}

func (s *Server) handleAPIJobsList(w http.ResponseWriter, r *http.Request) {
	if s.jobs == nil {
		s.writeAPIJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "jobs not available"})
		return
	}
	f := jobs.ListFilter{Limit: 100}
	if v := strings.TrimSpace(r.URL.Query().Get("server_id")); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad server_id"})
			return
		}
		f.ServerID = id
	}
	f.Type = strings.TrimSpace(r.URL.Query().Get("type"))
	if st := strings.TrimSpace(r.URL.Query().Get("status")); st != "" {
		f.Status = jobs.Status(st)
	}
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad limit"})
			return
		}
		f.Limit = n
	}
	list := s.jobs.List(f)
	busy := map[string]string{}
	for id, holder := range s.jobs.BusyServers() {
		busy[strconv.FormatInt(id, 10)] = holder
	}
	s.writeAPIJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"jobs": list,
		"busy": busy,
	})
}

func (s *Server) handleAPIServersBusy(w http.ResponseWriter, _ *http.Request) {
	if s.jobs == nil {
		s.writeAPIJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "jobs not available"})
		return
	}
	busy := map[string]string{}
	for id, holder := range s.jobs.BusyServers() {
		busy[strconv.FormatInt(id, 10)] = holder
	}
	s.writeAPIJSON(w, http.StatusOK, map[string]any{"ok": true, "busy": busy})
}

// handleAPIProvisionJob is a compatibility alias for GET /api/provision/jobs/{job_id}.
func (s *Server) handleAPIProvisionJob(w http.ResponseWriter, r *http.Request) {
	s.handleAPIJobGet(w, r)
}

func (s *Server) actionTimeout(kind string) time.Duration {
	rt, err := s.store.GetBatchRuntime()
	if err != nil {
		rt = store.DefaultBatchRuntime()
	}
	sec := 60
	switch kind {
	case "inventory":
		sec = rt.Timeouts.Inventory
	case "reboot", "reset", "power-on", "power-off":
		sec = rt.Timeouts.Reboot
	case "mount":
		sec = rt.Timeouts.Mount
	case "convert-uefi":
		// BIOS PATCH + optional ForceRestart — same ballpark as mount/reboot.
		sec = rt.Timeouts.Mount
		if rt.Timeouts.Reboot > sec {
			sec = rt.Timeouts.Reboot
		}
	case "probe":
		sec = rt.Timeouts.Probe
	case "detect":
		sec = rt.Timeouts.Detect
	case "claim-nics":
		sec = 90
	case "provision":
		sec = rt.Timeouts.Provision
	case "preflight":
		return s.preflightTimeout()
	}
	if sec < 5 {
		sec = 60
	}
	return time.Duration(sec) * time.Second
}
