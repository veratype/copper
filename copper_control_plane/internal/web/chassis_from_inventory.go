package web

import (
	"strings"

	"copper_control_plane/internal/store"
)

// applyChassisIDFromInventory copies Redfish system.serial_number into
// servers.chassis_id when chassis_id is still empty. Copper treats that SN as
// the chassis / asset id used for CSV import and LLDP matching.
func (s *Server) applyChassisIDFromInventory(srv *store.Server, inventoryPayload string) (bool, error) {
	if srv == nil {
		return false, nil
	}
	if strings.TrimSpace(srv.ChassisID) != "" {
		return false, nil
	}
	sn := strings.TrimSpace(inventoryHostIdentity(inventoryPayload).Serial)
	if sn == "" {
		return false, nil
	}
	srv.ChassisID = sn
	if err := s.store.UpdateServer(*srv); err != nil {
		return false, err
	}
	return true, nil
}

// backfillChassisIDFromInventory fills empty chassis_id from a cached inventory
// snapshot (so admins do not need to re-run Inventory after this fix).
func (s *Server) backfillChassisIDFromInventory(srv *store.Server) {
	if srv == nil || strings.TrimSpace(srv.ChassisID) != "" {
		return
	}
	snap, err := s.store.GetSnapshot(srv.ID, "inventory")
	if err != nil || snap == nil || strings.TrimSpace(snap.Payload) == "" {
		return
	}
	_, _ = s.applyChassisIDFromInventory(srv, snap.Payload)
}
