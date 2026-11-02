package web

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"copper_control_plane/internal/jobs"
	"copper_control_plane/internal/store"
)

type apiActionResult struct {
	OK       bool           `json:"ok"`
	ServerID int64          `json:"server_id"`
	Name     string         `json:"name"`
	Error    string         `json:"error,omitempty"`
	Kind     string         `json:"kind,omitempty"`
	Result   map[string]any `json:"result,omitempty"`
}

func (s *Server) writeAPIJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleAPIMediaFiles(w http.ResponseWriter, _ *http.Request) {
	cat, err := s.agent.MediaFiles()
	if err != nil {
		s.writeAPIJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.writeAPIJSON(w, http.StatusOK, map[string]any{"ok": true, "catalog": cat})
}

func (s *Server) handleAPIInventory(w http.ResponseWriter, r *http.Request) {
	s.enqueueRedfishAPI(w, r, jobs.TypeRedfishInventory, "inventory", nil)
}

func (s *Server) handleAPIProbe(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		s.writeAPIJSON(w, http.StatusNotFound, apiActionResult{OK: false, Error: err.Error()})
		return
	}
	out := apiActionResult{ServerID: srv.ID, Name: srv.Name, Kind: "probe"}
	if strings.TrimSpace(srv.Endpoint) == "" {
		out.Error = "no BMC endpoint yet — set an IP (Edit) before Probe"
		s.writeAPIJSON(w, http.StatusConflict, out)
		return
	}
	job, err := s.enqueueJob(jobs.EnqueueOpts{
		Type: jobs.TypeRedfishProbe, ServerID: srv.ID, ServerName: srv.Name,
		Timeout: s.actionTimeout("probe"),
	})
	if err != nil {
		out.Error = err.Error()
		s.writeAPIJSON(w, http.StatusServiceUnavailable, out)
		return
	}
	s.writeAPIJSON(w, http.StatusAccepted, map[string]any{
		"ok": true, "job_id": job.ID, "server_id": srv.ID, "name": srv.Name, "kind": "probe",
	})
}

func (s *Server) handleAPIDelete(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		s.writeAPIJSON(w, http.StatusNotFound, apiActionResult{OK: false, Error: err.Error()})
		return
	}
	out := apiActionResult{ServerID: srv.ID, Name: srv.Name, Kind: "delete"}
	if err := s.store.DeleteServer(srv.ID); err != nil {
		out.Error = err.Error()
		s.writeAPIJSON(w, http.StatusInternalServerError, out)
		return
	}
	out.OK = true
	s.writeAPIJSON(w, http.StatusOK, out)
}

func (s *Server) handleAPIReset(w http.ResponseWriter, r *http.Request) {
	resetType := "ForceRestart"
	kind := "reboot"
	if r.Body != nil {
		var body struct {
			ResetType string `json:"reset_type"`
		}
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&body); err == nil {
			if t := normalizeResetType(body.ResetType); t != "" {
				resetType = t
			}
		} else if err != io.EOF {
			// Empty body is fine (legacy reboot); reject only real JSON errors.
			if _, ok := err.(*json.SyntaxError); ok || strings.Contains(err.Error(), "invalid") {
				s.writeAPIJSON(w, http.StatusBadRequest, apiActionResult{OK: false, Error: "invalid JSON: " + err.Error()})
				return
			}
		}
	}
	switch resetType {
	case "On", "ForceOn":
		kind = "power-on"
	case "ForceOff", "GracefulShutdown":
		kind = "power-off"
	default:
		kind = "reboot"
	}
	s.enqueueRedfishAPI(w, r, jobs.TypeRedfishReset, kind, resetJobPayload{ResetType: resetType})
}

// normalizeResetType allowlists Redfish ComputerSystem.Reset types used by Copper.
func normalizeResetType(raw string) string {
	switch strings.TrimSpace(raw) {
	case "ForceRestart", "GracefulRestart", "On", "ForceOn", "ForceOff", "GracefulShutdown", "PushPowerButton", "Nmi":
		return strings.TrimSpace(raw)
	default:
		return ""
	}
}

