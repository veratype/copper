package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"copper_dhcp"

	_ "modernc.org/sqlite"
)

const (
	settingAgentDHCP    = "agent_dhcp_config" // OOB/BMC plane (pushed to copper_bmc)
	settingLocalDHCP    = "local_dhcp_config" // data plane (runs in this process)
	settingBatchRuntime = "batch_runtime"     // executors + per-action timeouts

	DefaultBatchExecutors          = 3
	MaxBatchExecutors              = 16
	DefaultTimeoutInventorySeconds = 180
	DefaultTimeoutRebootSeconds    = 60
	DefaultTimeoutMountSeconds     = 180

	// StatusDeclared = imported / known credentials, not yet matched to live OOB discovery.
	StatusDeclared = "declared"
	// StatusBound = matched to a live DHCP lease (or otherwise fulfilled from the BMC plane).
	StatusBound = "bound"

	SourceManual = "manual"
	SourceImport = "import"
)

type Store struct {
	db *sql.DB
}

type Server struct {
	ID        int64
	Name      string
	Endpoint  string // Redfish URL; may be empty until DHCP binds bmc_ip
	Username  string
	Password  string
	Insecure  bool
	MAC       string // BMC / OOB NIC MAC (for DHCP allow lists)
	ChassisID string // optional admin / chassis identifier from CSV
	Notes     string
	Status    string // declared | bound
	Source    string // manual | import
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
  endpoint TEXT NOT NULL DEFAULT '',
  username TEXT NOT NULL,
  password TEXT NOT NULL,
  insecure INTEGER NOT NULL DEFAULT 1,
  mac TEXT NOT NULL DEFAULT '',
  chassis_id TEXT NOT NULL DEFAULT '',
  notes TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'bound',
  source TEXT NOT NULL DEFAULT 'manual',
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
	if err != nil {
		return err
	}
	// Older DBs: additive columns.
	_, _ = s.db.Exec(`ALTER TABLE servers ADD COLUMN mac TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE servers ADD COLUMN chassis_id TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE servers ADD COLUMN status TEXT NOT NULL DEFAULT 'bound'`)
	_, _ = s.db.Exec(`ALTER TABLE servers ADD COLUMN source TEXT NOT NULL DEFAULT 'manual'`)
	return nil
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

// BatchRuntime is admin-tunable batch concurrency and per-action timeouts.
type BatchRuntime struct {
	Executors int `json:"executors"`
	Timeouts  struct {
		Inventory int `json:"inventory"` // seconds
		Reboot    int `json:"reboot"`
		Mount     int `json:"mount"`
	} `json:"timeouts"`
}

func DefaultBatchRuntime() BatchRuntime {
	var b BatchRuntime
	b.Executors = DefaultBatchExecutors
	b.Timeouts.Inventory = DefaultTimeoutInventorySeconds
	b.Timeouts.Reboot = DefaultTimeoutRebootSeconds
	b.Timeouts.Mount = DefaultTimeoutMountSeconds
	return b
}

func (b BatchRuntime) Normalize() BatchRuntime {
	out := b
	if out.Executors < 1 {
		out.Executors = DefaultBatchExecutors
	}
	if out.Executors > MaxBatchExecutors {
		out.Executors = MaxBatchExecutors
	}
	if out.Timeouts.Inventory < 10 {
		out.Timeouts.Inventory = DefaultTimeoutInventorySeconds
	}
	if out.Timeouts.Reboot < 5 {
		out.Timeouts.Reboot = DefaultTimeoutRebootSeconds
	}
	if out.Timeouts.Mount < 10 {
		out.Timeouts.Mount = DefaultTimeoutMountSeconds
	}
	// Soft ceilings so a typo does not hang the UI for hours.
	if out.Timeouts.Inventory > 900 {
		out.Timeouts.Inventory = 900
	}
	if out.Timeouts.Reboot > 300 {
		out.Timeouts.Reboot = 300
	}
	if out.Timeouts.Mount > 900 {
		out.Timeouts.Mount = 900
	}
	return out
}

func (s *Store) GetBatchRuntime() (BatchRuntime, error) {
	raw, err := s.GetSetting(settingBatchRuntime)
	if err != nil {
		return DefaultBatchRuntime(), err
	}
	if raw == "" {
		return DefaultBatchRuntime(), nil
	}
	var b BatchRuntime
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		return DefaultBatchRuntime(), fmt.Errorf("parse batch runtime: %w", err)
	}
	return b.Normalize(), nil
}

