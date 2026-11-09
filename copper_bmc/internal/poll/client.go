package poll

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"time"

	"copper_bmc/internal/activity"
	"copper_bmc/internal/command"
	"copper_bmc/internal/config"
	"copper_bmc/internal/media"
	"copper_bmc/internal/netx"

	"copper_dhcp"
)

// MaxWorkers caps concurrent Redfish/command executions in poll mode.
// Matches copper_control_plane MaxBatchExecutors so poll can keep up with
// listen-mode parallelism (CP dials many HTTP requests; here we pull many).
// Power-cycle jobs are still limited to 16 on the control plane job registry.
const MaxWorkers = 1000

// Runner dials copper_control_plane: heartbeat (cache) + long-poll commands.
// Commands run concurrently through the in-process command API (same Handler
// listen mode serves over HTTP), capped by MaxWorkers.
type Runner struct {
	cfg        *config.Config
	api        *command.API
	dhcp       *copper_dhcp.Manager
	lldp       *netx.LLDPTable
	lldpErr    string
	act        *activity.Buffer
	httpClient *http.Client
	workers    int

	activitySince uint64
	activityMu    sync.Mutex
}

func New(cfg *config.Config, api *command.API, dhcp *copper_dhcp.Manager, lldp *netx.LLDPTable, lldpErr string, act *activity.Buffer) *Runner {
	return &Runner{
		cfg:     cfg,
		api:     api,
		dhcp:    dhcp,
		lldp:    lldp,
		lldpErr: lldpErr,
		act:     act,
		httpClient: &http.Client{
			Timeout: 120 * time.Second, // covers long-poll wait
		},
		workers: MaxWorkers,
	}
}

// Run blocks: background heartbeat + concurrent command pull/exec forever.
func (r *Runner) Run() error {
	base := strings.TrimRight(r.cfg.ControlPlaneURL, "/")
	n := r.workers
	if n < 1 {
		n = 1
	}
	if n > MaxWorkers {
		n = MaxWorkers
	}
	r.act.Info("poll", "poll mode → %s (os=%s, workers=%d)", base, runtime.GOOS, n)

	go r.heartbeatLoop(base)

	// Token bucket of free worker slots. Acquire before claiming so we never
	// deliver more in-flight commands than we can execute (same idea as
	// listen mode's natural HTTP concurrency, but pull-based).
	slots := make(chan struct{}, n)
	handler := r.api.Handler()

	for {
		slots <- struct{}{}

		// Idle (this is the only held slot): long-poll. Already busy: non-blocking
		// claim so we fill the remaining workers as soon as CP has more work.
		waitSec := 0
		if len(slots) == 1 {
			waitSec = 25
		}

		cmd, err := r.nextCommand(base, waitSec)
		if err != nil {
			<-slots
			r.act.Warn("poll", "next command failed: %v — retry in 3s", err)
			time.Sleep(3 * time.Second)
			continue
		}
		if cmd == nil {
			<-slots
			continue
		}

		r.act.Info("poll", "got command %s %s %s (queued=%s expires=%s in_flight≈%d)",
			cmd.ID, cmd.Method, cmd.Path, cmd.QueuedAt, cmd.ExpiresAt, len(slots))

		go func(cmd *pollCommand) {
			defer func() { <-slots }()
			status, body, execErr := r.exec(handler, cmd)
			errMsg := ""
			if execErr != nil {
				errMsg = execErr.Error()
				r.act.Error("poll", "command %s exec: %v", cmd.ID, execErr)
			}
			if err := r.postResult(base, cmd.ID, status, body, errMsg); err != nil {
				r.act.Error("poll", "post result %s: %v", cmd.ID, err)
				time.Sleep(2 * time.Second)
			}
		}(cmd)
	}
}

func (r *Runner) heartbeatLoop(base string) {
	// First heartbeat ASAP so CP marks the agent online before commands arrive.
	for {
		if err := r.heartbeat(base); err != nil {
			r.act.Warn("poll", "heartbeat failed: %v — retry in 5s", err)
			time.Sleep(5 * time.Second)
			continue
		}
		time.Sleep(10 * time.Second)
	}
}

