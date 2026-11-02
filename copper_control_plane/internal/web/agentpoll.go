package web

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"copper_control_plane/internal/store"

	"copper_dhcp"
)

// Heartbeat from copper_bmc in poll mode — last-known cache source.
type agentHeartbeatRequest struct {
	OS                string                   `json:"os"`
	DHCPRole          bool                     `json:"dhcp_role"`
	DHCPRunning       bool                     `json:"dhcp_running"`
	DHCPConfigEnabled bool                     `json:"dhcp_config_enabled"`
	DHCPConfig        *copper_dhcp.Config      `json:"dhcp_config,omitempty"`
	LLDPRole          bool                     `json:"lldp_role"`
	LLDPRunning       bool                     `json:"lldp_running"`
	LLDPInitError     string                   `json:"lldp_init_error,omitempty"`
	Media             *agentHeartbeatMedia     `json:"media,omitempty"`
	Leases            []store.AgentDHCPLease   `json:"leases,omitempty"`
	Neighbors         []store.AgentLLDPNeighbor `json:"neighbors,omitempty"`
	Activity          []store.AgentActivityEvent `json:"activity,omitempty"`
	ActivityNextSince uint64                   `json:"activity_next_since,omitempty"`
	Health            map[string]any           `json:"health,omitempty"`
}

type agentHeartbeatMedia struct {
	Folder  string                 `json:"folder"`
	BaseURL string                 `json:"base_url"`
	Bind    string                 `json:"bind"`
	Iface   string                 `json:"interface"`
	Files   []store.AgentMediaFile `json:"files"`
	Note    string                 `json:"note,omitempty"`
}

func (s *Server) handleAgentPollHeartbeat(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.IsPollMode() {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "control plane is not in poll mode",
		})
		return
	}
	var req agentHeartbeatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json: " + err.Error()})
		return
	}
	now := time.Now().UTC()
	cache := store.AgentCache{
		LastSeenAt:        now,
		OS:                strings.TrimSpace(req.OS),
		DHCPRole:          req.DHCPRole,
		DHCPRunning:       req.DHCPRunning,
		DHCPConfigEnabled: req.DHCPConfigEnabled,
		DHCPConfig:        req.DHCPConfig,
		LLDPRole:          req.LLDPRole,
		LLDPRunning:       req.LLDPRunning,
		LLDPInitError:     req.LLDPInitError,
		Leases:            req.Leases,
		Neighbors:         req.Neighbors,
		Health:            req.Health,
	}
	if req.Media != nil {
		cache.MediaFolder = req.Media.Folder
		cache.MediaBaseURL = req.Media.BaseURL
		cache.MediaBind = req.Media.Bind
		cache.MediaIface = req.Media.Iface
		cache.MediaFiles = req.Media.Files
		cache.MediaNote = req.Media.Note
	}
	// Merge activity into a bounded ring kept on CP.
	prev, _ := s.store.GetAgentCache()
	var merged []store.AgentActivityEvent
	if prev != nil {
		merged = append(merged, prev.Activity...)
	}
	seen := map[uint64]bool{}
	for _, ev := range merged {
		seen[ev.ID] = true
	}
	for _, ev := range req.Activity {
		if seen[ev.ID] {
			continue
		}
		merged = append(merged, ev)
		seen[ev.ID] = true
	}
	const maxActivity = 2000
	if len(merged) > maxActivity {
		merged = merged[len(merged)-maxActivity:]
	}
	cache.Activity = merged
	cache.ActivityNextSince = req.ActivityNextSince
	if cache.ActivityNextSince == 0 {
		for _, ev := range merged {
			if ev.ID > cache.ActivityNextSince {
				cache.ActivityNextSince = ev.ID
			}
		}
	}
	if err := s.store.SaveAgentCache(cache); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"last_seen_at": now.Format(time.RFC3339),
	})
}

func (s *Server) handleAgentPollNext(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.IsPollMode() {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "control plane is not in poll mode",
		})
		return
	}
	waitSec := 25
	var body struct {
		WaitSeconds int `json:"wait_seconds"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.WaitSeconds > 0 && body.WaitSeconds <= 60 {
		waitSec = body.WaitSeconds
	}

	deadline := time.Now().Add(time.Duration(waitSec) * time.Second)
	for {
		cmd, err := s.store.ClaimNextAgentCommand(time.Now().UTC())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if cmd != nil {
			var headers map[string]string
			_ = json.Unmarshal([]byte(cmd.HeadersJSON), &headers)
			writeJSON(w, http.StatusOK, map[string]any{
				"command": map[string]any{
					"id":           cmd.ID,
					"method":       cmd.Method,
					"path":         cmd.Path,
					"headers":      headers,
					"body":         jsonRawOrNull(cmd.Body),
					"queued_at":    cmd.QueuedAt.UTC().Format(time.RFC3339Nano),
					"delivered_at": cmd.DeliveredAt.UTC().Format(time.RFC3339Nano),
					"expires_at":   cmd.ExpiresAt.UTC().Format(time.RFC3339Nano),
				},
			})
			return
		}
		if time.Now().After(deadline) {
			writeJSON(w, http.StatusOK, map[string]any{"command": nil})
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (s *Server) handleAgentPollResult(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.IsPollMode() {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "control plane is not in poll mode",
		})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing command id"})
		return
	}
	var req struct {
		StatusCode int             `json:"status_code"`
		Body       json.RawMessage `json:"body"`
		Error      string          `json:"error"`
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json: " + err.Error()})
		return
	}
	var bodyBytes []byte
	if len(req.Body) > 0 && string(req.Body) != "null" {
		bodyBytes = []byte(req.Body)
	}
	if s.agent != nil && s.agent.Poll != nil {
		if err := s.agent.Poll.DeliverResult(id, req.StatusCode, bodyBytes, req.Error); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
	} else if err := s.store.CompleteAgentCommand(id, req.StatusCode, bodyBytes, req.Error, time.Now().UTC()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func jsonRawOrNull(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return json.RawMessage(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
