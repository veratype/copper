package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"copper_control_plane/internal/config"
	"copper_control_plane/internal/store"

	"copper_dhcp"
)

// Client talks to copper_bmc — either by dialing the command API (listen)
// or by enqueueing work for a polling agent (poll).
type Client struct {
	Mode       string // config.AgentModeListen | config.AgentModePoll
	BaseURL    string
	HTTPClient *http.Client
	Store      *store.Store // poll: last-known cache
	Poll       *PollBridge  // poll: command queue waiters
}

func New(baseURL string) *Client {
	return &Client{
		Mode:    config.AgentModeListen,
		BaseURL: baseURL,
		HTTPClient: &http.Client{
			Timeout: 180 * time.Second, // inventory walks cpu/ram/disk/network/gpu
		},
	}
}

// NewPoll returns a client that never dials copper_bmc; observations come from
// the last heartbeat cache and commands go through PollBridge.
func NewPoll(st *store.Store, bridge *PollBridge) *Client {
	return &Client{
		Mode:  config.AgentModePoll,
		Store: st,
		Poll:  bridge,
		HTTPClient: &http.Client{
			Timeout: 180 * time.Second,
		},
	}
}

func (c *Client) IsPollMode() bool {
	return c != nil && c.Mode == config.AgentModePoll
}

// LastSeen returns the last agent heartbeat time in poll mode (zero if none).
func (c *Client) LastSeen() (time.Time, string, error) {
	if !c.IsPollMode() {
		return time.Time{}, "", nil
	}
	cache, err := c.requireCache()
	if err != nil {
		return time.Time{}, "", err
	}
	return cache.LastSeenAt, cache.OS, nil
}

func (c *Client) requireCache() (*store.AgentCache, error) {
	if c.Store == nil {
		return nil, fmt.Errorf("agent cache unavailable")
	}
	cache, err := c.Store.GetAgentCache()
	if err != nil {
		return nil, err
	}
	if cache == nil || cache.LastSeenAt.IsZero() {
		return nil, fmt.Errorf("agent has not checked in yet (poll mode)")
	}
	return cache, nil
}

func (c *Client) Health() (map[string]any, error) {
	if c.IsPollMode() {
		cache, err := c.requireCache()
		if err != nil {
			return nil, err
		}
		if cache.Health != nil {
			out := cloneMap(cache.Health)
			out["as_of"] = cache.LastSeenAt.UTC().Format(time.RFC3339)
			out["agent_os"] = cache.OS
			return out, nil
		}
		return map[string]any{
			"status":   "ok",
			"service":  "copper_bmc",
			"as_of":    cache.LastSeenAt.UTC().Format(time.RFC3339),
			"agent_os": cache.OS,
			"mode":     "poll",
			"dhcp": map[string]any{
				"role_enabled":   cache.DHCPRole,
				"running":        cache.DHCPRunning,
				"config_enabled": cache.DHCPConfigEnabled,
			},
			"lldp": map[string]any{
				"role_enabled": cache.LLDPRole,
				"running":      cache.LLDPRunning,
				"init_error":   cache.LLDPInitError,
			},
		}, nil
	}
	var out map[string]any
	err := c.do(http.MethodGet, "/v1/health", nil, nil, &out)
	return out, err
}

// HealthQuick is Health with a short timeout for background watchers.
func (c *Client) HealthQuick() (map[string]any, error) {
	if c.IsPollMode() {
		return c.Health()
	}
	client := *c
	client.HTTPClient = &http.Client{Timeout: 3 * time.Second}
	return client.Health()
}

// DHCPRoleEnabled reports whether the agent was started with the DHCP role.
func (c *Client) DHCPRoleEnabled() (bool, error) {
	if c.IsPollMode() {
		cache, err := c.requireCache()
		if err != nil {
			return false, err
		}
		return cache.DHCPRole, nil
	}
	h, err := c.Health()
	if err != nil {
		return false, err
	}
	dhcp, _ := h["dhcp"].(map[string]any)
	if dhcp == nil {
		return false, nil
	}
	v, _ := dhcp["role_enabled"].(bool)
	return v, nil
}

func (c *Client) GetDHCPConfig() (map[string]any, error) {
	if c.IsPollMode() {
		cache, err := c.requireCache()
		if err != nil {
			return nil, err
		}
		out := map[string]any{
			"role_enabled": true,
			"running":      cache.DHCPRunning,
			"as_of":        cache.LastSeenAt.UTC().Format(time.RFC3339),
		}
		if cache.DHCPConfig != nil {
			out["config"] = cache.DHCPConfig
		}
		return out, nil
	}
	var out map[string]any
	err := c.do(http.MethodGet, "/v1/dhcp/config", nil, nil, &out)
	return out, err
}

