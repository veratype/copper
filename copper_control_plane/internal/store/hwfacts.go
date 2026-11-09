package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// HardwareFacts is a deduped, queryable summary of a server's inventory snapshot.
// Free-form Labels stay separate; this is structured hardware only.
type HardwareFacts struct {
	CPUCount   int      `json:"cpu_count"`
	CPUModels  []string `json:"cpu_models,omitempty"`
	CPUCores   int      `json:"cpu_cores"`
	CPUThreads int      `json:"cpu_threads"`
	CPUSummary string   `json:"cpu_summary,omitempty"`

	RAMGiB     int    `json:"ram_gib"`
	RAMModules int    `json:"ram_modules"`
	RAMSummary string `json:"ram_summary,omitempty"`

	GPUCount   int      `json:"gpu_count"`
	GPUModels  []string `json:"gpu_models,omitempty"`
	HasGPU     bool     `json:"has_gpu"`
	GPUSummary string   `json:"gpu_summary,omitempty"`

	NICCount   int    `json:"nic_count"`
	NICSummary string `json:"nic_summary,omitempty"`

	PSUCount   int    `json:"psu_count"`
	PSUSummary string `json:"psu_summary,omitempty"`

	Storage        []StorageFact `json:"storage,omitempty"`
	DriveCount     int           `json:"drive_count"`
	StorageSummary string        `json:"storage_summary,omitempty"`

	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// StorageFact is one media/protocol bucket after drive dedupe.
type StorageFact struct {
	Key         string `json:"key"` // HDD | NVMe | SATA-SSD | SAS-SSD | SSD | Other | …
	MediaType   string `json:"media_type,omitempty"`
	Protocol    string `json:"protocol,omitempty"`
	Count       int    `json:"count"`
	CapacityGiB int64  `json:"capacity_gib,omitempty"`
}

// StorageCount returns drives in a bucket (case-insensitive key match).
func (h *HardwareFacts) StorageCount(key string) int {
	if h == nil {
		return 0
	}
	want := strings.ToUpper(strings.TrimSpace(key))
	for _, s := range h.Storage {
		if strings.ToUpper(s.Key) == want {
			return s.Count
		}
	}
	return 0
}

// SSDCount is NVMe + SATA-SSD + SAS-SSD + generic SSD.
func (h *HardwareFacts) SSDCount() int {
	if h == nil {
		return 0
	}
	n := 0
	for _, s := range h.Storage {
		k := strings.ToUpper(s.Key)
		switch k {
		case "NVME", "SATA-SSD", "SAS-SSD", "SSD":
			n += s.Count
		}
	}
	return n
}

func (s *Store) migrateHardwareFacts() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS server_hw_facts (
  server_id INTEGER PRIMARY KEY,
  payload TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  FOREIGN KEY (server_id) REFERENCES servers(id) ON DELETE CASCADE
);
`)
	return err
}

// SaveHardwareFacts upserts the structured facts for a server.
func (s *Store) SaveHardwareFacts(serverID int64, facts HardwareFacts) error {
	facts.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(facts)
	if err != nil {
		return err
	}
	now := facts.UpdatedAt.Format(time.RFC3339)
	_, err = s.db.Exec(`
INSERT INTO server_hw_facts(server_id, payload, updated_at) VALUES(?,?,?)
ON CONFLICT(server_id) DO UPDATE SET
  payload=excluded.payload,
  updated_at=excluded.updated_at`, serverID, string(raw), now)
	return err
}

// RefreshHardwareFactsFromInventory parses an inventory snapshot JSON and stores facts.
func (s *Store) RefreshHardwareFactsFromInventory(serverID int64, inventoryJSON string) error {
	facts, err := SummarizeInventory(inventoryJSON)
	if err != nil {
		return err
	}
	return s.SaveHardwareFacts(serverID, facts)
}

// GetHardwareFacts returns stored facts, or nil if none.
func (s *Store) GetHardwareFacts(serverID int64) (*HardwareFacts, error) {
	var payload, updated string
	err := s.db.QueryRow(`
SELECT payload, updated_at FROM server_hw_facts WHERE server_id=?`, serverID).Scan(&payload, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var facts HardwareFacts
	if err := json.Unmarshal([]byte(payload), &facts); err != nil {
		return nil, fmt.Errorf("parse hw facts: %w", err)
	}
	if t, err := time.Parse(time.RFC3339, updated); err == nil {
		facts.UpdatedAt = t
	}
	return &facts, nil
}

// DeleteHardwareFacts removes facts for one server (best-effort).
func (s *Store) DeleteHardwareFacts(serverID int64) error {
	_, err := s.db.Exec(`DELETE FROM server_hw_facts WHERE server_id=?`, serverID)
	return err
}

func (s *Store) attachHardwareFacts(servers []Server) error {
	if len(servers) == 0 {
		return nil
	}
	rows, err := s.db.Query(`SELECT server_id, payload, updated_at FROM server_hw_facts`)
	if err != nil {
		return err
	}
	defer rows.Close()

	byID := map[int64]*HardwareFacts{}
	for rows.Next() {
		var id int64
		var payload, updated string
		if err := rows.Scan(&id, &payload, &updated); err != nil {
			return err
		}
		var facts HardwareFacts
		if err := json.Unmarshal([]byte(payload), &facts); err != nil {
			continue
		}
		if t, err := time.Parse(time.RFC3339, updated); err == nil {
			facts.UpdatedAt = t
		}
		cp := facts
		byID[id] = &cp
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range servers {
		if f, ok := byID[servers[i].ID]; ok {
			servers[i].Hardware = f
		}
	}
	return nil
}

// BackfillHardwareFactsFromSnapshots rebuilds facts for every inventory snapshot.
// Safe to call on startup; skips servers that fail to parse.
// Loads all rows first — Store uses MaxOpenConns(1), so writing while a Query
// cursor is open would deadlock.
func (s *Store) BackfillHardwareFactsFromSnapshots() (int, error) {
	rows, err := s.db.Query(`SELECT server_id, payload FROM snapshots WHERE kind='inventory'`)
	if err != nil {
		return 0, err
	}
	type snap struct {
		id      int64
		payload string
	}
	var list []snap
	for rows.Next() {
		var item snap
		if err := rows.Scan(&item.id, &item.payload); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}

	n := 0
	for _, item := range list {
		if strings.TrimSpace(item.payload) == "" {
			continue
		}
		if err := s.RefreshHardwareFactsFromInventory(item.id, item.payload); err != nil {
			continue
		}
		n++
	}
	return n, nil
}
