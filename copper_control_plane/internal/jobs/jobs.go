// Package jobs is a small in-process job registry with SQLite-backed history.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"copper_control_plane/internal/activity"
)

// Job type identifiers.
const (
	TypeProvisionWrite     = "provision.write"
	TypeProvisionPreflight = "provision.preflight"
	TypeStorageDetect         = "storage.detect"
	TypeStorageConfig         = "storage.config"
	TypeStorageExec           = "storage.exec"
	TypeStorageWipePartitions = "storage.wipe-partitions"
	TypeSoftRAIDScan          = "softraid.scan"
	TypeSoftRAIDExec          = "softraid.exec"
	TypeRedfishProbe       = "redfish.probe"
	TypeRedfishInventory   = "redfish.inventory"
	TypeRedfishReset       = "redfish.reset"
	TypeRedfishMount       = "redfish.mount"
	TypeRedfishConvertUEFI = "redfish.convert-uefi"
	TypeInventoryClaimNICs = "inventory.claim-nics" // orphan Data DHCP lease → attach MACs to matching server
)

// Status is the lifecycle of one job.
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusOK        Status = "ok"
	StatusError     Status = "error"
	StatusCancelled Status = "cancelled"
)

const maxLogLines = 2000

// downloadProgressLogWorthy keeps only ~25%% milestones in the durable job log.
func downloadProgressLogWorthy(line string) bool {
	// DOWNLOAD_PROGRESS pct=47 bytes=...
	idx := strings.Index(line, "pct=")
	if idx < 0 {
		return true // bytes-only counter — keep rare lines
	}
	rest := line[idx+4:]
	n := 0
	for i := 0; i < len(rest); i++ {
		if rest[i] < '0' || rest[i] > '9' {
			break
		}
		n = n*10 + int(rest[i]-'0')
	}
	return n == 0 || n == 100 || n%25 == 0
}

