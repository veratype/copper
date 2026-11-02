package store

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"
)

const settingDataLeaseBindings = "data_lease_bindings"

// DataLeaseBinding is an admin override: this data-plane MAC belongs to server_id.
// Used when Resolve cannot identify the host, or to correct a wrong auto-match.
type DataLeaseBinding struct {
	ServerID   int64  `json:"server_id"`
	ServerName string `json:"server_name,omitempty"`
	IP         string `json:"ip,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"` // RFC3339
}

// GetDataLeaseBindings returns MAC(lowercase) → binding.
func (s *Store) GetDataLeaseBindings() (map[string]DataLeaseBinding, error) {
	raw, err := s.GetSetting(settingDataLeaseBindings)
	if err != nil {
		return nil, err
	}
	out := map[string]DataLeaseBinding{}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("parse data_lease_bindings: %w", err)
	}
	// Normalize keys.
	norm := map[string]DataLeaseBinding{}
	for k, v := range out {
		mac, err := normalizeStoreMAC(k)
		if err != nil || mac == "" || v.ServerID <= 0 {
			continue
		}
		norm[strings.ToLower(mac)] = v
	}
	return norm, nil
}

func (s *Store) saveDataLeaseBindings(m map[string]DataLeaseBinding) error {
	if m == nil {
		m = map[string]DataLeaseBinding{}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return s.SetSetting(settingDataLeaseBindings, string(b))
}

// UpsertDataLeaseBinding sets or replaces the admin binding for mac.
func (s *Store) UpsertDataLeaseBinding(mac string, b DataLeaseBinding) error {
	mac, err := normalizeStoreMAC(mac)
	if err != nil || mac == "" {
		return fmt.Errorf("invalid MAC")
	}
	if b.ServerID <= 0 {
		return fmt.Errorf("server_id required")
	}
	m, err := s.GetDataLeaseBindings()
	if err != nil {
		return err
	}
	b.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	m[strings.ToLower(mac)] = b
	return s.saveDataLeaseBindings(m)
}

// DeleteDataLeaseBinding removes the admin binding for mac (no-op if missing).
func (s *Store) DeleteDataLeaseBinding(mac string) error {
	mac, err := normalizeStoreMAC(mac)
	if err != nil || mac == "" {
		return fmt.Errorf("invalid MAC")
	}
	m, err := s.GetDataLeaseBindings()
	if err != nil {
		return err
	}
	key := strings.ToLower(mac)
	if _, ok := m[key]; !ok {
		return nil
	}
	delete(m, key)
	return s.saveDataLeaseBindings(m)
}

func normalizeStoreMAC(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	hw, err := net.ParseMAC(s)
	if err != nil {
		return "", fmt.Errorf("invalid MAC %q", s)
	}
	return hw.String(), nil
}