func (s *Store) SaveBatchRuntime(b BatchRuntime) error {
	b = b.Normalize()
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return s.SetSetting(settingBatchRuntime, string(raw))
}

// GetAgentDHCPConfig loads the last saved agent DHCP policy (may be zero value).
func (s *Store) GetAgentDHCPConfig() (copper_dhcp.Config, error) {
	raw, err := s.GetSetting(settingAgentDHCP)
	if err != nil || raw == "" {
		return copper_dhcp.Config{}, err
	}
	var cfg copper_dhcp.Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return copper_dhcp.Config{}, fmt.Errorf("parse agent dhcp config: %w", err)
	}
	return cfg, nil
}

func (s *Store) SaveAgentDHCPConfig(cfg copper_dhcp.Config) error {
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return s.SetSetting(settingAgentDHCP, string(b))
}

// GetLocalDHCPConfig loads data-plane DHCP policy for this control plane host.
func (s *Store) GetLocalDHCPConfig() (copper_dhcp.Config, error) {
	raw, err := s.GetSetting(settingLocalDHCP)
	if err != nil || raw == "" {
		return copper_dhcp.Config{}, err
	}
	var cfg copper_dhcp.Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return copper_dhcp.Config{}, fmt.Errorf("parse local dhcp config: %w", err)
	}
	return cfg, nil
}

func (s *Store) SaveLocalDHCPConfig(cfg copper_dhcp.Config) error {
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return s.SetSetting(settingLocalDHCP, string(b))
}

const serverSelect = `
SELECT id, name, endpoint, username, password, insecure, mac, chassis_id, notes, status, source, created_at, updated_at
FROM servers`

func (s *Store) ListServers() ([]Server, error) {
	rows, err := s.db.Query(serverSelect + ` ORDER BY name COLLATE NOCASE`)
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
	row := s.db.QueryRow(serverSelect+` WHERE id=?`, id)
	srv, err := scanServer(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("server %d not found", id)
	}
	if err != nil {
		return nil, err
	}
	return &srv, nil
}

// FindServerByMAC returns a server with the given canonical MAC, or nil.
func (s *Store) FindServerByMAC(mac string) (*Server, error) {
	mac = strings.ToLower(strings.TrimSpace(mac))
	if mac == "" {
		return nil, nil
	}
	servers, err := s.ListServers()
	if err != nil {
		return nil, err
	}
	for i := range servers {
		if strings.ToLower(servers[i].MAC) == mac {
			return &servers[i], nil
		}
	}
	return nil, nil
}

// FindServerByBMCIP finds a server whose endpoint host equals ip.
func (s *Store) FindServerByBMCIP(ip string) (*Server, error) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return nil, nil
	}
	servers, err := s.ListServers()
	if err != nil {
		return nil, err
	}
	for i := range servers {
		host := EndpointHost(servers[i].Endpoint)
		if host == ip {
			return &servers[i], nil
		}
	}
	return nil, nil
}

// FindServerByChassisID returns a server with the given chassis_id, or nil.
func (s *Store) FindServerByChassisID(id string) (*Server, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, nil
	}
	row := s.db.QueryRow(serverSelect+` WHERE chassis_id=? COLLATE NOCASE`, id)
	srv, err := scanServer(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &srv, nil
}

