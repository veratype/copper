package store

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

const (
	MaxLabelLen     = 64
	MaxLabelsPerSet = 200 // soft cap per assign/add call (not a hard inventory limit)

	// LabelInstalled is auto-attached when Deploy OS (provision.write) finishes ok.
	LabelInstalled = "installed"
)

// NormalizeLabelName trims and validates a single label. Empty input returns "".
func NormalizeLabelName(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	// Collapse internal whitespace.
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if prevSpace {
				continue
			}
			b.WriteByte(' ')
			prevSpace = true
			continue
		}
		prevSpace = false
		if r == ',' || r == ';' {
			return "", fmt.Errorf("label %q must not contain commas or semicolons", s)
		}
		b.WriteRune(r)
	}
	s = strings.TrimSpace(b.String())
	if s == "" {
		return "", nil
	}
	if len([]rune(s)) > MaxLabelLen {
		return "", fmt.Errorf("label %q is longer than %d characters", s, MaxLabelLen)
	}
	return s, nil
}

// ParseLabelList splits a free-form labels field on commas, semicolons, or newlines.
// Order is preserved; duplicates (case-insensitive) are dropped. Empty ok (zero labels).
func ParseLabelList(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, p := range parts {
		name, err := NormalizeLabelName(p)
		if err != nil {
			return nil, err
		}
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, name)
		if len(out) > MaxLabelsPerSet {
			return nil, fmt.Errorf("too many labels (max %d per request)", MaxLabelsPerSet)
		}
	}
	return out, nil
}

