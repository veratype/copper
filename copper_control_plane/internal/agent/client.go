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

	"copper_control_plane/internal/store"

	"copper_dhcp"
)

// Client talks to copper_bmc's command API, attaching BMC target headers per call.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		BaseURL: baseURL,
		HTTPClient: &http.Client{
			Timeout: 180 * time.Second, // inventory walks cpu/ram/disk/network/gpu
		},
	}
}

func (c *Client) Health() (map[string]any, error) {
	var out map[string]any
	err := c.do(http.MethodGet, "/v1/health", nil, nil, &out)
	return out, err
}

// DHCPRoleEnabled reports whether the agent was started with the DHCP role.
func (c *Client) DHCPRoleEnabled() (bool, error) {
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
	var out map[string]any
	err := c.do(http.MethodGet, "/v1/dhcp/config", nil, nil, &out)
	return out, err
}

func (c *Client) PutDHCPConfig(cfg copper_dhcp.Config) (map[string]any, error) {
	var out map[string]any
	err := c.do(http.MethodPut, "/v1/dhcp/config", nil, cfg, &out)
	return out, err
}

func (c *Client) DHCPLeases() (map[string]any, error) {
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
}

// Activity fetches agent activity logs. since=0 returns a recent snapshot.
func (c *Client) Activity(since uint64, limit int, component string) (*ActivityResponse, error) {
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

func (c *Client) MountMedia(srv *store.Server, body any) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.do(http.MethodPost, "/v1/system/virtual-media/mount", srv, body, &out)
	return out, err
}

func (c *Client) Reset(srv *store.Server, body any) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.do(http.MethodPost, "/v1/system/actions/reset", srv, body, &out)
	return out, err
}

func (c *Client) do(method, path string, srv *store.Server, body any, dest any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, c.BaseURL+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if srv != nil {
		req.Header.Set("X-Copper-BMC-Endpoint", srv.Endpoint)
		req.Header.Set("X-Copper-BMC-Username", srv.Username)
		req.Header.Set("X-Copper-BMC-Password", srv.Password)
		req.Header.Set("X-Copper-BMC-Insecure", strconv.FormatBool(srv.Insecure))
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
	if resp.StatusCode >= 300 {
		var errBody struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &errBody)
		if errBody.Error != "" {
			return fmt.Errorf("agent %s: %s", resp.Status, errBody.Error)
		}
		return fmt.Errorf("agent %s: %s", resp.Status, string(data))
	}
	if dest == nil {
		return nil
	}
	return json.Unmarshal(data, dest)
}
