package web

import (
	"log"
	"sync"
	"time"

	"copper_control_plane/internal/store"
)

const agentRepushCooldown = 30 * time.Second

// StartAgentWatch periodically checks copper_bmc health and re-pushes the
// saved OOB DHCP policy when the agent is up with the DHCP role but DHCP is
// not running (typical after a BMC restart before/without local restore).
// Interval is admin-tunable in Settings (default 60s).
//
// In poll mode, "health" is the last heartbeat cache (as_of last_seen_at);
// DHCP re-push is enqueued for the agent to pull.
func (s *Server) StartAgentWatch() {
	go s.agentWatchLoop()
}

func (s *Server) agentWatchLoop() {
	// Immediate attempt so we don't wait a full tick after CP startup.
	s.reconcileAgentDHCP(true)

	for {
		interval := s.agentWatchInterval()
		timer := time.NewTimer(interval)
		<-timer.C
		timer.Stop()
		s.reconcileAgentDHCP(false)
	}
}

// agentWatchInterval reads the current Settings value each tick so a save
// applies without restarting the control plane.
func (s *Server) agentWatchInterval() time.Duration {
	sec := store.DefaultAgentWatchSeconds
	if rt, err := s.store.GetBatchRuntime(); err == nil {
		sec = rt.Normalize().AgentWatchSeconds
	}
	if sec < store.MinAgentWatchSeconds {
		sec = store.DefaultAgentWatchSeconds
	}
	return time.Duration(sec) * time.Second
}

type agentWatchState struct {
	mu       sync.Mutex
	wasUp    bool
	lastPush time.Time
	lastNote string // surfaced on OOB DHCP page
}

func (s *Server) reconcileAgentDHCP(startup bool) {
	h, err := s.agent.HealthQuick()
	if err != nil {
		s.watch.mu.Lock()
		if s.watch.wasUp || startup {
			if s.cfg.IsPollMode() {
				log.Printf("agent watch: copper_bmc has not checked in (poll): %v", err)
			} else {
				log.Printf("agent watch: copper_bmc unreachable: %v", err)
			}
		}
		s.watch.wasUp = false
		if s.cfg.IsPollMode() {
			s.watch.lastNote = "Agent has not checked in (poll) — will auto-repush OOB DHCP after heartbeat"
		} else {
			s.watch.lastNote = "Agent unreachable — will auto-repush OOB DHCP when it returns"
		}
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
		note := ""
		if s.cfg.IsPollMode() {
			if asOf, _ := h["as_of"].(string); asOf != "" {
				note = "Agent last seen " + asOf + " (poll cache)"
			}
		}
		s.watch.lastNote = note
		s.watch.mu.Unlock()
		return
	}
	s.watch.mu.Unlock()

	if recovered && !startup {
		if s.cfg.IsPollMode() {
			log.Printf("agent watch: copper_bmc checked in (poll)")
		} else {
			log.Printf("agent watch: copper_bmc is back")
		}
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
		if s.cfg.IsPollMode() {
			if asOf, _ := h["as_of"].(string); asOf != "" {
				s.watch.lastNote = "Agent last seen " + asOf + " (poll cache)"
			} else {
				s.watch.lastNote = ""
			}
		} else {
			s.watch.lastNote = ""
		}
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
	if s.cfg.IsPollMode() {
		s.watch.lastNote = "Enqueued OOB DHCP re-push for polling agent"
	} else {
		s.watch.lastNote = "Auto-repushed OOB DHCP to agent"
	}
	s.watch.mu.Unlock()
	log.Printf("agent watch: auto-repushed OOB DHCP policy to copper_bmc")
}

func (s *Server) agentWatchNote() string {
	s.watch.mu.Lock()
	defer s.watch.mu.Unlock()
	return s.watch.lastNote
}
