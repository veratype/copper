package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"copper_control_plane/internal/store"
)

type leaseAssignBody struct {
	MAC      string `json:"mac"`
	IP       string `json:"ip"`
	ServerID int64  `json:"server_id"`
}

type leaseUnassignBody struct {
	MAC      string `json:"mac"`
	IP       string `json:"ip"`
	ServerID int64  `json:"server_id"` // optional; inferred from binding / inventory if 0
}

// handleAPIAssignLease manually binds a Data DHCP lease MAC to a baremetal.
// Body: {"mac":"…","ip":"…","server_id":7}
func (s *Server) handleAPIAssignLease(w http.ResponseWriter, r *http.Request) {
	var body leaseAssignBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid JSON: " + err.Error()})
		return
	}
	mac, err := normalizeMACInput(body.MAC)
	if err != nil || mac == "" {
		s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "valid mac is required"})
		return
	}
	if body.ServerID <= 0 {
		s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "server_id is required"})
		return
	}
	srv, err := s.store.GetServer(body.ServerID)
	if err != nil || srv == nil {
		s.writeAPIJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "server not found"})
		return
	}
	ip := strings.TrimSpace(body.IP)

	ctx := r.Context()
	var added bool
	err = s.withServerLock(ctx, srv.ID, func(locked context.Context) error {
		if err := s.detachMACFromOtherServers(locked, mac, srv.ID); err != nil {
			return err
		}
		ok, err := s.attachMACToServerInventory(locked, srv.ID, mac, "manual")
		if err != nil {
			return err
		}
		added = ok
		return s.store.UpsertDataLeaseBinding(mac, store.DataLeaseBinding{
			ServerID:   srv.ID,
			ServerName: srv.Name,
			IP:         ip,
		})
	})
	if err != nil {
		s.writeAPIJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if ip != "" {
		s.markClaimTaken(srv.ID, ip)
	}
	s.writeAPIJSON(w, http.StatusOK, map[string]any{
		"ok": true, "mac": mac, "ip": ip, "server_id": srv.ID, "server_name": srv.Name, "inventory_added": added,
	})
}

// handleAPIUnassignLease clears an admin/auto binding so the lease shows as unknown again.
// Body: {"mac":"…","ip":"…","server_id":7}
func (s *Server) handleAPIUnassignLease(w http.ResponseWriter, r *http.Request) {
	var body leaseUnassignBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid JSON: " + err.Error()})
		return
	}
	mac, err := normalizeMACInput(body.MAC)
	if err != nil || mac == "" {
		s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "valid mac is required"})
		return
	}
	serverID := body.ServerID
	if serverID <= 0 {
		if bindings, berr := s.store.GetDataLeaseBindings(); berr == nil {
			if b, ok := bindings[strings.ToLower(mac)]; ok {
				serverID = b.ServerID
			}
		}
	}
	if serverID <= 0 {
		for _, n := range s.listInventoryHostNICs() {
			if strings.EqualFold(n.MAC, mac) {
				serverID = n.ServerID
				break
			}
		}
	}
	if serverID <= 0 {
		s.writeAPIJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "could not find which server owns this MAC"})
		return
	}

	ctx := r.Context()
	var removed bool
	err = s.withServerLock(ctx, serverID, func(locked context.Context) error {
		ok, err := s.removeMACFromServerInventory(locked, serverID, mac)
		if err != nil {
			return err
		}
		removed = ok
		return s.store.DeleteDataLeaseBinding(mac)
	})
	if err != nil {
		s.writeAPIJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.clearClaimTaken(serverID, strings.TrimSpace(body.IP))

	name := ""
	if srv, gerr := s.store.GetServer(serverID); gerr == nil && srv != nil {
		name = srv.Name
	}
	s.writeAPIJSON(w, http.StatusOK, map[string]any{
		"ok": true, "mac": mac, "server_id": serverID, "server_name": name, "inventory_removed": removed,
	})
}

func (s *Server) clearClaimTaken(serverID int64, leaseIP string) {
	s.claimTakenMu.Lock()
	defer s.claimTakenMu.Unlock()
	if s.claimTakenByIP == nil {
		return
	}
	if cur, ok := s.claimTakenByIP[serverID]; ok {
		if leaseIP == "" || cur == leaseIP {
			delete(s.claimTakenByIP, serverID)
		}
	}
}

