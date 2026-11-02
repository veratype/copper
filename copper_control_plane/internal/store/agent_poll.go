package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"copper_dhcp"
)

const (
	AgentCmdQueued    = "queued"
	AgentCmdDelivered = "delivered"
	AgentCmdDone      = "done"
	AgentCmdExpired   = "expired"
	AgentCmdError     = "error"

	settingAgentCache = "agent_poll_cache"
)

// AgentCache is the last heartbeat snapshot from a poll-mode copper_bmc.
type AgentCache struct {
	LastSeenAt        time.Time              `json:"last_seen_at"`
	OS                string                 `json:"os"`
	DHCPRole          bool                   `json:"dhcp_role"`
	DHCPRunning       bool                   `json:"dhcp_running"`
	DHCPConfigEnabled bool                   `json:"dhcp_config_enabled"`
	DHCPConfig        *copper_dhcp.Config    `json:"dhcp_config,omitempty"`
	LLDPRole          bool                   `json:"lldp_role"`
	LLDPRunning       bool                   `json:"lldp_running"`
	LLDPInitError     string                 `json:"lldp_init_error,omitempty"`
	MediaFolder       string                 `json:"media_folder,omitempty"`
	MediaBaseURL      string                 `json:"media_base_url,omitempty"`
	MediaBind         string                 `json:"media_bind,omitempty"`
	MediaIface        string                 `json:"media_interface,omitempty"`
	MediaFiles        []AgentMediaFile       `json:"media_files,omitempty"`
	MediaNote         string                 `json:"media_note,omitempty"`
	Leases            []AgentDHCPLease       `json:"leases,omitempty"`
	Neighbors         []AgentLLDPNeighbor    `json:"neighbors,omitempty"`
	Activity          []AgentActivityEvent   `json:"activity,omitempty"`
	ActivityNextSince uint64                 `json:"activity_next_since,omitempty"`
	Health            map[string]any         `json:"health,omitempty"`
}

type AgentMediaFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type AgentDHCPLease struct {
	MAC       string `json:"mac"`
	IP        string `json:"ip"`
	ExpiresAt string `json:"expires_at,omitempty"`
	Reserved  bool   `json:"reserved,omitempty"`
}

type AgentLLDPNeighbor struct {
	ChassisID      string   `json:"chassis_id"`
	ChassisSubtype string   `json:"chassis_subtype,omitempty"`
	PortID         string   `json:"port_id,omitempty"`
	PortSubtype    string   `json:"port_subtype,omitempty"`
	TTL            uint16   `json:"ttl,omitempty"`
	SysName        string   `json:"sys_name,omitempty"`
	SysDescription string   `json:"sys_description,omitempty"`
	MgmtAddrs      []string `json:"mgmt_addrs,omitempty"`
	SrcMAC         string   `json:"src_mac"`
	Iface          string   `json:"iface,omitempty"`
	LastSeen       string   `json:"last_seen,omitempty"`
}

type AgentActivityEvent struct {
	ID        uint64 `json:"id"`
	Time      string `json:"time"`
	Component string `json:"component"`
	Level     string `json:"level"`
	Message   string `json:"message"`
}

// AgentCommand is one queued HTTP-shaped op for a poll-mode agent.
type AgentCommand struct {
	ID            string
	Method        string
	Path          string
	HeadersJSON   string // JSON object string
	Body          []byte
	Status        string
	QueuedAt      time.Time
	DeliveredAt   time.Time
	CompletedAt   time.Time
	ExpiresAt     time.Time
	ResultStatus  int
	ResultBody    []byte
	ResultError   string
}

func (s *Store) migrateAgentPoll() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS agent_commands (
  id TEXT PRIMARY KEY NOT NULL,
  method TEXT NOT NULL,
  path TEXT NOT NULL,
  headers_json TEXT NOT NULL DEFAULT '{}',
  body BLOB,
  status TEXT NOT NULL,
  queued_at TEXT NOT NULL,
  delivered_at TEXT NOT NULL DEFAULT '',
  completed_at TEXT NOT NULL DEFAULT '',
  expires_at TEXT NOT NULL,
  result_status INTEGER NOT NULL DEFAULT 0,
  result_body BLOB,
  result_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_agent_commands_status_queued
  ON agent_commands(status, queued_at);
`)
	return err
}

func (s *Store) SaveAgentCache(c AgentCache) error {
	if c.LastSeenAt.IsZero() {
		c.LastSeenAt = time.Now().UTC()
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.SetSetting(settingAgentCache, string(raw))
}

func (s *Store) GetAgentCache() (*AgentCache, error) {
	raw, err := s.GetSetting(settingAgentCache)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var c AgentCache
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil, fmt.Errorf("parse agent cache: %w", err)
	}
	return &c, nil
}

func (s *Store) EnqueueAgentCommand(cmd *AgentCommand) error {
	if cmd.ID == "" {
		return fmt.Errorf("command id required")
	}
	if cmd.Status == "" {
		cmd.Status = AgentCmdQueued
	}
	if cmd.QueuedAt.IsZero() {
		cmd.QueuedAt = time.Now().UTC()
	}
	if cmd.HeadersJSON == "" {
		cmd.HeadersJSON = "{}"
	}
	_, err := s.db.Exec(`
INSERT INTO agent_commands(
  id, method, path, headers_json, body, status,
  queued_at, delivered_at, completed_at, expires_at,
  result_status, result_body, result_error
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		cmd.ID, cmd.Method, cmd.Path, cmd.HeadersJSON, cmd.Body, cmd.Status,
		cmd.QueuedAt.UTC().Format(time.RFC3339Nano),
		formatTimeOrEmpty(cmd.DeliveredAt),
		formatTimeOrEmpty(cmd.CompletedAt),
		cmd.ExpiresAt.UTC().Format(time.RFC3339Nano),
		cmd.ResultStatus, cmd.ResultBody, cmd.ResultError,
	)
	return err
}