func (c *Client) PutDHCPConfig(cfg copper_dhcp.Config) (map[string]any, error) {
	var out map[string]any
	err := c.do(http.MethodPut, "/v1/dhcp/config", nil, cfg, &out)
	return out, err
}

// ClearDHCPLeases asks copper_bmc to wipe OOB DHCP leases (listen dial or poll enqueue).
func (c *Client) ClearDHCPLeases() (map[string]any, error) {
	var out map[string]any
	err := c.do(http.MethodDelete, "/v1/dhcp/leases", nil, nil, &out)
	return out, err
}

func (c *Client) DHCPLeases() (map[string]any, error) {
	if c.IsPollMode() {
		cache, err := c.requireCache()
		if err != nil {
			return nil, err
		}
		leases := make([]map[string]any, 0, len(cache.Leases))
		for _, l := range cache.Leases {
			leases = append(leases, map[string]any{
				"mac":        l.MAC,
				"ip":         l.IP,
				"expires_at": l.ExpiresAt,
				"reserved":   l.Reserved,
			})
		}
		return map[string]any{
			"leases": leases,
			"as_of":  cache.LastSeenAt.UTC().Format(time.RFC3339),
		}, nil
	}
	var out map[string]any
	err := c.do(http.MethodGet, "/v1/dhcp/leases", nil, nil, &out)
	return out, err
}

// ActivityEvent is one copper_bmc activity line.
type ActivityEvent struct {
	ID        uint64 `json:"id"`
	Time      string `json:"time"`
	Component string `json:"component"`
	Level     string `json:"level"`
	Message   string `json:"message"`
}

// ActivityResponse is the agent GET /v1/activity payload.
type ActivityResponse struct {
	Events    []ActivityEvent `json:"events"`
	NextSince uint64          `json:"next_since"`
	AsOf      string          `json:"as_of,omitempty"`
}

