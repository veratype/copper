package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"copper_control_plane/internal/jobs"
)

// handleAPIClaimNICs enqueues inventory.claim-nics for an orphan Data DHCP lease.
// Body: {"ip":"10.10.96.105","mac":"40:a6:…"} — SSH to the lease, match inventory
// server, merge NICs. ServerID is 0 until match; inventory write uses WithServer.
func (s *Server) handleAPIClaimNICs(w http.ResponseWriter, r *http.Request) {
	var body claimNICsJobPayload
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid JSON: " + err.Error()})
		return
	}
	ip := strings.TrimSpace(body.IP)
	mac := strings.TrimSpace(body.MAC)
	if ip == "" {
		s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "ip is required"})
		return
	}
	if mac != "" {
		if n, err := normalizeMACInput(mac); err == nil {
			mac = n
		}
	}
	if s.jobs == nil {
		s.writeAPIJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "jobs not available"})
		return
	}
	job, err := s.enqueueJob(jobs.EnqueueOpts{
		Type:       jobs.TypeInventoryClaimNICs,
		ServerID:   0,
		ServerName: "orphan-lease",
		Payload:    claimNICsJobPayload{IP: ip, MAC: mac},
		Timeout:    90 * time.Second,
	})
	if err != nil {
		s.writeAPIJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.writeAPIJSON(w, http.StatusAccepted, map[string]any{
		"ok": true, "job_id": job.ID, "kind": "claim-nics", "ip": ip, "mac": mac,
	})
}
