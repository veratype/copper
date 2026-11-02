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
// Empty path disables file persistence. Call before Load.
func (s *Store) SetPersistPath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persistPath = path
}

// SetLeaseBackend enables DB (or other) persistence. Call before Load.
// When set, Save/Load prefer the backend over a file path.
func (s *Store) SetLeaseBackend(b LeaseBackend) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backend = b
}

// Load restores non-expired leases from the backend or persist file that still
// fit the current pool / reservations. Missing storage is not an error.
func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var leases []Lease
	if s.backend != nil {
		loaded, err := s.backend.LoadLeases()
		if err != nil {
			return fmt.Errorf("load leases from backend: %w", err)
		}
		leases = loaded
	} else if s.persistPath != "" {
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
		leases = payload.Leases
	} else {
		return nil
	}

	now := time.Now()
	for _, l := range leases {
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
		// Preserve original expiry from storage.
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
	if s.backend == nil && s.persistPath == "" {
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
	if s.backend != nil {
		_ = s.backend.SaveLeases(out)
		return
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

// SaveLeasesFile writes leases to path (creates parent dirs). Empty slice is valid.
func SaveLeasesFile(path string, leases []Lease) error {
	if path == "" {
		return nil
	}
	if leases == nil {
		leases = []Lease{}
	}
	data, err := json.MarshalIndent(leaseFilePayload{Leases: leases}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
