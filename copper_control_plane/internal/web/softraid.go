package web

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"copper_control_plane/internal/jobs"
	"copper_control_plane/internal/softraid"
)

func (s *Server) loadSoftRAID(serverID int64) (softraid.Topology, error) {
	snap, err := s.store.GetSnapshot(serverID, softraid.SnapshotKind)
	if err != nil {
		return softraid.Topology{}, err
	}
	if snap == nil || strings.TrimSpace(snap.Payload) == "" {
		return softraid.Topology{}, nil
	}
	var top softraid.Topology
	if err := json.Unmarshal([]byte(snap.Payload), &top); err != nil {
		return softraid.Topology{}, err
	}
	return top, nil
}

func (s *Server) saveSoftRAID(serverID int64, top softraid.Topology) error {
	raw, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	return s.store.SaveSnapshot(serverID, softraid.SnapshotKind, string(raw))
}

func (s *Server) handleSoftRAIDPage(w http.ResponseWriter, r *http.Request) {
	ids, err := parseStorageIDs(r)
	if err != nil || len(ids) == 0 {
		// Main-menu /softraid with no selection: open all inventory servers.
		servers, lerr := s.store.ListServers()
		if lerr != nil {
			http.Error(w, lerr.Error(), http.StatusInternalServerError)
			return
		}
		ids = make([]int64, 0, len(servers))
		for _, srv := range servers {
			ids = append(ids, srv.ID)
		}
		if len(ids) == 0 {
			http.Error(w, "no servers in inventory — add a server first", http.StatusBadRequest)
			return
		}
	}
	type srvMeta struct {
		ID     int64    `json:"id"`
		Name   string   `json:"name"`
		DataIP string   `json:"data_ip"`
		OK     bool     `json:"ok"`
		Tool   string   `json:"tool"`
		Labels []string `json:"labels,omitempty"`
	}
	metas := make([]srvMeta, 0, len(ids))
	for _, id := range ids {
		srv, err := s.store.GetServer(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		top, _ := s.loadSoftRAID(id)
		ip := ""
		if hip, _, e := s.dataIPForServer(srv); e == nil {
			ip = hip
		}
		metas = append(metas, srvMeta{
			ID: srv.ID, Name: srv.Name, DataIP: ip, Labels: srv.Labels,
			OK: top.OK, Tool: top.Tool,
		})
	}
	idsJSON, _ := json.Marshal(ids)
	metaJSON, _ := json.Marshal(metas)
	levelsJSON, _ := json.Marshal(softraid.SupportedLevels)
	primary := metas[0]
	s.render(w, "softraid.html", pageData{
		Title:          "Soft RAID · " + primary.Name,
		Authed:         true,
		ServerIDs:      template.JS(idsJSON),
		StorageMeta:    template.JS(metaJSON),
		SoftRAIDLevels: template.JS(levelsJSON),
	})
}

func (s *Server) handleAPISoftRAIDScan(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		s.writeAPIJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if _, err := s.liveHost(srv); err != nil {
		_ = s.saveSoftRAID(srv.ID, softraid.Topology{OK: false, Error: err.Error()})
		s.writeAPIJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	job, err := s.enqueueJob(jobs.EnqueueOpts{
		Type: jobs.TypeSoftRAIDScan, ServerID: srv.ID, ServerName: srv.Name,
		Timeout: s.actionTimeout("detect"),
	})
	if err != nil {
		s.writeAPIJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.writeAPIJSON(w, http.StatusAccepted, map[string]any{
		"ok": true, "job_id": job.ID, "server_id": srv.ID, "name": srv.Name, "kind": "softraid-scan",
	})
}

func (s *Server) handleAPISoftRAIDGet(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		s.writeAPIJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	top, err := s.loadSoftRAID(srv.ID)
	if err != nil {
		s.writeAPIJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if hip, _, e := s.dataIPForServer(srv); e == nil {
		top.DataIP = hip
	}
	s.writeAPIJSON(w, http.StatusOK, map[string]any{
		"ok": true, "topology": top, "levels": softraid.SupportedLevels, "rules": softraid.LevelRules,
	})
}

func (s *Server) handleAPISoftRAIDExec(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		s.writeAPIJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	var body struct {
		Action softraid.Action `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid JSON"})
		return
	}
	if strings.TrimSpace(body.Action.Op) == "" {
		s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "action.op required"})
		return
	}
	if _, err := softraid.ExecPreview(body.Action); err != nil {
		s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	job, err := s.enqueueJob(jobs.EnqueueOpts{
		Type: jobs.TypeSoftRAIDExec, ServerID: srv.ID, ServerName: srv.Name,
		Timeout: s.actionTimeout("detect"), Payload: softRAIDExecPayload{Action: body.Action},
	})
	if err != nil {
		s.writeAPIJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.writeAPIJSON(w, http.StatusAccepted, map[string]any{
		"ok": true, "job_id": job.ID, "server_id": srv.ID, "name": srv.Name, "kind": "softraid-exec",
	})
}

func (s *Server) handleAPISoftRAIDExecBatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ServerIDs []int64         `json:"server_ids"`
		Action    softraid.Action `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid JSON"})
		return
	}
	if len(body.ServerIDs) == 0 {
		s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "server_ids required"})
		return
	}
	if _, err := softraid.ExecPreview(body.Action); err != nil {
		s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	type started struct {
		ServerID int64  `json:"server_id"`
		Name     string `json:"name"`
		JobID    string `json:"job_id,omitempty"`
		Error    string `json:"error,omitempty"`
	}
	out := make([]started, 0, len(body.ServerIDs))
	payload := softRAIDExecPayload{Action: body.Action}
	for _, id := range body.ServerIDs {
		srv, err := s.store.GetServer(id)
		if err != nil {
			out = append(out, started{ServerID: id, Error: err.Error()})
			continue
		}
		job, err := s.enqueueJob(jobs.EnqueueOpts{
			Type: jobs.TypeSoftRAIDExec, ServerID: srv.ID, ServerName: srv.Name,
			Timeout: s.actionTimeout("detect"), Payload: payload,
		})
		if err != nil {
			out = append(out, started{ServerID: srv.ID, Name: srv.Name, Error: err.Error()})
			continue
		}
		out = append(out, started{ServerID: srv.ID, Name: srv.Name, JobID: job.ID})
	}
	s.writeAPIJSON(w, http.StatusAccepted, map[string]any{"ok": true, "jobs": out})
}

func (s *Server) handleAPISoftRAIDScanBatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ServerIDs []int64 `json:"server_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && r.ContentLength > 0 {
		s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid JSON"})
		return
	}
	if len(body.ServerIDs) == 0 {
		if q := strings.TrimSpace(r.URL.Query().Get("ids")); q != "" {
			for _, p := range strings.Split(q, ",") {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				id, err := strconv.ParseInt(p, 10, 64)
				if err != nil {
					s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
					return
				}
				body.ServerIDs = append(body.ServerIDs, id)
			}
		}
	}
	if len(body.ServerIDs) == 0 {
		s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "server_ids required"})
		return
	}
	type started struct {
		ServerID int64  `json:"server_id"`
		Name     string `json:"name"`
		JobID    string `json:"job_id,omitempty"`
		Error    string `json:"error,omitempty"`
	}
	out := make([]started, 0, len(body.ServerIDs))
	for _, id := range body.ServerIDs {
		srv, err := s.store.GetServer(id)
		if err != nil {
			out = append(out, started{ServerID: id, Error: err.Error()})
			continue
		}
		job, err := s.enqueueJob(jobs.EnqueueOpts{
			Type: jobs.TypeSoftRAIDScan, ServerID: srv.ID, ServerName: srv.Name,
			Timeout: s.actionTimeout("detect"),
		})
		if err != nil {
			out = append(out, started{ServerID: srv.ID, Name: srv.Name, Error: err.Error()})
			continue
		}
		out = append(out, started{ServerID: srv.ID, Name: srv.Name, JobID: job.ID})
	}
	s.writeAPIJSON(w, http.StatusAccepted, map[string]any{"ok": true, "jobs": out})
}