// attachMACToServerInventory appends mac to inventory network[] if missing.
// Returns whether a new entry was added.
func (s *Server) attachMACToServerInventory(ctx context.Context, serverID int64, mac, source string) (bool, error) {
	_ = ctx
	snap, err := s.store.GetSnapshot(serverID, "inventory")
	if err != nil {
		return false, err
	}
	payload := ""
	if snap != nil {
		payload = snap.Payload
	}
	have := inventoryMACSet(payload)
	if _, ok := have[strings.ToLower(mac)]; ok {
		return false, nil
	}
	added, newPayload, err := mergeManualMACIntoInventory(payload, mac, source)
	if err != nil {
		return false, err
	}
	if added == 0 {
		return false, nil
	}
	if err := s.store.SaveSnapshot(serverID, "inventory", newPayload); err != nil {
		return false, err
	}
	return true, nil
}

func mergeManualMACIntoInventory(payload, mac, source string) (int, string, error) {
	var root map[string]json.RawMessage
	if strings.TrimSpace(payload) == "" {
		root = map[string]json.RawMessage{}
	} else if err := json.Unmarshal([]byte(payload), &root); err != nil {
		return 0, "", fmt.Errorf("parse inventory: %w", err)
	}
	var network []map[string]any
	if raw, ok := root["network"]; ok && len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &network); err != nil {
			return 0, "", fmt.Errorf("parse network: %w", err)
		}
	}
	have := inventoryMACSet(payload)
	key := strings.ToLower(mac)
	if _, ok := have[key]; ok {
		formatted, err := json.MarshalIndent(root, "", "  ")
		return 0, string(formatted), err
	}
	if strings.TrimSpace(source) == "" {
		source = "manual"
	}
	network = append(network, map[string]any{
		"id":          "manual:" + mac,
		"name":        "manual",
		"mac_address": mac,
		"source":      source,
	})
	netRaw, err := json.Marshal(network)
	if err != nil {
		return 0, "", err
	}
	root["network"] = netRaw
	if raw, ok := root["counts"]; ok && len(raw) > 0 && string(raw) != "null" {
		var counts map[string]any
		if err := json.Unmarshal(raw, &counts); err == nil {
			counts["network"] = len(network)
			if cRaw, err := json.Marshal(counts); err == nil {
				root["counts"] = cRaw
			}
		}
	}
	formatted, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return 0, "", err
	}
	return 1, string(formatted), nil
}

// removeMACFromServerInventory removes network[] entries for mac (typically
// live_os / manual). Redfish-sourced rows are also removed so the lease can
// become unknown until Inventory is re-run.
func (s *Server) removeMACFromServerInventory(ctx context.Context, serverID int64, mac string) (bool, error) {
	_ = ctx
	snap, err := s.store.GetSnapshot(serverID, "inventory")
	if err != nil {
		return false, err
	}
	if snap == nil || strings.TrimSpace(snap.Payload) == "" {
		return false, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(snap.Payload), &root); err != nil {
		return false, fmt.Errorf("parse inventory: %w", err)
	}
	var network []map[string]any
	if raw, ok := root["network"]; ok && len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &network); err != nil {
			return false, fmt.Errorf("parse network: %w", err)
		}
	}
	key := strings.ToLower(mac)
	kept := make([]map[string]any, 0, len(network))
	removed := false
	for _, n := range network {
		hit := false
		for _, fld := range []string{"mac_address", "permanent_mac"} {
			raw, _ := n[fld].(string)
			m, nerr := normalizeMACInput(raw)
			if nerr == nil && m != "" && strings.ToLower(m) == key {
				hit = true
				break
			}
		}
		if hit {
			removed = true
			continue
		}
		kept = append(kept, n)
	}
	if !removed {
		return false, nil
	}
	netRaw, err := json.Marshal(kept)
	if err != nil {
		return false, err
	}
	root["network"] = netRaw
	if raw, ok := root["counts"]; ok && len(raw) > 0 && string(raw) != "null" {
		var counts map[string]any
		if err := json.Unmarshal(raw, &counts); err == nil {
			counts["network"] = len(kept)
			if cRaw, err := json.Marshal(counts); err == nil {
				root["counts"] = cRaw
			}
		}
	}
	formatted, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, err
	}
	if err := s.store.SaveSnapshot(serverID, "inventory", string(formatted)); err != nil {
		return false, err
	}
	return true, nil
}

// detachMACFromOtherServers strips mac from every other server's network[] so
// assign does not leave the lease enriched under two hosts.
func (s *Server) detachMACFromOtherServers(ctx context.Context, mac string, keepServerID int64) error {
	servers, err := s.store.ListServers()
	if err != nil {
		return err
	}
	for _, srv := range servers {
		if srv.ID == keepServerID {
			continue
		}
		// Nested WithServer would deadlock — strip without taking other locks;
		// assign already holds keepServerID. Snapshot writes for other servers
		// are best-effort under the caller's responsibility (admin action).
		if _, err := s.removeMACFromServerInventory(ctx, srv.ID, mac); err != nil {
			return fmt.Errorf("detach from %s: %w", srv.Name, err)
		}
	}
	return nil
}
