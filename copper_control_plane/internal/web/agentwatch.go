package web

import (
	"log"
	"sync"
	"time"
)

const (
	agentWatchInterval = 5 * time.Second
	agentRepushCooldown = 30 * time.Second
)

// StartAgentWatch periodically checks copper_bmc health and re-pushes the
// saved OOB DHCP policy when the agent is up with the DHCP role but DHCP is
// not running (typical after a BMC restart before/without local restore).
func (s *Server) StartAgentWatch() {
	go s.agentWatchLoop()
}

func (s *Server) agentWatchLoop() {
	// Immediate attempt so we don't wait a full tick after CP startup.
	s.reconcileAgentDHCP(true)

	t := time.NewTicker(agentWatchInterval)
	defer t.Stop()
	for range t.C {
		s.reconcileAgentDHCP(false)
	}
}

type agentWatchState struct {
	mu        sync.Mutex
	wasUp     bool
	lastPush  time.Time
	lastNote  string // surfaced on OOB DHCP page
}

func (s *Server) reconcileAgentDHCP(startup bool) {
	h, err := s.agent.HealthQuick()
	if err != nil {
		s.watch.mu.Lock()
		if s.watch.wasUp || startup {
			log.Printf("agent watch: copper_bmc unreachable: %v", err)
		}
		s.watch.wasUp = false
		s.watch.lastNote = "Agent unreachable — will auto-repush OOB DHCP when it returns"
		s.watch.mu.Unlock()
		return
	}

	dhcp, _ := h["dhcp"].(map[string]any)
	role, _ := dhcp["role_enabled"].(bool)
	running, _ := dhcp["running"].(bool)

	s.watch.mu.Lock()
	recovered := !s.watch.wasUp
	s.watch.wasUp = true
	if !role {
		s.watch.lastNote = ""
		s.watch.mu.Unlock()
		return
	}
	s.watch.mu.Unlock()

	if recovered && !startup {
		log.Printf("agent watch: copper_bmc is back")
	}

	cfg, err := s.store.GetAgentDHCPConfig()
	if err != nil {
		log.Printf("agent watch: load saved OOB DHCP config: %v", err)
		return
	}
	if !cfg.Enabled {
		s.watch.mu.Lock()
		s.watch.lastNote = ""
		s.watch.mu.Unlock()
		return
	}

	if running {
		s.watch.mu.Lock()
		s.watch.lastNote = ""
		s.watch.mu.Unlock()
		return
	}

	s.watch.mu.Lock()
	cooled := time.Since(s.watch.lastPush) >= agentRepushCooldown
	shouldPush := recovered || startup || cooled
	s.watch.mu.Unlock()
	if !shouldPush {
		return
	}

	if err := s.pushDHCP(cfg); err != nil {
		log.Printf("agent watch: auto-repush OOB DHCP failed: %v", err)
		s.watch.mu.Lock()
		s.watch.lastNote = "Agent DHCP is not running — auto-repush failed: " + err.Error()
		s.watch.mu.Unlock()
		return
	}

	s.watch.mu.Lock()
	s.watch.lastPush = time.Now()
	s.watch.lastNote = "Auto-repushed saved OOB DHCP config to agent (DHCP was not running)"
	s.watch.mu.Unlock()
	log.Printf("agent watch: auto-repushed OOB DHCP config to copper_bmc")
}

func (s *Server) agentWatchNote() string {
	s.watch.mu.Lock()
	defer s.watch.mu.Unlock()
	return s.watch.lastNote
}