// Activity fetches agent activity logs. since=0 returns a recent snapshot.
func (c *Client) Activity(since uint64, limit int, component string) (*ActivityResponse, error) {
	if c.IsPollMode() {
		cache, err := c.requireCache()
		if err != nil {
			return nil, err
		}
		comp := strings.ToLower(strings.TrimSpace(component))
		var events []ActivityEvent
		var next uint64 = since
		for _, ev := range cache.Activity {
			if ev.ID <= since {
				continue
			}
			if comp != "" && strings.ToLower(ev.Component) != comp {
				continue
			}
			events = append(events, ActivityEvent{
				ID: ev.ID, Time: ev.Time, Component: ev.Component,
				Level: ev.Level, Message: ev.Message,
			})
			if ev.ID > next {
				next = ev.ID
			}
		}
		if limit > 0 && len(events) > limit {
			events = events[len(events)-limit:]
		}
		if events == nil {
			events = []ActivityEvent{}
		}
		if next == since && cache.ActivityNextSince > next {
			next = cache.ActivityNextSince
		}
		return &ActivityResponse{
			Events:    events,
			NextSince: next,
			AsOf:      cache.LastSeenAt.UTC().Format(time.RFC3339),
		}, nil
	}
	q := url.Values{}
	if since > 0 {
		q.Set("since", strconv.FormatUint(since, 10))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if component != "" {
		q.Set("component", component)
	}
	path := "/v1/activity"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	var out ActivityResponse
	err := c.do(http.MethodGet, path, nil, nil, &out)
	if err != nil {
		return nil, err
	}
	if out.Events == nil {
		out.Events = []ActivityEvent{}
	}
	return &out, nil
}

// LookupARP asks copper_bmc (on the OOB plane) to resolve ip → MAC.
func (c *Client) LookupARP(ip string) (string, error) {
	var out struct {
		MAC string `json:"mac"`
	}
	path := "/v1/net/arp?" + url.Values{"ip": {ip}}.Encode()
	err := c.do(http.MethodGet, path, nil, nil, &out)
	if err != nil {
		return "", err
	}
	if out.MAC == "" {
		return "", fmt.Errorf("agent returned empty MAC")
	}
	return out.MAC, nil
}

// LLDPNeighbor is one sighted LLDP speaker from copper_bmc.
type LLDPNeighbor struct {
	ChassisID      string   `json:"chassis_id"`
	ChassisSubtype string   `json:"chassis_subtype"`
	PortID         string   `json:"port_id"`
	PortSubtype    string   `json:"port_subtype"`
	TTL            uint16   `json:"ttl"`
	SysName        string   `json:"sys_name,omitempty"`
	SysDescription string   `json:"sys_description,omitempty"`
	MgmtAddrs      []string `json:"mgmt_addrs,omitempty"`
	SrcMAC         string   `json:"src_mac"`
	Iface          string   `json:"iface"`
	LastSeen       string   `json:"last_seen"`
}

// LLDPNeighbors lists all recent LLDP neighbors on the agent OOB NIC.
func (c *Client) LLDPNeighbors() ([]LLDPNeighbor, error) {
	if c.IsPollMode() {
		cache, err := c.requireCache()
		if err != nil {
			return nil, err
		}
		out := make([]LLDPNeighbor, 0, len(cache.Neighbors))
		for _, n := range cache.Neighbors {
			out = append(out, LLDPNeighbor{
				ChassisID: n.ChassisID, ChassisSubtype: n.ChassisSubtype,
				PortID: n.PortID, PortSubtype: n.PortSubtype, TTL: n.TTL,
				SysName: n.SysName, SysDescription: n.SysDescription,
				MgmtAddrs: n.MgmtAddrs, SrcMAC: n.SrcMAC, Iface: n.Iface, LastSeen: n.LastSeen,
			})
		}
		return out, nil
	}
	var out struct {
		Neighbors []LLDPNeighbor `json:"neighbors"`
	}
	err := c.do(http.MethodGet, "/v1/net/lldp", nil, nil, &out)
	if err != nil {
		return nil, err
	}
	if out.Neighbors == nil {
		out.Neighbors = []LLDPNeighbor{}
	}
	return out.Neighbors, nil
}

// LookupLLDP finds one neighbor by mac, ip, or chassis_id (first non-empty wins).
func (c *Client) LookupLLDP(mac, ip, chassisID string) (*LLDPNeighbor, error) {
	if c.IsPollMode() {
		neighbors, err := c.LLDPNeighbors()
		if err != nil {
			return nil, err
		}
		mac = strings.ToLower(strings.TrimSpace(mac))
		ip = strings.TrimSpace(ip)
		chassisID = strings.TrimSpace(chassisID)
		for i := range neighbors {
			n := &neighbors[i]
			if mac != "" && strings.EqualFold(n.SrcMAC, mac) {
				return n, nil
			}
			if ip != "" {
				for _, a := range n.MgmtAddrs {
					if a == ip {
						return n, nil
					}
				}
			}
			if chassisID != "" && strings.EqualFold(n.ChassisID, chassisID) {
				return n, nil
			}
		}
		return nil, fmt.Errorf("no LLDP neighbor in last agent cache (as of check-in)")
	}
	q := url.Values{}
	switch {
	case strings.TrimSpace(mac) != "":
		q.Set("mac", strings.TrimSpace(mac))
	case strings.TrimSpace(ip) != "":
		q.Set("ip", strings.TrimSpace(ip))
	case strings.TrimSpace(chassisID) != "":
		q.Set("chassis_id", strings.TrimSpace(chassisID))
	default:
		return nil, fmt.Errorf("mac, ip, or chassis_id required")
	}
	var out struct {
		Neighbor LLDPNeighbor `json:"neighbor"`
	}
	err := c.do(http.MethodGet, "/v1/net/lldp?"+q.Encode(), nil, nil, &out)
	if err != nil {
		return nil, err
	}
	if out.Neighbor.ChassisID == "" && out.Neighbor.SrcMAC == "" {
		return nil, fmt.Errorf("empty LLDP neighbor")
	}
	return &out.Neighbor, nil
}

// Probe verifies BMC reachability + credentials via a light Redfish boot read.
// Uses a shorter timeout than full inventory.
func (c *Client) Probe(srv *store.Server) (json.RawMessage, error) {
	client := *c
	client.HTTPClient = &http.Client{Timeout: 45 * time.Second}
	var out json.RawMessage
	err := client.do(http.MethodGet, "/v1/system/boot", srv, nil, &out)
	return out, err
}

func (c *Client) Inventory(srv *store.Server) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.do(http.MethodGet, "/v1/system", srv, nil, &out)
	return out, err
}

func (c *Client) Boot(srv *store.Server) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.do(http.MethodGet, "/v1/system/boot", srv, nil, &out)
	return out, err
}

func (c *Client) SetBoot(srv *store.Server, body any) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.do(http.MethodPost, "/v1/system/boot", srv, body, &out)
	return out, err
}