func (s *Server) handleAPIMount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ImageURL string `json:"image_url"`
		BootOnce *bool  `json:"boot_once"`
		Reboot   *bool  `json:"reboot"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeAPIJSON(w, http.StatusBadRequest, apiActionResult{OK: false, Error: "invalid JSON: " + err.Error()})
		return
	}
	imageURL := strings.TrimSpace(body.ImageURL)
	if imageURL == "" {
		s.writeAPIJSON(w, http.StatusBadRequest, apiActionResult{OK: false, Error: "image_url is required"})
		return
	}
	bootOnce := true
	if body.BootOnce != nil {
		bootOnce = *body.BootOnce
	}
	reboot := true
	if body.Reboot != nil {
		reboot = *body.Reboot
	}
	s.enqueueRedfishAPI(w, r, jobs.TypeRedfishMount, "mount", mountJobPayload{
		ImageURL: imageURL, BootOnce: bootOnce, Reboot: reboot,
	})
}

func (s *Server) handleAPIConvertUEFI(w http.ResponseWriter, r *http.Request) {
	reboot := true
	if r.Body != nil {
		var body struct {
			Reboot *bool `json:"reboot"`
		}
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&body); err == nil {
			if body.Reboot != nil {
				reboot = *body.Reboot
			}
		} else if err != io.EOF {
			if _, ok := err.(*json.SyntaxError); ok || strings.Contains(err.Error(), "invalid") {
				s.writeAPIJSON(w, http.StatusBadRequest, apiActionResult{OK: false, Error: "invalid JSON: " + err.Error()})
				return
			}
		}
	}
	rb := reboot
	s.enqueueRedfishAPI(w, r, jobs.TypeRedfishConvertUEFI, "convert-uefi", convertUEFIJobPayload{Reboot: &rb})
}

func (s *Server) enqueueRedfishAPI(w http.ResponseWriter, r *http.Request, typ, timeoutKind string, payload any) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		s.writeAPIJSON(w, http.StatusNotFound, apiActionResult{OK: false, Error: err.Error()})
		return
	}
	out := apiActionResult{ServerID: srv.ID, Name: srv.Name, Kind: timeoutKind}
	if typ != jobs.TypeRedfishProbe {
		if srv.Status == store.StatusDeclared {
			out.Error = "server is still declared — Probe (or wait for OOB DHCP bind) before Redfish actions"
			s.writeAPIJSON(w, http.StatusConflict, out)
			return
		}
		if strings.TrimSpace(srv.Endpoint) == "" {
			out.Error = "no BMC endpoint yet"
			s.writeAPIJSON(w, http.StatusConflict, out)
			return
		}
	}
	job, err := s.enqueueJob(jobs.EnqueueOpts{
		Type: typ, ServerID: srv.ID, ServerName: srv.Name,
		Timeout: s.actionTimeout(timeoutKind), Payload: payload,
	})
	if err != nil {
		out.Error = err.Error()
		s.writeAPIJSON(w, http.StatusServiceUnavailable, out)
		return
	}
	s.writeAPIJSON(w, http.StatusAccepted, map[string]any{
		"ok": true, "job_id": job.ID, "server_id": srv.ID, "name": srv.Name, "kind": timeoutKind,
	})
}

// handleAPIInventoryZIP packs cached inventory snapshots for the given ids into a zip.
// GET /api/servers/inventory.zip?ids=1,2,3
func (s *Server) handleAPIInventoryZIP(w http.ResponseWriter, r *http.Request) {
	ids, err := parseIDList(r.URL.Query().Get("ids"))
	if err != nil || len(ids) == 0 {
		http.Error(w, "ids query required (comma-separated server ids)", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q",
		fmt.Sprintf("inventories-%s.zip", time.Now().UTC().Format("20060102-150405"))))
	w.Header().Set("Cache-Control", "no-store")

	zw := zip.NewWriter(w)
	defer zw.Close()

	var missing []string
	for _, id := range ids {
		srv, err := s.store.GetServer(id)
		if err != nil || srv == nil {
			missing = append(missing, fmt.Sprintf("%d", id))
			continue
		}
		snap, err := s.store.GetSnapshot(id, "inventory")
		if err != nil || snap == nil {
			missing = append(missing, srv.Name)
			continue
		}
		name := sanitizeFilename(srv.Name)
		if name == "" {
			name = fmt.Sprintf("server-%d", id)
		}
		fw, err := zw.Create(name + "-inventory.json")
		if err != nil {
			continue
		}
		_, _ = fw.Write([]byte(snap.Payload))
		if !strings.HasSuffix(snap.Payload, "\n") {
			_, _ = fw.Write([]byte("\n"))
		}
	}
	if len(missing) > 0 {
		fw, err := zw.Create("_missing.txt")
		if err == nil {
			_, _ = fw.Write([]byte("No cached inventory for:\n" + strings.Join(missing, "\n") + "\n"))
		}
	}
}

func parseIDList(raw string) ([]int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty")
	}
	parts := strings.Split(raw, ",")
	var out []int64
	seen := map[int64]bool{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("invalid id %q", p)
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out, nil
}
