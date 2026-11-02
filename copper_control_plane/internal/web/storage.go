package web

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"copper_control_plane/internal/jobs"
	"copper_control_plane/internal/storage"
	"copper_control_plane/internal/store"

	"copper_dhcp"
)

// dataIPForServer resolves the live-OS address from Copper Data DHCP leases only.
// Prefer admin lease bindings, then inventory host NICs. Cached Detect / layout
// DataIP must never override what we leased.
func (s *Server) dataIPForServer(srv *store.Server) (ip string, mac string, err error) {
	if srv == nil {
		return "", "", fmt.Errorf("nil server")
	}
	leases, err := s.dataDHCPLeases()
	if err != nil {
		return "", "", err
	}
	leaseByMAC := leaseIPsByMAC(leases)
	if len(leaseByMAC) == 0 {
		return "", "", fmt.Errorf("no Data DHCP leases — is the live OS up and Data DHCP running?")
	}

	if bindings, berr := s.store.GetDataLeaseBindings(); berr == nil {
		for m, b := range bindings {
			if b.ServerID != srv.ID {
				continue
			}
			key := strings.ToLower(m)
			if lip := leaseByMAC[key]; lip != "" {
				return lip, m, nil
			}
		}
	}

	snap, err := s.store.GetSnapshot(srv.ID, "inventory")
	if err != nil {
		return "", "", err
	}
	if snap == nil || strings.TrimSpace(snap.Payload) == "" {
		return "", "", fmt.Errorf("no inventory yet — run Inventory so host NIC MACs are known")
	}
	_, nics := parseInventoryHostNICs(snap.Payload)
	ip, mac = matchLeaseToInventoryNICs(nics, leaseByMAC)
	if ip == "" {
		return "", "", fmt.Errorf("no Data DHCP lease matches this server's host NICs — mount/boot live OS first")
	}
	return ip, mac, nil
}

// dataIPsByServerID maps each baremetal id to its live Data DHCP lease IP (if any).
// Used on the Servers list so admins can confirm which data IP a task will hit.
func (s *Server) dataIPsByServerID() map[int64]string {
	out := map[int64]string{}
	leases, err := s.dataDHCPLeases()
	if err != nil {
		return out
	}
	leaseByMAC := leaseIPsByMAC(leases)
	if len(leaseByMAC) == 0 {
		return out
	}
	if bindings, berr := s.store.GetDataLeaseBindings(); berr == nil {
		for m, b := range bindings {
			if b.ServerID <= 0 {
				continue
			}
			if ip := leaseByMAC[strings.ToLower(m)]; ip != "" {
				out[b.ServerID] = ip
			}
		}
	}
	servers, err := s.store.ListServers()
	if err != nil {
		return out
	}
	for _, srv := range servers {
		if _, ok := out[srv.ID]; ok {
			continue
		}
		snap, err := s.store.GetSnapshot(srv.ID, "inventory")
		if err != nil || snap == nil || strings.TrimSpace(snap.Payload) == "" {
			continue
		}
		_, nics := parseInventoryHostNICs(snap.Payload)
		if ip, _ := matchLeaseToInventoryNICs(nics, leaseByMAC); ip != "" {
			out[srv.ID] = ip
		}
	}
	return out
}

// leaseIPsByMAC indexes active Data DHCP leases by normalized MAC.
func leaseIPsByMAC(leases []copper_dhcp.Lease) map[string]string {
	out := map[string]string{}
	for _, l := range leases {
		m, nerr := normalizeMACInput(l.MAC)
		if nerr != nil || m == "" || l.IP == nil {
			continue
		}
		out[strings.ToLower(m)] = l.IP.String()
	}
	return out
}