func (c *Client) VirtualMedia(srv *store.Server) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.do(http.MethodGet, "/v1/system/virtual-media", srv, nil, &out)
	return out, err
}

// MediaFile is one path under copper_bmc's OOB media folder.
type MediaFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// MediaCatalog is GET /v1/media/files from copper_bmc.
type MediaCatalog struct {
	Folder  string      `json:"folder"`
	BaseURL string      `json:"base_url"`
	Bind    string      `json:"bind"`
	Iface   string      `json:"interface"`
	Files   []MediaFile `json:"files"`
	Note    string      `json:"note,omitempty"`
	AsOf    string      `json:"as_of,omitempty"`
}

// MediaFiles lists ISOs/images served on the agent's OOB media plane.
func (c *Client) MediaFiles() (*MediaCatalog, error) {
	if c.IsPollMode() {
		cache, err := c.requireCache()
		if err != nil {
			return nil, err
		}
		out := &MediaCatalog{
			Folder:  cache.MediaFolder,
			BaseURL: cache.MediaBaseURL,
			Bind:    cache.MediaBind,
			Iface:   cache.MediaIface,
			Note:    cache.MediaNote,
			AsOf:    cache.LastSeenAt.UTC().Format(time.RFC3339),
			Files:   []MediaFile{},
		}
		for _, f := range cache.MediaFiles {
			out.Files = append(out.Files, MediaFile{Path: f.Path, Size: f.Size})
		}
		if out.Note == "" {
			out.Note = "from last agent heartbeat (as of " + out.AsOf + ")"
		}
		return out, nil
	}
	var out MediaCatalog
	err := c.do(http.MethodGet, "/v1/media/files", nil, nil, &out)
	if err != nil {
		return nil, err
	}
	if out.Files == nil {
		out.Files = []MediaFile{}
	}
	return &out, nil
}

func (c *Client) MountMedia(srv *store.Server, body any) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.do(http.MethodPost, "/v1/system/virtual-media/mount", srv, body, &out)
	return out, err
}

// EjectVirtualMedia asks the agent to eject every inserted VirtualMedia slot on the BMC.
func (c *Client) EjectVirtualMedia(srv *store.Server) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.do(http.MethodPost, "/v1/system/virtual-media/eject", srv, map[string]any{}, &out)
	return out, err
}

func (c *Client) Reset(srv *store.Server, body any) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.do(http.MethodPost, "/v1/system/actions/reset", srv, body, &out)
	return out, err
}

// ConvertToUEFI stages persistent BIOS boot mode = UEFI (and optionally reboots).
func (c *Client) ConvertToUEFI(srv *store.Server, body any) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.do(http.MethodPost, "/v1/system/bios/convert-uefi", srv, body, &out)
	return out, err
}

func (c *Client) do(method, path string, srv *store.Server, body any, dest any) error {
	var bodyBytes []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyBytes = b
	}

	headers := map[string]string{}
	if body != nil {
		headers["Content-Type"] = "application/json"
	}
	if srv != nil {
		headers["X-Copper-BMC-Endpoint"] = srv.Endpoint
		headers["X-Copper-BMC-Username"] = srv.Username
		headers["X-Copper-BMC-Password"] = srv.Password
		headers["X-Copper-BMC-Insecure"] = strconv.FormatBool(srv.Insecure)
	}

	if c.IsPollMode() {
		wait := 180 * time.Second
		if c.HTTPClient != nil && c.HTTPClient.Timeout > 0 {
			wait = c.HTTPClient.Timeout
		}
		status, data, err := c.Poll.EnqueueAndWait(method, path, headers, bodyBytes, wait)
		if err != nil {
			return fmt.Errorf("agent poll: %w", err)
		}
		return decodeAgentResponse(status, data, dest)
	}

	var rdr io.Reader
	if bodyBytes != nil {
		rdr = bytes.NewReader(bodyBytes)
	}
	req, err := http.NewRequest(method, c.BaseURL+path, rdr)
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("agent request: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return decodeAgentResponse(resp.StatusCode, data, dest)
}

func decodeAgentResponse(status int, data []byte, dest any) error {
	if status >= 300 {
		var errBody struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &errBody)
		if errBody.Error != "" {
			return fmt.Errorf("agent %d: %s", status, errBody.Error)
		}
		return fmt.Errorf("agent %d: %s", status, string(data))
	}
	if dest == nil {
		return nil
	}
	return json.Unmarshal(data, dest)
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
