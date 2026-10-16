package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type Server struct {
	ID        int64
	Name      string
	Endpoint  string
	Username  string
	Password  string
	Insecure  bool
	Notes     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS servers (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  endpoint TEXT NOT NULL,
  username TEXT NOT NULL,
  password TEXT NOT NULL,
  insecure INTEGER NOT NULL DEFAULT 1,
  notes TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS snapshots (
  server_id INTEGER NOT NULL,
  kind TEXT NOT NULL,
  payload TEXT NOT NULL,
  fetched_at TEXT NOT NULL,
  PRIMARY KEY (server_id, kind),
  FOREIGN KEY (server_id) REFERENCES servers(id) ON DELETE CASCADE
);
`)
	return err
}

// Snapshot is a cached Redfish result for one server + kind (e.g. inventory).
type Snapshot struct {
	ServerID  int64
	Kind      string
	Payload   string // pretty JSON
	FetchedAt time.Time
}

func (s *Store) SaveSnapshot(serverID int64, kind, payload string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`
INSERT INTO snapshots(server_id, kind, payload, fetched_at) VALUES(?,?,?,?)
ON CONFLICT(server_id, kind) DO UPDATE SET
  payload=excluded.payload,
  fetched_at=excluded.fetched_at`, serverID, kind, payload, now)
	return err
}

func (s *Store) GetSnapshot(serverID int64, kind string) (*Snapshot, error) {
	var snap Snapshot
	var fetched string
	err := s.db.QueryRow(`
SELECT server_id, kind, payload, fetched_at FROM snapshots
WHERE server_id=? AND kind=?`, serverID, kind).Scan(&snap.ServerID, &snap.Kind, &snap.Payload, &fetched)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	snap.FetchedAt, _ = time.Parse(time.RFC3339, fetched)
	return &snap, nil
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`
INSERT INTO settings(key, value) VALUES(?, ?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (s *Store) GetSetting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *Store) ListServers() ([]Server, error) {
	rows, err := s.db.Query(`
SELECT id, name, endpoint, username, password, insecure, notes, created_at, updated_at
FROM servers ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Server
	for rows.Next() {
		srv, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, srv)
	}
	return out, rows.Err()
}

func (s *Store) GetServer(id int64) (*Server, error) {
	row := s.db.QueryRow(`
SELECT id, name, endpoint, username, password, insecure, notes, created_at, updated_at
FROM servers WHERE id=?`, id)
	srv, err := scanServer(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("server %d not found", id)
	}
	if err != nil {
		return nil, err
	}
	return &srv, nil
}

func (s *Store) CreateServer(srv Server) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.Exec(`
INSERT INTO servers(name, endpoint, username, password, insecure, notes, created_at, updated_at)
VALUES(?,?,?,?,?,?,?,?)`,
		srv.Name, srv.Endpoint, srv.Username, srv.Password, boolToInt(srv.Insecure), srv.Notes, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateServer(srv Server) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`
UPDATE servers SET name=?, endpoint=?, username=?, password=?, insecure=?, notes=?, updated_at=?
WHERE id=?`,
		srv.Name, srv.Endpoint, srv.Username, srv.Password, boolToInt(srv.Insecure), srv.Notes, now, srv.ID)
	return err
}

func (s *Store) DeleteServer(id int64) error {
	_, _ = s.db.Exec(`DELETE FROM snapshots WHERE server_id=?`, id)
	_, err := s.db.Exec(`DELETE FROM servers WHERE id=?`, id)
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanServer(r rowScanner) (Server, error) {
	var srv Server
	var insecure int
	var created, updated string
	err := r.Scan(&srv.ID, &srv.Name, &srv.Endpoint, &srv.Username, &srv.Password,
		&insecure, &srv.Notes, &created, &updated)
	if err != nil {
		return srv, err
	}
	srv.Insecure = insecure != 0
	srv.CreatedAt, _ = time.Parse(time.RFC3339, created)
	srv.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return srv, nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
