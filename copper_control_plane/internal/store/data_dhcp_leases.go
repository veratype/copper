package store

import (
	"fmt"
	"net"
	"strings"
	"time"

	"copper_dhcp"
)

// DataDHCPLeaseBackend persists Data DHCP leases in SQLite (not a JSON file).
// Implements copper_dhcp.LeaseBackend.
type DataDHCPLeaseBackend struct {
	store *Store
}

// NewDataDHCPLeaseBackend returns a LeaseBackend backed by this Store.
func (s *Store) NewDataDHCPLeaseBackend() *DataDHCPLeaseBackend {
	return &DataDHCPLeaseBackend{store: s}
}

// SaveLeases replaces the entire data_dhcp_leases table with the given snapshot.
func (b *DataDHCPLeaseBackend) SaveLeases(leases []copper_dhcp.Lease) error {
	return b.store.ReplaceDataDHCPLeases(leases)
}

// LoadLeases returns non-expired leases from SQLite.
func (b *DataDHCPLeaseBackend) LoadLeases() ([]copper_dhcp.Lease, error) {
	return b.store.ListDataDHCPLeases()
}

// ReplaceDataDHCPLeases atomically replaces all Data DHCP leases.
func (s *Store) ReplaceDataDHCPLeases(leases []copper_dhcp.Lease) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM data_dhcp_leases`); err != nil {
		return fmt.Errorf("clear data dhcp leases: %w", err)
	}
	now := time.Now()
	for _, l := range leases {
		mac := strings.ToLower(strings.TrimSpace(l.MAC))
		if mac == "" || l.IP == nil {
			continue
		}
		ip := l.IP.To4()
		if ip == nil {
			continue
		}
		if l.ExpiresAt.Before(now) {
			continue
		}
		reserved := 0
		if l.Reserved {
			reserved = 1
		}
		_, err := tx.Exec(
			`INSERT INTO data_dhcp_leases(mac, ip, expires_at, reserved) VALUES(?,?,?,?)`,
			mac, ip.String(), l.ExpiresAt.UTC().Format(time.RFC3339Nano), reserved,
		)
		if err != nil {
			return fmt.Errorf("insert lease %s: %w", mac, err)
		}
	}
	return tx.Commit()
}

// ListDataDHCPLeases returns non-expired Data DHCP leases from SQLite.
// Expired rows are deleted as a side effect.
func (s *Store) ListDataDHCPLeases() ([]copper_dhcp.Lease, error) {
	rows, err := s.db.Query(`SELECT mac, ip, expires_at, reserved FROM data_dhcp_leases`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	now := time.Now()
	var out []copper_dhcp.Lease
	var expired []string
	for rows.Next() {
		var mac, ipStr, expStr string
		var reservedInt int
		if err := rows.Scan(&mac, &ipStr, &expStr, &reservedInt); err != nil {
			return nil, err
		}
		exp, err := time.Parse(time.RFC3339Nano, expStr)
		if err != nil {
			exp, err = time.Parse(time.RFC3339, expStr)
			if err != nil {
				expired = append(expired, mac)
				continue
			}
		}
		if exp.Before(now) {
			expired = append(expired, mac)
			continue
		}
		ip := net.ParseIP(ipStr)
		if ip != nil {
			ip = ip.To4()
		}
		if ip == nil {
			expired = append(expired, mac)
			continue
		}
		out = append(out, copper_dhcp.Lease{
			MAC:       mac,
			IP:        ip,
			ExpiresAt: exp,
			Reserved:  reservedInt != 0,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, mac := range expired {
		_, _ = s.db.Exec(`DELETE FROM data_dhcp_leases WHERE mac=?`, mac)
	}
	return out, nil
}
