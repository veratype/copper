package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"copper_control_plane/internal/store"
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