// Job is one tracked background operation.
type Job struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	ServerID   int64           `json:"server_id,omitempty"`
	ServerName string          `json:"server_name,omitempty"`
	Status     Status          `json:"status"`
	Phase      string          `json:"phase,omitempty"`
	Progress   string          `json:"progress,omitempty"`
	Log        []string        `json:"log"`
	Error      string          `json:"error,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	StartedAt  time.Time       `json:"started_at,omitempty"`
	FinishedAt time.Time       `json:"finished_at,omitempty"`

	// Convenience fields mirrored for provision Deploy UI compatibility.
	ImageURL   string `json:"image_url,omitempty"`
	TargetDisk string `json:"target_disk,omitempty"`
}

// ListFilter selects jobs for List.
type ListFilter struct {
	ServerID int64
	Type     string
	Status   Status
	Limit    int
}

// Persist is durable storage for jobs (typically SQLite).
type Persist interface {
	UpsertJob(j Job) error
	GetJob(id string) (*Job, error)
	ListJobs(f ListFilter) ([]*Job, error)
	FailInterruptedJobs(reason string) (int, error)
}

// Progress reports phase + log line updates from a handler.
type Progress func(phase, line string)

// Handler runs one job. It should respect ctx cancellation.
type Handler func(ctx context.Context, job *Job, progress Progress) error

// EnqueueOpts configures a new job.
type EnqueueOpts struct {
	Type       string
	ServerID   int64
	ServerName string
	Payload    any
	Timeout    time.Duration
	ImageURL   string
	TargetDisk string
}

// Registry tracks jobs, limits concurrency, and dispatches handlers.
//
// Concurrency rules:
//  1. Same server (ServerID > 0): at most one running job — others wait in queue.
//  2. Different servers: may run in parallel, capped by SetExecutors (global slots).
type Registry struct {
	mu         sync.Mutex
	jobs       map[string]*Job
	cancels    map[string]context.CancelFunc
	handlers   map[string]Handler
	persist    Persist
	act        *activity.Buffer
	limit      int
	active     int
	serverBusy map[int64]string // serverID → job id holding the per-server lock
	wake       chan struct{}
}

// New creates a registry. persist may be nil (memory-only).
func New(persist Persist, act *activity.Buffer) *Registry {
	if act == nil {
		act = activity.New(0)
	}
	r := &Registry{
		jobs:       map[string]*Job{},
		cancels:    map[string]context.CancelFunc{},
		handlers:   map[string]Handler{},
		persist:    persist,
		act:        act,
		limit:      3,
		serverBusy: map[int64]string{},
		wake:       make(chan struct{}, 1),
	}
	if persist != nil {
		if n, err := persist.FailInterruptedJobs("interrupted by control plane restart"); err == nil && n > 0 {
			act.Warn("jobs", "marked %d interrupted job(s) as error", n)
		}
	}
	return r
}

// Register binds a handler for a job type. Panics on empty type or nil handler.
func (r *Registry) Register(typ string, h Handler) {
	typ = strings.TrimSpace(typ)
	if typ == "" || h == nil {
		panic("jobs: Register requires type and handler")
	}
	r.mu.Lock()
	r.handlers[typ] = h
	r.mu.Unlock()
}

// SetExecutors caps how many jobs may run across different servers at once (1–16).
// Per-server serialization is always enforced regardless of this limit.
func (r *Registry) SetExecutors(n int) {
	if n < 1 {
		n = 1
	}
	if n > 16 {
		n = 16
	}
	r.mu.Lock()
	r.limit = n
	r.mu.Unlock()
	r.signal()
}

// Executors returns the concurrency limit.
func (r *Registry) Executors() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.limit
}

// Enqueue creates a queued job and starts a worker goroutine.
func (r *Registry) Enqueue(opts EnqueueOpts) (*Job, error) {
	typ := strings.TrimSpace(opts.Type)
	if typ == "" {
		return nil, fmt.Errorf("job type required")
	}
	r.mu.Lock()
	_, ok := r.handlers[typ]
	r.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("no handler registered for job type %q", typ)
	}
	timeout := opts.Timeout
	if timeout < time.Second {
		timeout = 5 * time.Minute
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	var payload json.RawMessage
	if opts.Payload != nil {
		raw, err := json.Marshal(opts.Payload)
		if err != nil {
			return nil, fmt.Errorf("marshal payload: %w", err)
		}
		payload = raw
	}
	now := time.Now().UTC()
	job := &Job{
		ID:         id,
		Type:       typ,
		ServerID:   opts.ServerID,
		ServerName: opts.ServerName,
		Status:     StatusQueued,
		Phase:      "queued",
		Log:        []string{},
		Payload:    payload,
		CreatedAt:  now,
		ImageURL:   opts.ImageURL,
		TargetDisk: opts.TargetDisk,
	}
	r.mu.Lock()
	r.jobs[id] = job
	r.mu.Unlock()
	r.save(job)
	r.act.Info("jobs", "job %s type=%s queued server=%s", id, typ, opts.ServerName)
	go r.run(id, timeout)
	return r.snapshot(id), nil
}

// Get returns a copy of a job (memory first, then persist).
func (r *Registry) Get(id string) (*Job, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, false
	}
	if cp := r.snapshot(id); cp != nil {
		return cp, true
	}
	if r.persist == nil {
		return nil, false
	}
	j, err := r.persist.GetJob(id)
	if err != nil || j == nil {
		return nil, false
	}
	return j, true
}

// List returns matching jobs (memory active + persisted history).
// Order is always newest-first by CreatedAt (stable by ID); never map iteration order.
func (r *Registry) List(f ListFilter) []*Job {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	if f.Limit > 500 {
		f.Limit = 500
	}
	seen := map[string]bool{}
	var out []*Job
	r.mu.Lock()
	for _, job := range r.jobs {
		if !matchFilter(job, f) {
			continue
		}
		cp := copyJob(job)
		out = append(out, cp)
		seen[job.ID] = true
	}
	r.mu.Unlock()
	if r.persist != nil {
		persisted, err := r.persist.ListJobs(f)
		if err == nil {
			for _, j := range persisted {
				if j == nil || seen[j.ID] {
					continue
				}
				out = append(out, j)
				seen[j.ID] = true
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out
}

// PurgeMemory cancels in-flight work and drops the in-memory job map.
// Call after durable jobs rows have been deleted (e.g. Settings purge).
func (r *Registry) PurgeMemory() {
	r.mu.Lock()
	for id, cancel := range r.cancels {
		if cancel != nil {
			cancel()
		}
		delete(r.cancels, id)
	}
	r.jobs = map[string]*Job{}
	r.serverBusy = map[int64]string{}
	r.active = 0
	r.mu.Unlock()
	r.signal()
}

// Cancel requests cancellation of a running/queued job.
func (r *Registry) Cancel(id string) bool {
	r.mu.Lock()
	cancel, ok := r.cancels[id]
	r.mu.Unlock()
	if !ok || cancel == nil {
		return false
	}
	cancel()
	return true
}

// WithServer runs fn while holding the same exclusive per-server lock jobs use.
// Sync handlers (SSH scans, form Redfish) must use this so they cannot overlap a
// job on the same baremetal. Different servers may run fn concurrently.
// serverID <= 0 runs fn without locking.
func (r *Registry) WithServer(ctx context.Context, serverID int64, fn func(context.Context) error) error {
	if fn == nil {
		return fmt.Errorf("nil func")
	}
	if serverID <= 0 {
		return fn(ctx)
	}
	id, err := newID()
	if err != nil {
		return err
	}
	holder := "sync:" + id
	if err := r.acquireServer(ctx, serverID, holder); err != nil {
		return err
	}
	defer r.releaseServer(serverID, holder)
	return fn(ctx)
}

// ServerBusy reports whether a job or sync op currently holds the server lock.
func (r *Registry) ServerBusy(serverID int64) bool {
	if serverID <= 0 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, busy := r.serverBusy[serverID]
	return busy
}

// BusyServers returns a copy of serverID → lock holder (job id or "sync:…").
func (r *Registry) BusyServers() map[int64]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[int64]string, len(r.serverBusy))
	for id, holder := range r.serverBusy {
		out[id] = holder
	}
	return out
}

func matchFilter(job *Job, f ListFilter) bool {
	if f.ServerID > 0 && job.ServerID != f.ServerID {
		return false
	}
	if f.Type != "" && job.Type != f.Type {
		return false
	}
	if f.Status != "" && job.Status != f.Status {
		return false
	}
	return true
}

func (r *Registry) run(id string, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	r.mu.Lock()
	r.cancels[id] = cancel
	serverID := int64(0)
	if job := r.jobs[id]; job != nil {
		serverID = job.ServerID
	}
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.cancels, id)
		r.mu.Unlock()
	}()

	// Same server first: serialize all work for one baremetal before taking a
	// global slot so waiting jobs do not starve other servers.
	r.set(id, func(job *Job) {
		job.Phase = "queued"
		if serverID > 0 {
			job.Log = append(job.Log, "[queued] waiting for server slot")
		} else {
			job.Log = append(job.Log, "[queued] waiting for executor slot")
		}
	})

	if err := r.acquireServer(ctx, serverID, id); err != nil {
		status := StatusError
		if ctx.Err() == context.Canceled {
			status = StatusCancelled
		}
		r.set(id, func(job *Job) {
			job.FinishedAt = time.Now().UTC()
			job.Status = status
			job.Phase = string(status)
			job.Error = "waiting for server: " + err.Error()
			job.Log = append(job.Log, "[error] "+job.Error)
		})
		r.act.Error("jobs", "job %s server wait failed: %v", id, err)
		return
	}
	defer r.releaseServer(serverID, id)

	r.set(id, func(job *Job) {
		job.Log = append(job.Log, "[queued] waiting for executor slot")
	})

	if err := r.acquire(ctx); err != nil {
		status := StatusError
		if ctx.Err() == context.Canceled {
			status = StatusCancelled
		}
		r.set(id, func(job *Job) {
			job.FinishedAt = time.Now().UTC()
			job.Status = status
			job.Phase = string(status)
			job.Error = "waiting for executor: " + err.Error()
			job.Log = append(job.Log, "[error] "+job.Error)
		})
		r.act.Error("jobs", "job %s executor wait failed: %v", id, err)
		return
	}
	defer r.release()

	r.mu.Lock()
	h := r.handlers[r.jobs[id].Type]
	snap := copyJob(r.jobs[id])
	r.mu.Unlock()
	if h == nil {
		r.set(id, func(job *Job) {
			job.FinishedAt = time.Now().UTC()
			job.Status = StatusError
			job.Phase = "error"
			job.Error = "handler missing"
			job.Log = append(job.Log, "[error] handler missing")
		})
		return
	}

	started := time.Now().UTC()
	r.set(id, func(job *Job) {
		job.Status = StatusRunning
		job.Phase = "start"
		job.StartedAt = started
		job.Log = append(job.Log, "[start] acquired executor slot")
	})

	progress := Progress(func(phase, line string) {
		r.set(id, func(job *Job) {
			if phase != "" {
				job.Phase = phase
			}
			if line != "" {
				job.Progress = line
				for _, part := range strings.Split(strings.ReplaceAll(line, "\r", "\n"), "\n") {
					part = strings.TrimSpace(part)
					if part == "" {
						continue
					}
					prefix := phase
					if prefix == "" {
						prefix = "log"
					}
					// DOWNLOAD_PROGRESS updates every ~2s; keep every line in Progress
					// for the UI "last" field, but only persist milestones in Log so
					// batch downloads don't flood the ring buffer.
					if strings.Contains(part, "DOWNLOAD_PROGRESS") {
						if !downloadProgressLogWorthy(part) {
							continue
						}
					}
					job.Log = append(job.Log, fmt.Sprintf("[%s] %s", prefix, part))
				}
				if len(job.Log) > maxLogLines {
					job.Log = job.Log[len(job.Log)-maxLogLines:]
				}
			}
		})
		if line != "" && !strings.Contains(line, "DOWNLOAD_PROGRESS") {
			r.act.Info("jobs", "job %s %s: %s", id, phase, truncate(line, 300))
		}
	})

	err := h(ctx, snap, progress)

	r.set(id, func(job *Job) {
		job.FinishedAt = time.Now().UTC()
		if err != nil {
			if ctx.Err() == context.Canceled && !isTimeout(ctx) {
				job.Status = StatusCancelled
				job.Phase = "cancelled"
			} else {
				job.Status = StatusError
				job.Phase = "error"
			}
			job.Error = err.Error()
			for _, part := range strings.Split(err.Error(), "\n") {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				job.Log = append(job.Log, "[error] "+part)
			}
		} else {
			job.Status = StatusOK
			job.Phase = "done"
			job.Log = append(job.Log, "[done] finished OK")
		}
	})
	if err != nil {
		r.act.Error("jobs", "job %s failed: %v", id, err)
	} else {
		r.act.Info("jobs", "job %s ok", id)
	}
}

func isTimeout(ctx context.Context) bool {
	return ctx.Err() == context.DeadlineExceeded
}

func (r *Registry) acquireServer(ctx context.Context, serverID int64, jobID string) error {
	if serverID <= 0 {
		return nil
	}
	for {
		r.mu.Lock()
		_, busy := r.serverBusy[serverID]
		if !busy {
			r.serverBusy[serverID] = jobID
			r.mu.Unlock()
			return nil
		}
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.wake:
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (r *Registry) releaseServer(serverID int64, jobID string) {
	if serverID <= 0 {
		return
	}
	r.mu.Lock()
	if r.serverBusy[serverID] == jobID {
		delete(r.serverBusy, serverID)
	}
	r.mu.Unlock()
	r.signal()
}

func (r *Registry) acquire(ctx context.Context) error {
	for {
		r.mu.Lock()
		if r.active < r.limit {
			r.active++
			r.mu.Unlock()
			return nil
		}
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.wake:
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (r *Registry) release() {
	r.mu.Lock()
	if r.active > 0 {
		r.active--
	}
	r.mu.Unlock()
	r.signal()
}

func (r *Registry) signal() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *Registry) set(id string, fn func(*Job)) {
	r.mu.Lock()
	job, ok := r.jobs[id]
	if !ok {
		r.mu.Unlock()
		return
	}
	fn(job)
	cp := copyJob(job)
	r.mu.Unlock()
	r.save(cp)
}

// SetResult stores a JSON result on the job (called from handlers via Registry).
func (r *Registry) SetResult(id string, result any) {
	raw, err := json.Marshal(result)
	if err != nil {
		return
	}
	r.set(id, func(job *Job) {
		job.Result = raw
	})
}

// SetServerMeta updates ServerID/Name after a discovery job learns which host it touched.
func (r *Registry) SetServerMeta(id string, serverID int64, serverName string) {
	r.set(id, func(job *Job) {
		if serverID > 0 {
			job.ServerID = serverID
		}
		if strings.TrimSpace(serverName) != "" {
			job.ServerName = strings.TrimSpace(serverName)
		}
	})
}

func (r *Registry) save(job *Job) {
	if r.persist == nil || job == nil {
		return
	}
	_ = r.persist.UpsertJob(*job)
}

func (r *Registry) snapshot(id string) *Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	job := r.jobs[id]
	if job == nil {
		return nil
	}
	return copyJob(job)
}

func copyJob(job *Job) *Job {
	cp := *job
	cp.Log = append([]string(nil), job.Log...)
	if job.Payload != nil {
		cp.Payload = append(json.RawMessage(nil), job.Payload...)
	}
	if job.Result != nil {
		cp.Result = append(json.RawMessage(nil), job.Result...)
	}
	return &cp
}

func newID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
