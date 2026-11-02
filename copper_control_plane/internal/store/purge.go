package store

import "fmt"

// PurgeInventoryAndDHCP removes servers, labels, snapshots, jobs, Data DHCP
// leases, poll-mode agent command queue / heartbeat cache, and saved OOB +
// Data DHCP policies. Keeps operational settings (session, live-OS SSH,
// data media, batch runtime, agent_mode).
func (s *Store) PurgeInventoryAndDHCP() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmts := []string{
		`DELETE FROM server_labels`,
		`DELETE FROM snapshots`,
		`DELETE FROM servers`,
		`DELETE FROM labels`,
		`DELETE FROM jobs`,
		`DELETE FROM data_dhcp_leases`,
		`DELETE FROM agent_commands`,
		`DELETE FROM settings WHERE key IN (?, ?, ?, ?)`,
	}
	for i, q := range stmts {
		var execErr error
		if i == len(stmts)-1 {
			_, execErr = tx.Exec(q, settingAgentDHCP, settingLocalDHCP, settingAgentCache, settingDataLeaseBindings)
		} else {
			_, execErr = tx.Exec(q)
		}
		if execErr != nil {
			return fmt.Errorf("purge step %d: %w", i, execErr)
		}
	}
	return tx.Commit()
}
