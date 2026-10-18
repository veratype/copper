package copper_dhcp

import (
	"fmt"
	"log"
	"sync"
)

// Manager owns an optional running Server so config can be applied at runtime
// (control plane pushes policy; agent starts/stops without restart).
type Manager struct {
	mu         sync.Mutex
	logger     *log.Logger
	raw        Config
	server     *Server
	running    bool
	leaseFile  string // optional JSON persistence for OOB leases across restarts
	configFile string // optional JSON persistence for last applied Config
}

func NewManager() *Manager {
	return &Manager{logger: log.Default()}
}

func (m *Manager) SetLogger(l *log.Logger) {
	if l != nil {
		m.logger = l
	}
}

// SetLeaseFile enables JSON lease persistence at path (created on first write).
// Call before Apply. Empty path disables persistence.
func (m *Manager) SetLeaseFile(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.leaseFile = path
}

// SetConfigFile enables saving the last applied Config to path (and loading it
// via ApplySaved). Call before Apply / ApplySaved.
func (m *Manager) SetConfigFile(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.configFile = path
}

// ApplySaved loads configFile (if set) and Applies it. No-op if missing.
// Returns whether a config was found and applied.
func (m *Manager) ApplySaved() (bool, error) {
	m.mu.Lock()
	path := m.configFile
	m.mu.Unlock()
	cfg, ok, err := LoadConfigFile(path)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	if err := m.Apply(cfg); err != nil {
		return true, err
	}
	return true, nil
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
	path := m.leaseFile
	m.mu.Unlock()
	if srv != nil {
		return srv.Store().Leases()
	}
	leases, err := LoadLeasesFile(path)
	if err != nil {
		m.logger.Printf("dhcp lease file read: %v", err)
		return nil
	}
	return leases
}

// Apply validates and (re)starts or stops the DHCP server from cfg.
// cfg.Enabled=false stops the listener and stores the config.
func (m *Manager) Apply(cfg Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !cfg.Enabled {
		m.stopLocked()
		m.raw = cfg
		m.persistConfigLocked(cfg)
		m.logger.Printf("dhcp stopped (disabled by config)")
		return nil
	}

	// Force Enabled for Normalize; Validate pool/MAC policy.
	n, err := cfg.Normalize()
	if err != nil {
		return err
	}

	m.stopLocked()

	store := NewStore(n)
	if m.leaseFile != "" {
		store.SetPersistPath(m.leaseFile)
		if err := store.Load(); err != nil {
			m.logger.Printf("dhcp lease load from %s: %v", m.leaseFile, err)
		} else if restored := len(store.Leases()); restored > 0 {
			m.logger.Printf("dhcp restored %d lease(s) from %s", restored, m.leaseFile)
		}
	}

	srv := &Server{
		cfg:    n,
		store:  store,
		logger: m.logger,
	}
	if err := srv.Listen(); err != nil {
		return err
	}
	m.server = srv
	m.raw = cfg
	m.running = true
	m.persistConfigLocked(cfg)

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

func (m *Manager) persistConfigLocked(cfg Config) {
	if m.configFile == "" {
		return
	}
	if err := SaveConfigFile(m.configFile, cfg); err != nil {
		m.logger.Printf("dhcp config save to %s: %v", m.configFile, err)
	}
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
