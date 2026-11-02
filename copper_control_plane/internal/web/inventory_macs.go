package web

import (
	"encoding/json"
	"fmt"
	"strings"
)

// inventoryNIC is one host (system) NIC MAC from a cached Redfish inventory.
type inventoryNIC struct {
	ServerID   int64
	ServerName string
	MAC        string
	NICName    string
	Hardware   string // manufacturer model · serial
}

// listInventoryHostNICs returns unique host NIC MACs from cached inventory
// snapshots. Requires Probe + Inventory; empty when none have been run.
// MACs are harvested from the whole inventory JSON except the bmc subtree
// (network, pcie, nested adapters, …).
func (s *Server) listInventoryHostNICs() []inventoryNIC {
	servers, err := s.store.ListServers()
	if err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []inventoryNIC
	for _, srv := range servers {
		snap, err := s.store.GetSnapshot(srv.ID, "inventory")
		if err != nil || snap == nil || strings.TrimSpace(snap.Payload) == "" {
			continue
		}
		hw, nics := parseInventoryHostNICs(snap.Payload)
		for _, n := range nics {
			mac, err := normalizeMACInput(n.mac)
			if err != nil || mac == "" || isZeroMAC(mac) {
				continue
			}
			key := strings.ToLower(mac)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			nicName := strings.TrimSpace(n.name)
			if nicName == "" {
				nicName = n.id
			}
			out = append(out, inventoryNIC{
				ServerID:   srv.ID,
				ServerName: srv.Name,
				MAC:        mac,
				NICName:    nicName,
				Hardware:   hw,
			})
		}
	}
	return out
}

type rawInvNIC struct {
	mac  string
	name string
	id   string
}

// parseInventoryHostNICs extracts display hardware and every MAC found anywhere
// in the inventory JSON. Named entries from network[] are preferred for labels;
// additional MACs (pcie.mac_addresses, bmc, nested objects, …) are appended.
func parseInventoryHostNICs(payload string) (hardware string, nics []rawInvNIC) {
	var inv struct {
		System struct {
			Manufacturer string `json:"manufacturer"`
			Model        string `json:"model"`
			SerialNumber string `json:"serial_number"`
		} `json:"system"`
		Network []struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			MACAddress   string `json:"mac_address"`
			PermanentMAC string `json:"permanent_mac"`
		} `json:"network"`
	}
	if err := json.Unmarshal([]byte(payload), &inv); err != nil {
		return "", nil
	}
	parts := []string{}
	if m := strings.TrimSpace(inv.System.Manufacturer); m != "" {
		parts = append(parts, m)
	}
	if m := strings.TrimSpace(inv.System.Model); m != "" {
		parts = append(parts, m)
	}
	hardware = strings.Join(parts, " ")
	if sn := strings.TrimSpace(inv.System.SerialNumber); sn != "" {
		if hardware != "" {
			hardware = fmt.Sprintf("%s · SN %s", hardware, sn)
		} else {
			hardware = "SN " + sn
		}
	}

	seen := map[string]struct{}{}
	add := func(raw, name, id string) {
		mac, err := normalizeMACInput(raw)
		if err != nil || mac == "" || isZeroMAC(mac) {
			return
		}
		key := strings.ToLower(mac)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		nics = append(nics, rawInvNIC{mac: mac, name: name, id: id})
	}

	for _, n := range inv.Network {
		for _, raw := range []string{n.MACAddress, n.PermanentMAC} {
			add(raw, n.Name, n.ID)
		}
	}
	// Generic walk for host NICs wherever vendors stash them (network[],
	// pcie[].mac_addresses, nested adapters, …). BMC / OOB MACs are excluded —
	// those belong to the management plane and must not bind Data DHCP leases.
	for _, mac := range collectHostMACsFromJSON(payload) {
		add(mac, "", "")
	}
	return hardware, nics
}

// collectHostMACsFromJSON walks the inventory document and returns every
// distinct non-zero MAC-shaped string under the host/system plane.
// The "bmc" subtree is skipped so iLO/iDRAC MACs never get treated as data NICs.
func collectHostMACsFromJSON(payload string) []string {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return nil
	}
	var root any
	if err := json.Unmarshal([]byte(payload), &root); err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				// OOB / BMC plane — never use for data-NIC ↔ server mapping.
				if strings.EqualFold(strings.TrimSpace(k), "bmc") {
					continue
				}
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		case string:
			mac, err := normalizeMACInput(x)
			if err != nil || mac == "" || isZeroMAC(mac) {
				return
			}
			key := strings.ToLower(mac)
			if _, ok := seen[key]; ok {
				return
			}
			seen[key] = struct{}{}
			out = append(out, mac)
		}
	}
	walk(root)
	return out
}

// collectMACsFromJSON is kept as an alias for tests / callers that want host MACs.
func collectMACsFromJSON(payload string) []string {
	return collectHostMACsFromJSON(payload)
}

// lookupInventoryNIC finds a host NIC by MAC (case-insensitive).
func lookupInventoryNIC(nics []inventoryNIC, mac string) *inventoryNIC {
	mac, err := normalizeMACInput(mac)
	if err != nil || mac == "" {
		return nil
	}
	key := strings.ToLower(mac)
	for i := range nics {
		if strings.ToLower(nics[i].MAC) == key {
			return &nics[i]
		}
	}
	return nil
}
