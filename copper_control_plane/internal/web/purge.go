package web

import (
	"fmt"
	"net/http"
	"strings"

	"copper_dhcp"
)

const purgeConfirmPhrase = "PURGE"

// handleSettingsPurgePost wipes inventory + DHCP state on the control plane and
// best-effort clears OOB DHCP on copper_bmc. Uses agent.Client so both listen
// (direct dial) and poll (enqueue + wait) modes are honored.
func (s *Server) handleSettingsPurgePost(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	confirm := strings.TrimSpace(r.FormValue("confirm"))
	if !strings.EqualFold(confirm, purgeConfirmPhrase) {
		rt, _ := s.store.GetBatchRuntime()
		live, _ := s.store.GetLiveOSSSH()
		dm, _ := s.store.GetDataMedia()
		sess, _ := s.store.GetSessionSettings()
		s.render(w, "settings.html", s.settingsPageData(rt, live, dm, sess,
			`Type PURGE in the confirm box to wipe inventory and DHCP data`))
		return
	}

	notes := []string{}

	// 1) Stop Data DHCP in this process and clear its leases.
	if s.localDHCP != nil {
		disabled := copper_dhcp.Config{Enabled: false}
		if err := s.localDHCP.Apply(disabled); err != nil {
			notes = append(notes, "data DHCP stop: "+err.Error())
		} else {
			notes = append(notes, "data DHCP stopped")
		}
		if err := s.localDHCP.ClearLeases(); err != nil {
			notes = append(notes, "data DHCP leases: "+err.Error())
		} else {
			notes = append(notes, "data DHCP leases cleared")
		}
	}

	// 2) Push disabled OOB policy + clear leases on copper_bmc (listen or poll).
	oobNotes := s.purgeAgentOOBDHCP()
	notes = append(notes, oobNotes...)

	// 3) Wipe SQLite inventory / DHCP / jobs / poll queue.
	if err := s.store.PurgeInventoryAndDHCP(); err != nil {
		rt, _ := s.store.GetBatchRuntime()
		live, _ := s.store.GetLiveOSSSH()
		dm, _ := s.store.GetDataMedia()
		sess, _ := s.store.GetSessionSettings()
		s.render(w, "settings.html", s.settingsPageData(rt, live, dm, sess,
			"Database purge failed: "+err.Error()+" ("+strings.Join(notes, "; ")+")"))
		return
	}
	notes = append(notes, "control-plane DB purged (servers, labels, snapshots, jobs, DHCP policies/leases, agent poll cache/commands)")

	if s.jobs != nil {
		s.jobs.PurgeMemory()
		notes = append(notes, "in-memory jobs cleared")
	}

	s.claimTakenMu.Lock()
	s.claimTakenByIP = map[int64]string{}
	s.claimTakenMu.Unlock()

	flash := "Purge complete. " + strings.Join(notes, "; ")
	http.Redirect(w, r, "/settings?flash="+urlQuery(flash), http.StatusSeeOther)
}

func (s *Server) purgeAgentOOBDHCP() []string {
	var notes []string
	if s.agent == nil {
		return []string{"copper_bmc: no agent client configured"}
	}

	role, err := s.agent.DHCPRoleEnabled()
	if err != nil {
		mode := "listen"
		if s.agent.IsPollMode() {
			mode = "poll"
		}
		notes = append(notes, fmt.Sprintf("copper_bmc (%s): unreachable — %v (OOB DHCP on agent not cleared)", mode, err))
		return notes
	}
	if !role {
		notes = append(notes, "copper_bmc: DHCP role disabled — skipped OOB config/leases clear")
		return notes
	}

	disabled := copper_dhcp.Config{Enabled: false}
	if err := s.pushDHCP(disabled); err != nil {
		notes = append(notes, "OOB DHCP disable push: "+err.Error())
	} else if s.agent.IsPollMode() {
		notes = append(notes, "OOB DHCP disabled on agent (poll)")
	} else {
		notes = append(notes, "OOB DHCP disabled on agent (listen)")
	}

	if _, err := s.agent.ClearDHCPLeases(); err != nil {
		notes = append(notes, "OOB DHCP leases clear: "+err.Error())
	} else {
		notes = append(notes, "OOB DHCP leases cleared on agent")
	}
	return notes
}