// ClaimNextAgentCommand marks the oldest non-expired queued command delivered.
func (s *Store) ClaimNextAgentCommand(now time.Time) (*AgentCommand, error) {
	if err := s.ExpireAgentCommands(now); err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	row := tx.QueryRow(`
SELECT id, method, path, headers_json, body, status,
       queued_at, delivered_at, completed_at, expires_at,
       result_status, result_body, result_error
FROM agent_commands
WHERE status=?
ORDER BY queued_at ASC
LIMIT 1`, AgentCmdQueued)

	cmd, err := scanAgentCommand(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !cmd.ExpiresAt.IsZero() && !now.Before(cmd.ExpiresAt) {
		_, _ = tx.Exec(`UPDATE agent_commands SET status=?, completed_at=?, result_error=? WHERE id=?`,
			AgentCmdExpired, now.UTC().Format(time.RFC3339Nano), "expired before delivery", cmd.ID)
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return s.ClaimNextAgentCommand(now)
	}

	delivered := now.UTC()
	_, err = tx.Exec(`UPDATE agent_commands SET status=?, delivered_at=? WHERE id=? AND status=?`,
		AgentCmdDelivered, delivered.Format(time.RFC3339Nano), cmd.ID, AgentCmdQueued)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	cmd.Status = AgentCmdDelivered
	cmd.DeliveredAt = delivered
	return cmd, nil
}

func (s *Store) CompleteAgentCommand(id string, statusCode int, body []byte, resultErr string, now time.Time) error {
	st := AgentCmdDone
	if resultErr != "" && statusCode == 0 {
		st = AgentCmdError
	}
	_, err := s.db.Exec(`
UPDATE agent_commands
SET status=?, completed_at=?, result_status=?, result_body=?, result_error=?
WHERE id=?`,
		st, now.UTC().Format(time.RFC3339Nano), statusCode, body, resultErr, id)
	return err
}

func (s *Store) GetAgentCommand(id string) (*AgentCommand, error) {
	row := s.db.QueryRow(`
SELECT id, method, path, headers_json, body, status,
       queued_at, delivered_at, completed_at, expires_at,
       result_status, result_body, result_error
FROM agent_commands WHERE id=?`, id)
	cmd, err := scanAgentCommand(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return cmd, err
}

func (s *Store) ExpireAgentCommands(now time.Time) error {
	_, err := s.db.Exec(`
UPDATE agent_commands
SET status=?, completed_at=?, result_error=?
WHERE status IN (?, ?) AND expires_at <> '' AND expires_at <= ?`,
		AgentCmdExpired, now.UTC().Format(time.RFC3339Nano), "expired waiting for agent",
		AgentCmdQueued, AgentCmdDelivered, now.UTC().Format(time.RFC3339Nano))
	return err
}

func scanAgentCommand(row scannable) (*AgentCommand, error) {
	var cmd AgentCommand
	var body, resultBody []byte
	var queued, delivered, completed, expires string
	err := row.Scan(
		&cmd.ID, &cmd.Method, &cmd.Path, &cmd.HeadersJSON, &body, &cmd.Status,
		&queued, &delivered, &completed, &expires,
		&cmd.ResultStatus, &resultBody, &cmd.ResultError,
	)
	if err != nil {
		return nil, err
	}
	cmd.Body = body
	cmd.ResultBody = resultBody
	cmd.QueuedAt, _ = parseTimeFlexible(queued)
	cmd.DeliveredAt, _ = parseTimeFlexible(delivered)
	cmd.CompletedAt, _ = parseTimeFlexible(completed)
	cmd.ExpiresAt, _ = parseTimeFlexible(expires)
	return &cmd, nil
}

func formatTimeOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTimeFlexible(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}
