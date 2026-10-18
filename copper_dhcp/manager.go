package copper_dhcp

import (
	"fmt"
	"log"
	"sync"
)

// Manager owns an optional running Server so config can be applied at runtime
// (control plane pushes policy; agent starts/stops without restart).
type Manager struct {
	mu      sync.Mutex
	logger  *log.Logger
	raw     Config
	server  *Server
	running bool
}

func NewManager() *Manager {
	return &Manager{logger: log.Default()}
}

func (m *Manager) SetLogger(l *log.Logger) {
	if l != nil {
		m.logger = l
	}
}

// Status is a snapshot for health / UI.
type Status struct {
	Running bool   `json:"running"`
	Config  Config `json:"config"`
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Status{Running: m.running, Config: m.raw}
}

func (m *Manager) Config() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.raw
}

func (m *Manager) Leases() []Lease {
	m.mu.Lock()
	srv := m.server
	m.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Store().Leases()
}

// Apply validates and (re)starts or stops the DHCP server from cfg.
// cfg.Enabled=false stops the listener and stores the config.
func (m *Manager) Apply(cfg Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !cfg.Enabled {
		m.stopLocked()
		m.raw = cfg
		m.logger.Printf("dhcp stopped (disabled by config)")
		return nil
	}

	// Force Enabled for Normalize; Validate pool/MAC policy.
	n, err := cfg.Normalize()
	if err != nil {
		return err
	}

	m.stopLocked()

	srv := &Server{
		cfg:    n,
		store:  NewStore(n),
		logger: m.logger,
	}
	if err := srv.Listen(); err != nil {
		return err
	}
	m.server = srv
	m.raw = cfg
	m.running = true

	go func() {
		if err := srv.Serve(); err != nil {
			m.logger.Printf("dhcp server exited: %v", err)
		}
		m.mu.Lock()
		if m.server == srv {
			m.running = false
		}
		m.mu.Unlock()
	}()
	m.logger.Printf("dhcp applied: pool=%s-%s allow_all=%v", n.RangeStart, n.RangeEnd, n.AllowAll)
	return nil
}

func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
	m.raw.Enabled = false
}

func (m *Manager) stopLocked() {
	if m.server != nil {
		_ = m.server.Close()
		m.server = nil
	}
	m.running = false
}

// ErrNotRunning is returned when leases are requested but DHCP is down.
var ErrNotRunning = fmt.Errorf("dhcp server is not running")