// matchLeaseToInventoryNICs returns the first inventory NIC MAC that has a live lease.
func matchLeaseToInventoryNICs(nics []rawInvNIC, leaseByMAC map[string]string) (ip, mac string) {
	for _, n := range nics {
		m, nerr := normalizeMACInput(n.mac)
		if nerr != nil || m == "" {
			continue
		}
		key := strings.ToLower(m)
		if ip := leaseByMAC[key]; ip != "" {
			return ip, m
		}
	}
	return "", ""
}

func (s *Server) liveHost(srv *store.Server) (storage.Host, error) {
	creds, err := s.store.GetLiveOSSSH()
	if err != nil {
		return storage.Host{}, err
	}
	sc := storage.Creds{Username: creds.Username, Password: creds.Password, Port: creds.Port}
	if err := sc.Validate(); err != nil {
		return storage.Host{}, err
	}
	ip, _, err := s.dataIPForServer(srv)
	if err != nil {
		return storage.Host{}, err
	}
	return storage.Host{IP: ip, Creds: sc}, nil
}

func (s *Server) loadDetect(serverID int64) (storage.DetectResult, error) {
	snap, err := s.store.GetSnapshot(serverID, storage.SnapshotKind)
	if err != nil {
		return storage.DetectResult{}, err
	}
	if snap == nil || strings.TrimSpace(snap.Payload) == "" {
		return storage.DetectResult{}, nil
	}
	var d storage.DetectResult
	if err := json.Unmarshal([]byte(snap.Payload), &d); err != nil {
		return storage.DetectResult{}, err
	}
	return d, nil
}

func (s *Server) saveDetect(serverID int64, d storage.DetectResult) error {
	raw, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return s.store.SaveSnapshot(serverID, storage.SnapshotKind, string(raw))
}

func (s *Server) detectTimeout() time.Duration {
	rt, err := s.store.GetBatchRuntime()
	if err != nil {
		return time.Duration(store.DefaultTimeoutDetectSeconds) * time.Second
	}
	sec := rt.Timeouts.Detect
	if sec < 15 {
		sec = store.DefaultTimeoutDetectSeconds
	}
	return time.Duration(sec) * time.Second
}

func (s *Server) handleAPIDetectRAID(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		s.writeAPIJSON(w, http.StatusNotFound, apiActionResult{OK: false, Error: err.Error()})
		return
	}
	out := apiActionResult{ServerID: srv.ID, Name: srv.Name, Kind: "detect-raid"}
	if _, err := s.liveHost(srv); err != nil {
		out.Error = err.Error()
		_ = s.saveDetect(srv.ID, storage.DetectResult{
			OK: false, Family: storage.FamilyUnknown, Error: err.Error(), DetectedAt: time.Now().UTC(),
		})
		s.writeAPIJSON(w, http.StatusConflict, out)
		return
	}
	job, err := s.enqueueJob(jobs.EnqueueOpts{
		Type: jobs.TypeStorageDetect, ServerID: srv.ID, ServerName: srv.Name,
		Timeout: s.actionTimeout("detect"),
	})
	if err != nil {
		out.Error = err.Error()
		s.writeAPIJSON(w, http.StatusServiceUnavailable, out)
		return
	}
	out.OK = true
	s.writeAPIJSON(w, http.StatusAccepted, map[string]any{
		"ok": true, "job_id": job.ID, "server_id": srv.ID, "name": srv.Name, "kind": "detect-raid",
	})
}

func (s *Server) driverForServer(srv *store.Server) (storage.Driver, storage.Host, storage.DetectResult, error) {
	host, err := s.liveHost(srv)
	if err != nil {
		return nil, host, storage.DetectResult{}, err
	}
	d, err := s.loadDetect(srv.ID)
	if err != nil {
		return nil, host, d, err
	}
	if d.Family == "" || (!d.OK && d.Family == storage.FamilyUnknown) {
		return nil, host, d, fmt.Errorf("RAID not detected yet — run Detect RAID first")
	}
	drv, err := storage.ResolveDriver(d)
	if err != nil {
		return nil, host, d, err
	}
	return drv, host, d, nil
}

