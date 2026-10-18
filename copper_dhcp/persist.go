package copper_dhcp

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// leaseFilePayload is the on-disk shape for persisted leases.
type leaseFilePayload struct {
	Leases []Lease `json:"leases"`
}

// SetPersistPath enables writing the lease table to path after mutations.
// Empty path disables persistence. Call before Load.
func (s *Store) SetPersistPath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persistPath = path
}

// Load restores non-expired leases from the persist file that still fit the
// current pool / reservations. Missing file is not an error.
func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.persistPath == "" {
		return nil
	}
	data, err := os.ReadFile(s.persistPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read lease file: %w", err)
	}
	var payload leaseFilePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("parse lease file: %w", err)
	}
	now := time.Now()
	for _, l := range payload.Leases {
		if l.ExpiresAt.Before(now) {
			continue
		}
		mac, err := normalizeMAC(l.MAC)
		if err != nil || mac == "" {
			continue
		}
		ip := l.IP.To4()
		if ip == nil {
			continue
		}
		if !s.restorableLocked(mac, ip, l.Reserved) {
			continue
		}
		// Preserve original expiry from disk.
		if old, ok := s.byMAC[mac]; ok {
			delete(s.byIP, old.IP.String())
		}
		ip = append(net.IP(nil), ip...)
		lease := &Lease{
			MAC:       mac,
			IP:        ip,
			ExpiresAt: l.ExpiresAt,
			Reserved:  l.Reserved,
		}
		s.byMAC[mac] = lease
		s.byIP[ip.String()] = lease
	}
	return nil
}

func (s *Store) restorableLocked(mac string, ip net.IP, reserved bool) bool {
	if owner, ok := s.cfg.ReserveIP[ip.String()]; ok {
		return owner == mac
	}
	if _, ok := s.cfg.ReserveMAC[mac]; ok {
		return reserved || s.inPool(ip)
	}
	return s.inPool(ip)
}

func (s *Store) saveLocked() {
	if s.persistPath == "" {
		return
	}
	out := make([]Lease, 0, len(s.byMAC))
	for _, l := range s.byMAC {
		out = append(out, Lease{
			MAC:       l.MAC,
			IP:        append(net.IP(nil), l.IP...),
			ExpiresAt: l.ExpiresAt,
			Reserved:  l.Reserved,
		})
	}
	data, err := json.MarshalIndent(leaseFilePayload{Leases: out}, "", "  ")
	if err != nil {
		return
	}
	dir := filepath.Dir(s.persistPath)
	if dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	tmp := s.persistPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, s.persistPath)
}

// LoadLeasesFile reads a lease file without a live Store (for Manager when DHCP is stopped).
// Expired entries are omitted. Missing file yields an empty slice.
func LoadLeasesFile(path string) ([]Lease, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var payload leaseFilePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]Lease, 0, len(payload.Leases))
	for _, l := range payload.Leases {
		if l.ExpiresAt.Before(now) {
			continue
		}
		mac, err := normalizeMAC(l.MAC)
		if err != nil {
			continue
		}
		ip := l.IP.To4()
		if ip == nil {
			continue
		}
		out = append(out, Lease{
			MAC:       mac,
			IP:        append(net.IP(nil), ip...),
			ExpiresAt: l.ExpiresAt,
			Reserved:  l.Reserved,
		})
	}
	return out, nil
}