func (s *Store) CreateServer(srv Server) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	if srv.Status == "" {
		// New rows stay declared until Probe or OOB DHCP bind.
		srv.Status = StatusDeclared
	}
	if srv.Source == "" {
		srv.Source = SourceManual
	}
	res, err := s.db.Exec(`
INSERT INTO servers(name, endpoint, username, password, insecure, mac, chassis_id, notes, status, source, created_at, updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		srv.Name, srv.Endpoint, srv.Username, srv.Password, boolToInt(srv.Insecure),
		srv.MAC, srv.ChassisID, srv.Notes, srv.Status, srv.Source, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateServer(srv Server) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if srv.Status == "" {
		srv.Status = StatusBound
	}
	if srv.Source == "" {
		srv.Source = SourceManual
	}
	_, err := s.db.Exec(`
UPDATE servers SET name=?, endpoint=?, username=?, password=?, insecure=?, mac=?, chassis_id=?, notes=?, status=?, source=?, updated_at=?
WHERE id=?`,
		srv.Name, srv.Endpoint, srv.Username, srv.Password, boolToInt(srv.Insecure),
		srv.MAC, srv.ChassisID, srv.Notes, srv.Status, srv.Source, now, srv.ID)
	return err
}

// EnrichFromLLDP fills chassis_id / MAC / endpoint gaps from an LLDP sighting.
func (s *Store) EnrichFromLLDP(srv *Server, chassisID, srcMAC string, mgmtIPs []string) (changed bool, err error) {
	if srv == nil {
		return false, fmt.Errorf("nil server")
	}
	chassisID = strings.TrimSpace(chassisID)
	srcMAC = strings.TrimSpace(srcMAC)
	if srcMAC != "" {
		if hw, e := net.ParseMAC(srcMAC); e == nil {
			srcMAC = hw.String()
		}
	}

	if chassisID != "" && srv.ChassisID == "" {
		srv.ChassisID = chassisID
		changed = true
	}
	if srcMAC != "" && srv.MAC == "" {
		srv.MAC = srcMAC
		changed = true
	}
	if srv.Endpoint == "" {
		for _, ip := range mgmtIPs {
			ip = strings.TrimSpace(ip)
			if net.ParseIP(ip) != nil {
				srv.Endpoint = EndpointFromIP(ip)
				changed = true
				break
			}
		}
	}
	if !changed {
		return false, nil
	}
	return true, s.UpdateServer(*srv)
}

// BindFromLease fills missing MAC / endpoint from a live OOB DHCP lease and marks bound.
// Prefer keeping existing non-empty fields; only fills gaps (two-way fulfillment).
func (s *Store) BindFromLease(srv *Server, mac, ip string) (changed bool, err error) {
	if srv == nil {
		return false, fmt.Errorf("nil server")
	}
	mac = strings.TrimSpace(mac)
	ip = strings.TrimSpace(ip)

	if mac != "" && srv.MAC == "" {
		srv.MAC = mac
		changed = true
	} else if mac != "" && !strings.EqualFold(srv.MAC, mac) && srv.MAC != "" {
		// Same row matched by IP but DHCP reports a different MAC — prefer live L2.
		srv.MAC = mac
		changed = true
	}
	if ip != "" {
		host := EndpointHost(srv.Endpoint)
		if host == "" || host != ip {
			// OOB DHCP is source of truth for live BMC addressing.
			srv.Endpoint = EndpointFromIP(ip)
			changed = true
		}
	}
	if srv.Status != StatusBound {
		srv.Status = StatusBound
		changed = true
	}
	if !changed {
		return false, nil
	}
	return true, s.UpdateServer(*srv)
}

func (s *Store) DeleteServer(id int64) error {
	_, _ = s.db.Exec(`DELETE FROM snapshots WHERE server_id=?`, id)
	_, err := s.db.Exec(`DELETE FROM servers WHERE id=?`, id)
	return err
}

// EndpointFromIP builds a default Redfish URL for a BMC IPv4.
func EndpointFromIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return ""
	}
	return "https://" + ip
}

// EndpointHost extracts the host (usually BMC IP) from a Redfish endpoint URL.
func EndpointHost(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	// Avoid importing net/url here for a tiny helper — keep simple.
	rest := endpoint
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.IndexAny(rest, "/:"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanServer(r rowScanner) (Server, error) {
	var srv Server
	var insecure int
	var created, updated string
	err := r.Scan(&srv.ID, &srv.Name, &srv.Endpoint, &srv.Username, &srv.Password,
		&insecure, &srv.MAC, &srv.ChassisID, &srv.Notes, &srv.Status, &srv.Source, &created, &updated)
	if err != nil {
		return srv, err
	}
	srv.Insecure = insecure != 0
	if srv.Status == "" {
		srv.Status = StatusBound
	}
	if srv.Source == "" {
		srv.Source = SourceManual
	}
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