func (s *Server) handleAPIStorageConfig(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		s.writeAPIJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if _, _, _, err := s.driverForServer(srv); err != nil {
		s.writeAPIJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	job, err := s.enqueueJob(jobs.EnqueueOpts{
		Type: jobs.TypeStorageConfig, ServerID: srv.ID, ServerName: srv.Name,
		Timeout: s.actionTimeout("detect"),
	})
	if err != nil {
		s.writeAPIJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.writeAPIJSON(w, http.StatusAccepted, map[string]any{
		"ok": true, "job_id": job.ID, "server_id": srv.ID, "name": srv.Name,
	})
}

func (s *Server) handleAPIHostPartitions(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		s.writeAPIJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	host, err := s.liveHost(srv)
	if err != nil {
		s.writeAPIJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	var scan storage.PartitionScan
	err = s.withServerLock(r.Context(), srv.ID, func(ctx context.Context) error {
		cctx, cancel := context.WithTimeout(ctx, s.detectTimeout())
		defer cancel()
		var serr error
		scan, serr = storage.ListDiskPartitions(cctx, host)
		return serr
	})
	if err != nil {
		s.writeAPIJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.writeAPIJSON(w, http.StatusOK, map[string]any{"ok": true, "scan": scan})
}

func (s *Server) handleAPIStorageWipePartitions(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		s.writeAPIJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if _, err := s.liveHost(srv); err != nil {
		s.writeAPIJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	job, err := s.enqueueJob(jobs.EnqueueOpts{
		Type: jobs.TypeStorageWipePartitions, ServerID: srv.ID, ServerName: srv.Name,
		Timeout: s.actionTimeout("detect"),
	})
	if err != nil {
		s.writeAPIJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.writeAPIJSON(w, http.StatusAccepted, map[string]any{
		"ok": true, "job_id": job.ID, "server_id": srv.ID, "name": srv.Name,
	})
}

func (s *Server) handleAPIStorageWipePartitionsBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad json"})
		return
	}
	if len(req.IDs) == 0 {
		s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "ids required"})
		return
	}
	type started struct {
		ServerID   int64  `json:"server_id"`
		ServerName string `json:"server_name"`
		JobID      string `json:"job_id,omitempty"`
		Error      string `json:"error,omitempty"`
	}
	var out []started
	for _, id := range req.IDs {
		row := started{ServerID: id}
		srv, err := s.store.GetServer(id)
		if err != nil {
			row.Error = err.Error()
			out = append(out, row)
			continue
		}
		row.ServerName = srv.Name
		if _, err := s.liveHost(srv); err != nil {
			row.Error = err.Error()
			out = append(out, row)
			continue
		}
		job, err := s.enqueueJob(jobs.EnqueueOpts{
			Type: jobs.TypeStorageWipePartitions, ServerID: srv.ID, ServerName: srv.Name,
			Timeout: s.actionTimeout("detect"),
		})
		if err != nil {
			row.Error = err.Error()
			out = append(out, row)
			continue
		}
		row.JobID = job.ID
		out = append(out, row)
	}
	s.writeAPIJSON(w, http.StatusAccepted, map[string]any{"ok": true, "jobs": out})
}

func (s *Server) handleAPIStorageExec(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		s.writeAPIJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	var req struct {
		Args []string `json:"args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad json"})
		return
	}
	drv, _, _, err := s.driverForServer(srv)
	if err != nil {
		s.writeAPIJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !drv.Allowed(req.Args) {
		s.writeAPIJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "command not allowed: " + strings.Join(req.Args, " ")})
		return
	}
	job, err := s.enqueueJob(jobs.EnqueueOpts{
		Type: jobs.TypeStorageExec, ServerID: srv.ID, ServerName: srv.Name,
		Timeout: s.actionTimeout("detect"),
		Payload: storageExecPayload{Args: req.Args},
	})
	if err != nil {
		s.writeAPIJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.writeAPIJSON(w, http.StatusAccepted, map[string]any{
		"ok": true, "job_id": job.ID, "server_id": srv.ID, "name": srv.Name,
	})
}

// handleAPIStorageExecBatch enqueues the same allowlisted command on many servers.
func (s *Server) handleAPIStorageExecBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs  []int64  `json:"ids"`
		Args []string `json:"args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad json"})
		return
	}
	if len(req.IDs) == 0 {
		s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "ids required"})
		return
	}
	type started struct {
		ServerID   int64  `json:"server_id"`
		ServerName string `json:"server_name"`
		JobID      string `json:"job_id,omitempty"`
		Error      string `json:"error,omitempty"`
	}
	var out []started
	for _, id := range req.IDs {
		row := started{ServerID: id}
		srv, err := s.store.GetServer(id)
		if err != nil {
			row.Error = err.Error()
			out = append(out, row)
			continue
		}
		row.ServerName = srv.Name
		drv, _, _, err := s.driverForServer(srv)
		if err != nil {
			row.Error = err.Error()
			out = append(out, row)
			continue
		}
		if !drv.Allowed(req.Args) {
			row.Error = "command not allowed"
			out = append(out, row)
			continue
		}
		job, err := s.enqueueJob(jobs.EnqueueOpts{
			Type: jobs.TypeStorageExec, ServerID: srv.ID, ServerName: srv.Name,
			Timeout: s.actionTimeout("detect"),
			Payload: storageExecPayload{Args: req.Args},
		})
		if err != nil {
			row.Error = err.Error()
			out = append(out, row)
			continue
		}
		row.JobID = job.ID
		out = append(out, row)
	}
	s.writeAPIJSON(w, http.StatusAccepted, map[string]any{"ok": true, "jobs": out})
}

