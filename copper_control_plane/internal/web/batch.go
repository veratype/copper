package web

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	s.runAPIAction(w, r, "inventory", true, func(srv *store.Server) (json.RawMessage, error) {
		return s.agent.Inventory(srv)
	})
}

func (s *Server) handleAPIReset(w http.ResponseWriter, r *http.Request) {
	s.runAPIAction(w, r, "reset", false, func(srv *store.Server) (json.RawMessage, error) {
		return s.agent.Reset(srv, map[string]string{"reset_type": "ForceRestart"})
	})
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
	payload := map[string]any{
		"image_url": imageURL,
		"boot_once": bootOnce,
		"reboot":    reboot,
	}
	s.runAPIAction(w, r, "mount", false, func(srv *store.Server) (json.RawMessage, error) {
		return s.agent.MountMedia(srv, payload)
	})
}

func (s *Server) runAPIAction(w http.ResponseWriter, r *http.Request, kind string, persist bool, fn func(*store.Server) (json.RawMessage, error)) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		s.writeAPIJSON(w, http.StatusNotFound, apiActionResult{OK: false, Error: err.Error()})
		return
	}
	out := apiActionResult{ServerID: srv.ID, Name: srv.Name, Kind: kind}
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

	raw, err := fn(srv)
	if err != nil {
		out.Error = err.Error()
		s.writeAPIJSON(w, http.StatusBadGateway, out)
		return
	}

	formatted := string(raw)
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err == nil {
		if pretty, err := json.MarshalIndent(parsed, "", "  "); err == nil {
			formatted = string(pretty)
		}
	}
	if persist {
		if err := s.store.SaveSnapshot(srv.ID, kind, formatted); err != nil {
			out.Error = "ok but failed to cache: " + err.Error()
			s.writeAPIJSON(w, http.StatusInternalServerError, out)
			return
		}
	}

	out.OK = true
	if m, ok := parsed.(map[string]any); ok {
		out.Result = m
	}
	s.writeAPIJSON(w, http.StatusOK, out)
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