func (s *Store) migrateLabels() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS labels (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL COLLATE NOCASE UNIQUE
);
CREATE TABLE IF NOT EXISTS server_labels (
  server_id INTEGER NOT NULL,
  label_id INTEGER NOT NULL,
  PRIMARY KEY (server_id, label_id),
  FOREIGN KEY (server_id) REFERENCES servers(id) ON DELETE CASCADE,
  FOREIGN KEY (label_id) REFERENCES labels(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_server_labels_label ON server_labels(label_id);
`)
	return err
}

// ListLabels returns every known label name, sorted case-insensitively.
func (s *Store) ListLabels() ([]string, error) {
	rows, err := s.db.Query(`SELECT name FROM labels ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func (s *Store) ensureLabelID(name string) (int64, error) {
	name, err := NormalizeLabelName(name)
	if err != nil {
		return 0, err
	}
	if name == "" {
		return 0, fmt.Errorf("empty label")
	}
	res, err := s.db.Exec(`INSERT OR IGNORE INTO labels(name) VALUES(?)`, name)
	if err != nil {
		return 0, err
	}
	// INSERT OR IGNORE that hits the unique name must NOT trust LastInsertId —
	// SQLite leaves last_insert_rowid as a stale value from an earlier INSERT
	// on this connection (wrong label id → only the first server gets tagged).
	if n, _ := res.RowsAffected(); n > 0 {
		id, err := res.LastInsertId()
		if err != nil {
			return 0, err
		}
		if id > 0 {
			return id, nil
		}
	}
	var id int64
	err = s.db.QueryRow(`SELECT id FROM labels WHERE name=? COLLATE NOCASE`, name).Scan(&id)
	return id, err
}

// SetServerLabels replaces all labels on a server (empty list clears them).
func (s *Store) SetServerLabels(serverID int64, names []string) error {
	names, err := dedupeNormalized(names)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM server_labels WHERE server_id=?`, serverID); err != nil {
		return err
	}
	for _, name := range names {
		var id int64
		res, err := tx.Exec(`INSERT OR IGNORE INTO labels(name) VALUES(?)`, name)
		if err != nil {
			return err
		}
		if lid, _ := res.LastInsertId(); lid > 0 {
			id = lid
		} else if err := tx.QueryRow(`SELECT id FROM labels WHERE name=? COLLATE NOCASE`, name).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO server_labels(server_id, label_id) VALUES(?,?)`,
			serverID, id,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AddServerLabels attaches labels without removing existing ones.
func (s *Store) AddServerLabels(serverID int64, names []string) error {
	names, err := dedupeNormalized(names)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return nil
	}
	for _, name := range names {
		id, err := s.ensureLabelID(name)
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(
			`INSERT OR IGNORE INTO server_labels(server_id, label_id) VALUES(?,?)`,
			serverID, id,
		); err != nil {
			return err
		}
	}
	return nil
}

// RemoveServerLabels detaches labels; unknown names are ignored.
func (s *Store) RemoveServerLabels(serverID int64, names []string) error {
	names, err := dedupeNormalized(names)
	if err != nil {
		return err
	}
	for _, name := range names {
		_, err := s.db.Exec(`
DELETE FROM server_labels
WHERE server_id=? AND label_id IN (
  SELECT id FROM labels WHERE name=? COLLATE NOCASE
)`, serverID, name)
		if err != nil {
			return err
		}
	}
	return nil
}

// AddLabelsToServers attaches the same labels to many servers (batch add).
func (s *Store) AddLabelsToServers(serverIDs []int64, names []string) error {
	names, err := dedupeNormalized(names)
	if err != nil {
		return err
	}
	if len(names) == 0 || len(serverIDs) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(names))
	for _, name := range names {
		id, err := s.ensureLabelID(name)
		if err != nil {
			return err
		}
		ids = append(ids, id)
	}
	for _, sid := range serverIDs {
		for _, lid := range ids {
			if _, err := s.db.Exec(
				`INSERT OR IGNORE INTO server_labels(server_id, label_id) VALUES(?,?)`,
				sid, lid,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

// RemoveLabelsFromServers detaches labels from many servers.
func (s *Store) RemoveLabelsFromServers(serverIDs []int64, names []string) error {
	names, err := dedupeNormalized(names)
	if err != nil {
		return err
	}
	if len(names) == 0 || len(serverIDs) == 0 {
		return nil
	}
	for _, sid := range serverIDs {
		if err := s.RemoveServerLabels(sid, names); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) labelsForServer(serverID int64) ([]string, error) {
	rows, err := s.db.Query(`
SELECT l.name FROM labels l
INNER JOIN server_labels sl ON sl.label_id = l.id
WHERE sl.server_id=?
ORDER BY l.name COLLATE NOCASE`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func (s *Store) attachLabels(servers []Server) error {
	if len(servers) == 0 {
		return nil
	}
	rows, err := s.db.Query(`
SELECT sl.server_id, l.name
FROM server_labels sl
INNER JOIN labels l ON l.id = sl.label_id
ORDER BY l.name COLLATE NOCASE`)
	if err != nil {
		return err
	}
	defer rows.Close()

	byID := map[int64][]string{}
	for rows.Next() {
		var sid int64
		var name string
		if err := rows.Scan(&sid, &name); err != nil {
			return err
		}
		byID[sid] = append(byID[sid], name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range servers {
		servers[i].Labels = byID[servers[i].ID]
		if servers[i].Labels == nil {
			servers[i].Labels = []string{}
		}
	}
	return nil
}

func dedupeNormalized(names []string) ([]string, error) {
	out := make([]string, 0, len(names))
	seen := map[string]struct{}{}
	for _, raw := range names {
		name, err := NormalizeLabelName(raw)
		if err != nil {
			return nil, err
		}
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, name)
		if len(out) > MaxLabelsPerSet {
			return nil, fmt.Errorf("too many labels (max %d per request)", MaxLabelsPerSet)
		}
	}
	return out, nil
}

// ServerHasAllLabels reports whether srv carries every required label (case-insensitive).
func ServerHasAllLabels(srv Server, required []string) bool {
	if len(required) == 0 {
		return true
	}
	have := map[string]struct{}{}
	for _, l := range srv.Labels {
		have[strings.ToLower(l)] = struct{}{}
	}
	for _, r := range required {
		name, err := NormalizeLabelName(r)
		if err != nil || name == "" {
			return false
		}
		if _, ok := have[strings.ToLower(name)]; !ok {
			return false
		}
	}
	return true
}

// FilterServersByLabels keeps servers that have all required labels.
func FilterServersByLabels(servers []Server, required []string) []Server {
	required, err := dedupeNormalized(required)
	if err != nil || len(required) == 0 {
		return servers
	}
	out := make([]Server, 0, len(servers))
	for _, srv := range servers {
		if ServerHasAllLabels(srv, required) {
			out = append(out, srv)
		}
	}
	return out
}

// SortLabelNames sorts in place case-insensitively.
func SortLabelNames(names []string) {
	sort.Slice(names, func(i, j int) bool {
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})
}