func (s *Server) handleStoragePage(w http.ResponseWriter, r *http.Request) {
	ids, err := parseStorageIDs(r)
	if err != nil || len(ids) == 0 {
		http.Error(w, "need server id(s)", http.StatusBadRequest)
		return
	}
	type srvMeta struct {
		ID      int64    `json:"id"`
		Name    string   `json:"name"`
		Family  string   `json:"family"`
		Tool    string   `json:"tool"`
		Model   string   `json:"model"`
		DataIP  string   `json:"data_ip"`
		OK      bool     `json:"ok"`
		Labels  []string `json:"labels,omitempty"`
	}
	metas := make([]srvMeta, 0, len(ids))
	for _, id := range ids {
		srv, err := s.store.GetServer(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		d, _ := s.loadDetect(id)
		// Live Data DHCP only — never the Detect snapshot's DataIP.
		ip := ""
		if hip, _, e := s.dataIPForServer(srv); e == nil {
			ip = hip
		}
		metas = append(metas, srvMeta{
			ID: srv.ID, Name: srv.Name, Family: string(d.Family), Tool: d.Tool,
			Model: d.Model, DataIP: ip, Labels: srv.Labels,
			OK: d.OK && d.Family != "" && d.Family != storage.FamilyUnknown && d.Family != storage.FamilyNone,
		})
	}
	idsJSON, _ := json.Marshal(ids)
	metaJSON, _ := json.Marshal(metas)
	primary := metas[0]
	s.render(w, "storage.html", pageData{
		Title:       "Storage · " + primary.Name,
		Authed:      true,
		ServerIDs:   template.JS(idsJSON),
		StorageMeta: template.JS(metaJSON),
	})
}

func parseStorageIDs(r *http.Request) ([]int64, error) {
	if idStr := r.PathValue("id"); idStr != "" {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			return nil, err
		}
		return []int64{id}, nil
	}
	q := strings.TrimSpace(r.URL.Query().Get("ids"))
	if q == "" {
		return nil, fmt.Errorf("missing ids")
	}
	var ids []int64
	for _, p := range strings.Split(q, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}
