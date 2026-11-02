package store

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"copper_control_plane/internal/jobs"
)

// UpsertJob inserts or replaces a job row.
func (s *Store) UpsertJob(j jobs.Job) error {
	logRaw, err := json.Marshal(j.Log)
	if err != nil {
		return err
	}
	payload := string(j.Payload)
	result := string(j.Result)
	_, err = s.db.Exec(`
INSERT INTO jobs(
  id, type, server_id, server_name, status, phase, progress, error,
  log_json, payload, result, image_url, target_disk,
  created_at, started_at, finished_at
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  type=excluded.type,
  server_id=excluded.server_id,
  server_name=excluded.server_name,
  status=excluded.status,
  phase=excluded.phase,
  progress=excluded.progress,
  error=excluded.error,
  log_json=excluded.log_json,
  payload=excluded.payload,
  result=excluded.result,
  image_url=excluded.image_url,
  target_disk=excluded.target_disk,
  created_at=excluded.created_at,
  started_at=excluded.started_at,
  finished_at=excluded.finished_at
`,
		j.ID, j.Type, j.ServerID, j.ServerName, string(j.Status), j.Phase, j.Progress, j.Error,
		string(logRaw), payload, result, j.ImageURL, j.TargetDisk,
		formatTime(j.CreatedAt), formatTime(j.StartedAt), formatTime(j.FinishedAt),
	)
	return err
}

// GetJob loads one job by id.
func (s *Store) GetJob(id string) (*jobs.Job, error) {
	row := s.db.QueryRow(`
SELECT id, type, server_id, server_name, status, phase, progress, error,
       log_json, payload, result, image_url, target_disk,
       created_at, started_at, finished_at
FROM jobs WHERE id=?`, id)
	j, err := scanJob(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return j, err
}

// ListJobs returns recent jobs matching the filter (newest first).
func (s *Store) ListJobs(f jobs.ListFilter) ([]*jobs.Job, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	if f.Limit > 500 {
		f.Limit = 500
	}
	q := `
SELECT id, type, server_id, server_name, status, phase, progress, error,
       log_json, payload, result, image_url, target_disk,
       created_at, started_at, finished_at
FROM jobs WHERE 1=1`
	var args []any
	if f.ServerID > 0 {
		q += ` AND server_id=?`
		args = append(args, f.ServerID)
	}
	if f.Type != "" {
		q += ` AND type=?`
		args = append(args, f.Type)
	}
	if f.Status != "" {
		q += ` AND status=?`
		args = append(args, string(f.Status))
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, f.Limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*jobs.Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// FailInterruptedJobs marks queued/running jobs as error (process restart recovery).
func (s *Store) FailInterruptedJobs(reason string) (int, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "interrupted"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.Exec(`
UPDATE jobs SET status=?, phase=?, error=?, finished_at=?
WHERE status IN ('queued','running')`,
		string(jobs.StatusError), "error", reason, now)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

type scannable interface {
	Scan(dest ...any) error
}

func scanJob(row scannable) (*jobs.Job, error) {
	var j jobs.Job
	var status, logJSON, payload, result string
	var created, started, finished string
	err := row.Scan(
		&j.ID, &j.Type, &j.ServerID, &j.ServerName, &status, &j.Phase, &j.Progress, &j.Error,
		&logJSON, &payload, &result, &j.ImageURL, &j.TargetDisk,
		&created, &started, &finished,
	)
	if err != nil {
		return nil, err
	}
	j.Status = jobs.Status(status)
	if logJSON != "" {
		_ = json.Unmarshal([]byte(logJSON), &j.Log)
	}
	if j.Log == nil {
		j.Log = []string{}
	}
	if payload != "" {
		j.Payload = json.RawMessage(payload)
	}
	if result != "" {
		j.Result = json.RawMessage(result)
	}
	j.CreatedAt = parseTime(created)
	j.StartedAt = parseTime(started)
	j.FinishedAt = parseTime(finished)
	return &j, nil
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