type heartbeatPayload struct {
	OS                string              `json:"os"`
	DHCPRole          bool                `json:"dhcp_role"`
	DHCPRunning       bool                `json:"dhcp_running"`
	DHCPConfigEnabled bool                `json:"dhcp_config_enabled"`
	DHCPConfig        *copper_dhcp.Config `json:"dhcp_config,omitempty"`
	LLDPRole          bool                `json:"lldp_role"`
	LLDPRunning       bool                `json:"lldp_running"`
	LLDPInitError     string              `json:"lldp_init_error,omitempty"`
	Media             any                 `json:"media,omitempty"`
	Leases            any                 `json:"leases,omitempty"`
	Neighbors         any                 `json:"neighbors,omitempty"`
	Activity          []activity.Event    `json:"activity,omitempty"`
	ActivityNextSince uint64              `json:"activity_next_since,omitempty"`
	Health            map[string]any      `json:"health,omitempty"`
}

func (r *Runner) heartbeat(base string) error {
	r.activityMu.Lock()
	since := r.activitySince
	r.activityMu.Unlock()

	payload := heartbeatPayload{
		OS:            runtime.GOOS,
		DHCPRole:      r.dhcp != nil,
		LLDPRole:      r.cfg.LLDPRole,
		LLDPRunning:   r.lldp != nil,
		LLDPInitError: r.lldpErr,
		Health: map[string]any{
			"status":  "ok",
			"service": "copper_bmc",
			"mode":    "poll",
			"os":      runtime.GOOS,
			"workers": r.workers,
		},
	}
	if r.dhcp != nil {
		st := r.dhcp.Status()
		payload.DHCPRunning = st.Running
		payload.DHCPConfigEnabled = st.Config.Enabled
		cfgCopy := st.Config
		payload.DHCPConfig = &cfgCopy
		payload.Leases = r.dhcp.Leases()
	}
	if cat, err := media.ListCatalog(r.cfg); err == nil {
		payload.Media = cat
	}
	if r.lldp != nil {
		payload.Neighbors = r.lldp.List()
	}
	events := r.act.Since(since, 200, "")
	payload.Activity = events
	next := since
	for _, ev := range events {
		if ev.ID > next {
			next = ev.ID
		}
	}
	payload.ActivityNextSince = next

	var resp struct {
		OK bool `json:"ok"`
	}
	if err := r.postJSON(base+"/api/agent/poll/heartbeat", payload, 15*time.Second, &resp); err != nil {
		return err
	}
	r.activityMu.Lock()
	r.activitySince = next
	r.activityMu.Unlock()
	return nil
}

type pollCommand struct {
	ID          string            `json:"id"`
	Method      string            `json:"method"`
	Path        string            `json:"path"`
	Headers     map[string]string `json:"headers"`
	Body        json.RawMessage   `json:"body"`
	QueuedAt    string            `json:"queued_at"`
	DeliveredAt string            `json:"delivered_at"`
	ExpiresAt   string            `json:"expires_at"`
}

func (r *Runner) nextCommand(base string, waitSeconds int) (*pollCommand, error) {
	var resp struct {
		Command *pollCommand `json:"command"`
	}
	timeout := 60 * time.Second
	if waitSeconds <= 0 {
		timeout = 15 * time.Second
	}
	body := map[string]any{"wait_seconds": waitSeconds}
	if err := r.postJSON(base+"/api/agent/poll/commands/next", body, timeout, &resp); err != nil {
		return nil, err
	}
	return resp.Command, nil
}

func (r *Runner) postResult(base, id string, status int, body []byte, errMsg string) error {
	payload := struct {
		StatusCode int             `json:"status_code"`
		Body       json.RawMessage `json:"body"`
		Error      string          `json:"error"`
	}{
		StatusCode: status,
		Error:      errMsg,
	}
	if len(body) > 0 {
		payload.Body = json.RawMessage(body)
	}
	var resp map[string]any
	return r.postJSON(base+"/api/agent/poll/commands/"+id+"/result", payload, 30*time.Second, &resp)
}

func (r *Runner) exec(handler http.Handler, cmd *pollCommand) (int, []byte, error) {
	method := strings.ToUpper(strings.TrimSpace(cmd.Method))
	if method == "" {
		method = http.MethodGet
	}
	path := cmd.Path
	if path == "" {
		return 0, nil, fmt.Errorf("empty path")
	}
	var bodyReader io.Reader
	if len(cmd.Body) > 0 && string(cmd.Body) != "null" {
		bodyReader = bytes.NewReader(cmd.Body)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	for k, v := range cmd.Headers {
		req.Header.Set(k, v)
	}
	if bodyReader != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes(), nil
}

func (r *Runner) postJSON(url string, payload any, timeout time.Duration, dest any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	client := r.httpClient
	if timeout > 0 {
		c := *r.httpClient
		c.Timeout = timeout
		client = &c
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	if dest == nil {
		return nil
	}
	return json.Unmarshal(data, dest)
}
