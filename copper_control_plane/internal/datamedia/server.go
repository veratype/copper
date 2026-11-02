// Package datamedia serves install files (cloud images, ISOs) on the data plane
// from copper_control_plane — analogous to copper_bmc's OOB media server.
package datamedia

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"copper_control_plane/internal/activity"
)

const DefaultPort = 8081

// Config is persisted in Settings and applied at runtime.
type Config struct {
	Enabled bool   `json:"enabled"`
	Port    int    `json:"port"`
	Folder  string `json:"folder"`
	Bind    string `json:"bind"` // listen address; default 0.0.0.0
}

func DefaultConfig() Config {
	return Config{Enabled: false, Port: DefaultPort, Bind: "0.0.0.0"}
}

func (c Config) Normalize() Config {
	out := c
	out.Folder = strings.TrimSpace(out.Folder)
	out.Bind = strings.TrimSpace(out.Bind)
	if out.Bind == "" {
		out.Bind = "0.0.0.0"
	}
	if out.Port <= 0 || out.Port > 65535 {
		out.Port = DefaultPort
	}
	return out
}

func (c Config) Validate() error {
	c = c.Normalize()
	if !c.Enabled {
		return nil
	}
	if c.Folder == "" {
		return fmt.Errorf("data media folder is required when enabled")
	}
	info, err := os.Stat(c.Folder)
	if err != nil {
		return fmt.Errorf("data media folder: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("data media folder is not a directory: %s", c.Folder)
	}
	return nil
}

func (c Config) Addr() string {
	c = c.Normalize()
	return net.JoinHostPort(c.Bind, strconv.Itoa(c.Port))
}

// Status is the live listener state.
type Status struct {
	Config  Config `json:"config"`
	Running bool   `json:"running"`
	Addr    string `json:"addr,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
	Error   string `json:"error,omitempty"`
	Note    string `json:"note,omitempty"`
}

// Manager owns the optional data-plane HTTP file server.
type Manager struct {
	mu      sync.Mutex
	cfg     Config
	srv     *http.Server
	ln      net.Listener
	act     *activity.Buffer
	lastErr string
	digests *digestCache
	// ifaceHint is optional NIC name (e.g. Data DHCP interface) for BaseURL.
	ifaceHint string
}

func New(act *activity.Buffer) *Manager {
	if act == nil {
		act = activity.New(0)
	}
	return &Manager{cfg: DefaultConfig(), act: act, digests: newDigestCache()}
}

// SetIfaceHint helps build a host-reachable BaseURL when bind is 0.0.0.0.
func (m *Manager) SetIfaceHint(iface string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ifaceHint = strings.TrimSpace(iface)
}

func (m *Manager) Config() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{
		Config:  m.cfg,
		Running: m.ln != nil,
		Error:   m.lastErr,
	}
	if st.Running {
		st.Addr = m.cfg.Addr()
	}
	st.BaseURL = m.baseURLLocked()
	if st.BaseURL == "" && st.Running {
		st.Note = "bind is 0.0.0.0 — set a concrete Bind IP in Settings so live hosts can reach the image URL, or ensure Data DHCP interface has an IPv4"
	}
	return st
}

// Apply starts, restarts, or stops the media server to match cfg.
func (m *Manager) Apply(cfg Config) error {
	cfg = cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.stopLocked()
	m.cfg = cfg
	m.lastErr = ""

	if !cfg.Enabled {
		m.act.Info("datamedia", "disabled")
		return nil
	}

	ln, err := net.Listen("tcp", cfg.Addr())
	if err != nil {
		m.lastErr = err.Error()
		m.act.Error("datamedia", "listen %s failed: %v", cfg.Addr(), err)
		return fmt.Errorf("data media listen on %s: %w", cfg.Addr(), err)
	}
	handler := m.handlerLocked(cfg.Folder)
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	m.ln = ln
	m.srv = srv
	m.act.Info("datamedia", "serving %s on http://%s/ (data plane)", cfg.Folder, cfg.Addr())

	go func() {
		err := srv.Serve(ln)
		if err != nil && err != http.ErrServerClosed {
			m.mu.Lock()
			m.lastErr = err.Error()
			m.ln = nil
			m.srv = nil
			m.mu.Unlock()
			m.act.Error("datamedia", "serve stopped: %v", err)
		}
	}()
	m.WarmDigests()
	return nil
}

func (m *Manager) stopLocked() {
	if m.srv != nil {
		_ = m.srv.Close()
		m.srv = nil
	}
	if m.ln != nil {
		_ = m.ln.Close()
		m.ln = nil
	}
}

func (m *Manager) handlerLocked(folder string) http.Handler {
	root := http.Dir(folder)
	fs := http.FileServer(root)
	act := m.act
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		act.Info("datamedia", "%s %s %s", r.RemoteAddr, r.Method, r.URL.Path)
		w.Header().Set("Cache-Control", "no-store")
		if isDownloadableFile(folder, r.URL.Path) {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			// Content hash for provision retry (skip rewrite when target already matches).
			// Only attach when already cached — never block an image download on hashing.
			if d, ok := m.CachedDigestForURLPath(r.URL.Path); ok && d.SHA256 != "" {
				w.Header().Set("X-Copper-SHA256", d.SHA256)
				w.Header().Set("X-Copper-Size", strconv.FormatInt(d.Size, 10))
				w.Header().Set("Digest", "sha-256="+d.SHA256)
			}
		}
		fs.ServeHTTP(w, r)
	})
}

func isDownloadableFile(folder, urlPath string) bool {
	rel := strings.TrimPrefix(urlPath, "/")
	if rel == "" {
		return false
	}
	full := filepath.Join(folder, filepath.FromSlash(rel))
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		return false
	}
	return true
}

func (m *Manager) baseURLLocked() string {
	cfg := m.cfg
	if !cfg.Enabled && m.ln == nil {
		return ""
	}
	bind := cfg.Bind
	port := cfg.Port
	if bind != "" && bind != "0.0.0.0" {
		if ip := net.ParseIP(bind); ip != nil && ip.To4() != nil {
			return "http://" + net.JoinHostPort(bind, strconv.Itoa(port))
		}
	}
	ifaceName := m.ifaceHint
	if ifaceName == "" || ifaceName == "all" || ifaceName == "custom" {
		return ""
	}
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return ""
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() || ip.To4() == nil {
			continue
		}
		return "http://" + net.JoinHostPort(ip.String(), strconv.Itoa(port))
	}
	return ""
}
