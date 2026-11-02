package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"copper_control_plane/internal/jobs"
	"copper_control_plane/internal/storage"
	"copper_control_plane/internal/store"

	"copper_dhcp"
)

const (
	liveOSClaimSource       = "live_os"
	liveOSClaimSSHTries     = 3 // retries: live OS / DMI / SSH can be flaky per vendor
	liveOSClaimRoundWait    = 20 * time.Second
	liveOSSSHAttemptTimeout = 20 * time.Second
)

// waitAndClaimLiveOSNICs runs inside the mount job (same per-server lock — do
// not take WithServer again). Algorithm:
//
//  1. Data DHCP leases → known IPs (MAC already in this server's inventory) and
//     orphan IPs (MAC not in any inventory).
//  2. SSH (with retries) preferring known leases, then orphans.
//  3. Prove ownership via chassis serial / inventory MACs / system serial / UUID.
//  4. Merge live MACs into this server's inventory network[].
//
// Forgiving: SSH/mismatch failures skip that IP. Mount stays ok if claim fails.
func (s *Server) waitAndClaimLiveOSNICs(ctx context.Context, srv *store.Server, progress jobs.Progress) (int, error) {
	creds, err := s.store.GetLiveOSSSH()
	if err != nil {
		return 0, err
	}
	sc := storage.Creds{Username: creds.Username, Password: creds.Password, Port: creds.Port}
	if err := sc.Validate(); err != nil {
		return 0, fmt.Errorf("live OS SSH not configured: %w", err)
	}

	snap, err := s.store.GetSnapshot(srv.ID, "inventory")
	if err != nil {
		return 0, err
	}
	invPayload := ""
	if snap != nil {
		invPayload = snap.Payload
	}
	ident := inventoryHostIdentity(invPayload)
	known := inventoryMACSet(invPayload)
	if !ident.hasAny() && len(known) == 0 {
		return 0, fmt.Errorf("no inventory MACs/chassis_serial/serial/uuid — run Inventory first")
	}

	if progress != nil {
		progress("live-os", fmt.Sprintf("claim: known_macs=%d chassis_serial=%q serial=%q (≤%d SSH tries per data IP)",
			len(known), ident.primaryChassisSerial(), ident.Serial, liveOSClaimSSHTries))
	}

	tries := map[string]int{} // ip → SSH attempts used

	for round := 1; round <= liveOSClaimSSHTries; round++ {
		if err := ctx.Err(); err != nil {
			return 0, fmt.Errorf("cancelled during NIC claim: %w", err)
		}

		knownIPs, orphanIPs, orphanMACs := s.dataPlaneClaimTargets(known)
		if progress != nil {
			progress("live-os", fmt.Sprintf("round %d/%d: known_ips=%d orphan_ips=%d orphan_macs=%d",
				round, liveOSClaimSSHTries, len(knownIPs), len(orphanIPs), len(orphanMACs)))
		}

		candidates := append([]dataPlaneCandidate{}, knownIPs...)
		candidates = append(candidates, orphanIPs...)
		if len(candidates) == 0 {
			if round < liveOSClaimSSHTries {
				if progress != nil {
					progress("live-os", "no data DHCP candidates yet — waiting for leases")
				}
				if err := sleepCtx(ctx, liveOSClaimRoundWait); err != nil {
					return 0, err
				}
				continue
			}
			return 0, fmt.Errorf("no data DHCP leases for known or orphan MACs")
		}

		for _, c := range candidates {
			if tries[c.IP] >= liveOSClaimSSHTries {
				continue
			}
			tries[c.IP]++
			n := tries[c.IP]
			if progress != nil {
				progress("live-os", fmt.Sprintf("SSH %s (%s) try %d/%d", c.IP, c.Via, n, liveOSClaimSSHTries))
			}

			host := storage.Host{IP: c.IP, Creds: sc}
			probeCtx, cancel := context.WithTimeout(ctx, liveOSSSHAttemptTimeout)
			_, sshErr := storage.RunSSH(probeCtx, host.IP, host.Creds, "echo copper-ok")
			if sshErr != nil {
				cancel()
				if progress != nil {
					progress("live-os", fmt.Sprintf("SSH %s failed: %v", c.IP, sshErr))
				}
				continue
			}

			nics, nicErr := storage.ListNICs(probeCtx, host)
			var dmi liveDMI
			if nicErr == nil {
				dmi = readLiveOSDMI(probeCtx, host)
			}
			cancel()
			if nicErr != nil {
				if progress != nil {
					progress("live-os", fmt.Sprintf("NIC list %s failed: %v", c.IP, nicErr))
				}
				continue
			}

			if !liveHostMatchesServer(nics, dmi, known, ident) {
				if progress != nil {
					progress("live-os", fmt.Sprintf("skip %s — not this server (no chassis_serial/MAC/DMI match)", c.IP))
				}
				tries[c.IP] = liveOSClaimSSHTries
				continue
			}

			added, newPayload, mergeErr := mergeLiveOSNICsIntoInventory(invPayload, nics)
			if mergeErr != nil {
				return 0, mergeErr
			}
			claimedOrphans := countOrphanHits(nics, orphanMACs)
			if added > 0 {
				if err := s.store.SaveSnapshot(srv.ID, "inventory", newPayload); err != nil {
					return 0, fmt.Errorf("save inventory: %w", err)
				}
			}
			s.markClaimTaken(srv.ID, c.IP)
			if progress != nil {
				progress("live-os", fmt.Sprintf("matched via %s; added %d MAC(s) (%d were orphans)", c.Via, added, claimedOrphans))
			}
			return added, nil
		}

		if round < liveOSClaimSSHTries {
			if progress != nil {
				progress("live-os", "round done — brief wait for live OS / DHCP")
			}
			if err := sleepCtx(ctx, liveOSClaimRoundWait); err != nil {
				return 0, err
			}
		}
	}

	return 0, fmt.Errorf("could not claim NICs after %d SSH rounds", liveOSClaimSSHTries)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type dataPlaneCandidate struct {
	IP  string
	Via string
}

// dataPlaneClaimTargets returns SSH targets for this server: leases/reservations
// for known inventory MACs, then orphan leases. orphanMACs is the set of orphan
// lease MACs (for progress / claim stats).
func (s *Server) dataPlaneClaimTargets(known map[string]struct{}) (knownIPs, orphanIPs []dataPlaneCandidate, orphanMACs map[string]struct{}) {
	leases, _ := s.dataDHCPLeases()
	globalKnown := s.allInventoryMACSet()
	orphanMACs = map[string]struct{}{}

	seenKnown := map[string]struct{}{}
	seenOrphan := map[string]struct{}{}
	addKnown := func(ip, via string) {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			return
		}
		if _, ok := seenKnown[ip]; ok {
			return
		}
		seenKnown[ip] = struct{}{}
		knownIPs = append(knownIPs, dataPlaneCandidate{IP: ip, Via: via})
	}
	addOrphan := func(ip, mac, via string) {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			return
		}
		orphanMACs[strings.ToLower(mac)] = struct{}{}
		if _, ok := seenOrphan[ip]; ok {
			return
		}
		seenOrphan[ip] = struct{}{}
		orphanIPs = append(orphanIPs, dataPlaneCandidate{IP: ip, Via: via})
	}

	for _, l := range leases {
		ip := leaseIPString(l)
		mac, err := normalizeMACInput(l.MAC)
		if err != nil || mac == "" || ip == "" {
			continue
		}
		key := strings.ToLower(mac)
		if _, ok := known[key]; ok {
			addKnown(ip, "known-lease "+mac)
			continue
		}
		if _, ok := globalKnown[key]; ok {
			continue // another server's inventory MAC
		}
		addOrphan(ip, mac, "orphan-lease "+mac)
	}

	if cfg, err := s.store.GetLocalDHCPConfig(); err == nil {
		for _, r := range cfg.Reservations {
			mac, err := normalizeMACInput(r.MAC)
			if err != nil || mac == "" {
				continue
			}
			if _, ok := known[strings.ToLower(mac)]; !ok {
				continue
			}
			addKnown(strings.TrimSpace(r.IP), "known-reservation "+mac)
		}
	}
	return knownIPs, orphanIPs, orphanMACs
}

type claimNICsResult struct {
	ServerID   int64  `json:"server_id"`
	ServerName string `json:"server_name"`
	Added      int    `json:"added"`
	LeaseIP    string `json:"lease_ip"`
	LeaseMAC   string `json:"lease_mac,omitempty"`
}

// claimNICsFromLeaseIP SSHs to a data-plane IP (usually an orphan lease), finds
// which inventory server owns the host, then merges live NICs into that server's
// inventory. Matched servers are omitted from later Resolve searches so one
// baremetal is not assigned to two different lease IPs in the same wave.
func (s *Server) claimNICsFromLeaseIP(ctx context.Context, leaseIP, leaseMAC string, progress jobs.Progress) (claimNICsResult, error) {
	out := claimNICsResult{LeaseIP: strings.TrimSpace(leaseIP), LeaseMAC: strings.TrimSpace(leaseMAC)}
	if out.LeaseIP == "" {
		return out, fmt.Errorf("lease IP required")
	}
	creds, err := s.store.GetLiveOSSSH()
	if err != nil {
		return out, err
	}
	sc := storage.Creds{Username: creds.Username, Password: creds.Password, Port: creds.Port}
	if err := sc.Validate(); err != nil {
		return out, fmt.Errorf("live OS SSH not configured: %w", err)
	}

	host := storage.Host{IP: out.LeaseIP, Creds: sc}
	var lastErr error
	var nics []storage.NICInfo
	var dmi liveDMI
	for try := 1; try <= liveOSClaimSSHTries; try++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if progress != nil {
			progress("claim", fmt.Sprintf("SSH %s try %d/%d", out.LeaseIP, try, liveOSClaimSSHTries))
		}
		probeCtx, cancel := context.WithTimeout(ctx, liveOSSSHAttemptTimeout)
		_, sshErr := storage.RunSSH(probeCtx, host.IP, host.Creds, "echo copper-ok")
		if sshErr != nil {
			cancel()
			lastErr = sshErr
			if progress != nil {
				progress("claim", fmt.Sprintf("SSH failed: %v", sshErr))
			}
			if try < liveOSClaimSSHTries {
				_ = sleepCtx(ctx, 5*time.Second)
			}
			continue
		}
		var nicErr error
		nics, nicErr = storage.ListNICs(probeCtx, host)
		if nicErr == nil {
			dmi = readLiveOSDMI(probeCtx, host)
			if progress != nil {
				progress("claim", fmt.Sprintf("live DMI chassis_serial=%q product_serial=%q uuid=%q nics=%d",
					dmi.ChassisSerial, dmi.ProductSerial, dmi.ProductUUID, len(nics)))
			}
		}
		cancel()
		if nicErr != nil {
			lastErr = nicErr
			if progress != nil {
				progress("claim", fmt.Sprintf("NIC list failed: %v", nicErr))
			}
			if try < liveOSClaimSSHTries {
				_ = sleepCtx(ctx, 5*time.Second)
			}
			continue
		}
		// Retry when identity is still empty (DMI not ready) but we have no NICs either.
		if len(nics) == 0 && !dmi.hasAny() && try < liveOSClaimSSHTries {
			lastErr = fmt.Errorf("empty NIC/DMI probe")
			if progress != nil {
				progress("claim", "empty NIC/DMI — retrying")
			}
			_ = sleepCtx(ctx, 5*time.Second)
			continue
		}
		lastErr = nil
		break
	}
	if lastErr != nil {
		return out, fmt.Errorf("SSH to %s: %w", out.LeaseIP, lastErr)
	}
	if len(nics) == 0 {
		return out, fmt.Errorf("no NICs returned from %s", out.LeaseIP)
	}

	exclude := s.claimExcludeIDs(out.LeaseIP)
	if progress != nil && len(exclude) > 0 {
		progress("claim", fmt.Sprintf("omitting %d already-claimed baremetal(s) from search", len(exclude)))
	}
	srv, invPayload, err := s.matchLiveHostToServer(nics, dmi, exclude)
	if err != nil {
		return out, err
	}
	out.ServerID = srv.ID
	out.ServerName = srv.Name
	s.markClaimTaken(srv.ID, out.LeaseIP)
	if progress != nil {
		progress("claim", fmt.Sprintf("matched server %s — merging NICs into inventory (baremetal omitted from further search)", srv.Name))
	}

	added := 0
	err = s.withServerLock(ctx, srv.ID, func(locked context.Context) error {
		snap, err := s.store.GetSnapshot(srv.ID, "inventory")
		if err != nil {
			return err
		}
		payload := invPayload
		if snap != nil && strings.TrimSpace(snap.Payload) != "" {
			payload = snap.Payload
		}
		n, newPayload, err := mergeLiveOSNICsIntoInventory(payload, nics)
		if err != nil {
			return err
		}
		added = n
		if n > 0 {
			if err := s.store.SaveSnapshot(srv.ID, "inventory", newPayload); err != nil {
				return err
			}
		}
		// Persist admin-visible binding so Unassign can clear a wrong Resolve.
		bindMAC := out.LeaseMAC
		if bindMAC == "" && len(nics) > 0 {
			bindMAC = nics[0].MAC
		}
		if bindMAC != "" {
			if nm, nerr := normalizeMACInput(bindMAC); nerr == nil && nm != "" {
				_ = s.store.UpsertDataLeaseBinding(nm, store.DataLeaseBinding{
					ServerID:   srv.ID,
					ServerName: srv.Name,
					IP:         out.LeaseIP,
				})
			}
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	out.Added = added
	if progress != nil {
		progress("claim", fmt.Sprintf("done: %s added %d MAC(s)", srv.Name, added))
	}
	return out, nil
}

func (s *Server) claimExcludeIDs(leaseIP string) map[int64]struct{} {
	s.claimTakenMu.Lock()
	defer s.claimTakenMu.Unlock()
	out := map[int64]struct{}{}
	for id, ip := range s.claimTakenByIP {
		if ip != "" && ip != leaseIP {
			out[id] = struct{}{}
		}
	}
	return out
}

func (s *Server) markClaimTaken(serverID int64, leaseIP string) {
	if serverID <= 0 || strings.TrimSpace(leaseIP) == "" {
		return
	}
	s.claimTakenMu.Lock()
	defer s.claimTakenMu.Unlock()
	if s.claimTakenByIP == nil {
		s.claimTakenByIP = map[int64]string{}
	}
	s.claimTakenByIP[serverID] = strings.TrimSpace(leaseIP)
}

// matchLiveHostToServer finds the inventory server that owns this live host.
// excludeIDs are baremetals already claimed by another lease IP — omitted from search.
// If more than one remaining server matches, refuse to guess.
func (s *Server) matchLiveHostToServer(nics []storage.NICInfo, dmi liveDMI, excludeIDs map[int64]struct{}) (*store.Server, string, error) {
	servers, err := s.store.ListServers()
	if err != nil {
		return nil, "", err
	}
	type hit struct {
		srv     *store.Server
		payload string
	}
	var hits []hit
	for i := range servers {
		srv := &servers[i]
		if excludeIDs != nil {
			if _, skip := excludeIDs[srv.ID]; skip {
				continue
			}
		}
		snap, err := s.store.GetSnapshot(srv.ID, "inventory")
		if err != nil || snap == nil || strings.TrimSpace(snap.Payload) == "" {
			continue
		}
		known := inventoryMACSet(snap.Payload)
		ident := inventoryHostIdentity(snap.Payload)
		if !ident.hasAny() && len(known) == 0 {
			continue
		}
		if liveHostMatchesServer(nics, dmi, known, ident) {
			hits = append(hits, hit{srv: srv, payload: snap.Payload})
		}
	}
	if len(hits) == 0 {
		return nil, "", fmt.Errorf("no inventory server matched chassis_serial/MACs/DMI on this host — run Inventory on candidates first")
	}
	if len(hits) > 1 {
		names := make([]string, 0, len(hits))
		for _, h := range hits {
			names = append(names, h.srv.Name)
		}
		return nil, "", fmt.Errorf("ambiguous host identity — matched %d servers (%s); re-run Inventory", len(hits), strings.Join(names, ", "))
	}
	return hits[0].srv, hits[0].payload, nil
}

func countOrphanHits(nics []storage.NICInfo, orphanMACs map[string]struct{}) int {
	if len(orphanMACs) == 0 {
		return 0
	}
	n := 0
	for _, nic := range nics {
		mac, err := normalizeMACInput(nic.MAC)
		if err != nil || mac == "" || isZeroMAC(mac) {
			continue
		}
		if _, ok := orphanMACs[strings.ToLower(mac)]; ok {
			n++
		}
	}
	return n
}

func (s *Server) dataDHCPLeases() ([]copper_dhcp.Lease, error) {
	if s.localDHCP != nil {
		return s.localDHCP.Leases(), nil
	}
	return s.store.ListDataDHCPLeases()
}

func leaseIPString(l copper_dhcp.Lease) string {
	if l.IP == nil {
		return ""
	}
	return l.IP.String()
}

// hostIdentity is inventory-side proof for a baremetal.
// ChassisSerials / BoardSerials come from Redfish Chassis[] — never from servers.chassis_id.
// Supermicro (and similar) often have DIFFERENT system vs chassis vs board FRUs;
// Resolve must keep all of them so live DMI can hit any one.
type hostIdentity struct {
	Serial         string   // system.serial_number → DMI product_serial
	UUID           string   // system.uuid → DMI product_uuid
	ChassisSerials []string // chassis[].serial_number → DMI chassis_serial
	BoardSerials   []string // chassis[].board_serial → DMI board_serial
}

func (h hostIdentity) hasAny() bool {
	return h.Serial != "" || h.UUID != "" || len(h.ChassisSerials) > 0 || len(h.BoardSerials) > 0
}

func (h hostIdentity) primaryChassisSerial() string {
	if len(h.ChassisSerials) == 0 {
		return ""
	}
	return h.ChassisSerials[0]
}

func inventoryHostIdentity(payload string) hostIdentity {
	var inv struct {
		System struct {
			SerialNumber string `json:"serial_number"`
			UUID         string `json:"uuid"`
		} `json:"system"`
		Chassis []struct {
			SerialNumber string `json:"serial_number"`
			BoardSerial  string `json:"board_serial"`
		} `json:"chassis"`
	}
	_ = json.Unmarshal([]byte(payload), &inv)
	out := hostIdentity{
		Serial: usableIdentity(inv.System.SerialNumber),
		UUID:   usableIdentity(inv.System.UUID),
	}
	seen := map[string]struct{}{}
	add := func(dst *[]string, raw string) {
		sn := usableIdentity(raw)
		if sn == "" {
			return
		}
		key := strings.ToLower(sn)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		*dst = append(*dst, sn)
	}
	for _, ch := range inv.Chassis {
		add(&out.ChassisSerials, ch.SerialNumber)
		add(&out.BoardSerials, ch.BoardSerial)
	}
	return out
}

func inventoryMACSet(payload string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, mac := range collectHostMACsFromJSON(payload) {
		out[strings.ToLower(mac)] = struct{}{}
	}
	return out
}

func (s *Server) allInventoryMACSet() map[string]struct{} {
	out := map[string]struct{}{}
	for _, n := range s.listInventoryHostNICs() {
		out[strings.ToLower(n.MAC)] = struct{}{}
	}
	return out
}

type liveDMI struct {
	ProductSerial string
	ProductUUID   string
	ChassisSerial string
	BoardSerial   string
}

func (d liveDMI) hasAny() bool {
	return d.ProductSerial != "" || d.ProductUUID != "" || d.ChassisSerial != "" || d.BoardSerial != ""
}

// readLiveOSDMI reads product_serial, product_uuid, chassis_serial, and board_serial from the live OS.
func readLiveOSDMI(ctx context.Context, h storage.Host) liveDMI {
	raw, err := storage.RunSSH(ctx, h.IP, h.Creds, `bash -lc 'printf "%s\n" "$(cat /sys/class/dmi/id/product_serial 2>/dev/null)"; printf "%s\n" "$(cat /sys/class/dmi/id/product_uuid 2>/dev/null)"; printf "%s\n" "$(cat /sys/class/dmi/id/chassis_serial 2>/dev/null)"; printf "%s" "$(cat /sys/class/dmi/id/board_serial 2>/dev/null)"'`)
	if err != nil {
		return liveDMI{}
	}
	parts := strings.Split(raw, "\n")
	out := liveDMI{}
	if len(parts) > 0 {
		out.ProductSerial = usableIdentity(parts[0])
	}
	if len(parts) > 1 {
		out.ProductUUID = usableIdentity(parts[1])
	}
	if len(parts) > 2 {
		out.ChassisSerial = usableIdentity(parts[2])
	}
	if len(parts) > 3 {
		out.BoardSerial = usableIdentity(parts[3])
	}
	return out
}

// liveHostMatchesServer: any strong signal is enough (vendors differ).
//
// Identity (preferred for orphan Data DHCP Resolve): ANY usable live DMI field
// (chassis_serial, product_serial, product_uuid) may match ANY inventory
// identity field (system.serial_number, system.uuid, chassis[].serial_number).
// Vendors stash the same FRU value in different slots — HPE often puts the
// service tag in both System.SerialNumber and Chassis.SerialNumber, while
// Linux may expose it only as chassis_serial or only as product_serial.
//
// MAC intersection remains a fallback when Inventory already listed that NIC.
func liveHostMatchesServer(nics []storage.NICInfo, dmi liveDMI, known map[string]struct{}, ident hostIdentity) bool {
	liveIDs := liveIdentityTokens(dmi)
	invIDs := inventoryIdentityTokens(ident)
	for id := range liveIDs {
		if _, ok := invIDs[id]; ok {
			return true
		}
	}
	for _, n := range nics {
		mac, err := normalizeMACInput(n.MAC)
		if err != nil || mac == "" || isZeroMAC(mac) {
			continue
		}
		if _, ok := known[strings.ToLower(mac)]; ok {
			return true
		}
	}
	return false
}

// liveIdentityTokens is the set of usable DMI identity strings from the live OS.
func liveIdentityTokens(dmi liveDMI) map[string]struct{} {
	out := map[string]struct{}{}
	addIdentityToken(out, dmi.ChassisSerial)
	addIdentityToken(out, dmi.ProductSerial)
	addIdentityToken(out, dmi.ProductUUID)
	addIdentityToken(out, dmi.BoardSerial)
	return out
}

// inventoryIdentityTokens is the set of usable identity strings from Redfish inventory.
func inventoryIdentityTokens(ident hostIdentity) map[string]struct{} {
	out := map[string]struct{}{}
	addIdentityToken(out, ident.Serial)
	addIdentityToken(out, ident.UUID)
	for _, sn := range ident.ChassisSerials {
		addIdentityToken(out, sn)
	}
	for _, sn := range ident.BoardSerials {
		addIdentityToken(out, sn)
	}
	return out
}

func addIdentityToken(dst map[string]struct{}, raw string) {
	s := usableIdentity(raw)
	if s == "" {
		return
	}
	dst[strings.ToLower(s)] = struct{}{}
}

func usableIdentity(s string) string {
	s = strings.TrimSpace(strings.Trim(s, "\x00"))
	if s == "" {
		return ""
	}
	switch strings.ToLower(s) {
	case "none", "n/a", "na", "null", "not specified", "not available",
		"to be filled by o.e.m.", "default string", "system serial number",
		"chassis serial number", "0":
		return ""
	}
	return s
}

func isZeroMAC(mac string) bool {
	hw, err := net.ParseMAC(strings.TrimSpace(mac))
	if err != nil {
		return true
	}
	for _, b := range []byte(hw) {
		if b != 0 {
			return false
		}
	}
	return true
}

// mergeLiveOSNICsIntoInventory appends live-OS NIC MACs missing from inventory
// network[]. Returns added count and pretty-printed JSON.
func mergeLiveOSNICsIntoInventory(payload string, live []storage.NICInfo) (added int, out string, err error) {
	var root map[string]json.RawMessage
	if strings.TrimSpace(payload) == "" {
		root = map[string]json.RawMessage{}
	} else if err := json.Unmarshal([]byte(payload), &root); err != nil {
		return 0, "", fmt.Errorf("parse inventory: %w", err)
	}

	var network []map[string]any
	if raw, ok := root["network"]; ok && len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &network); err != nil {
			return 0, "", fmt.Errorf("parse network: %w", err)
		}
	}

	have := inventoryMACSet(payload)

	for _, n := range live {
		if strings.EqualFold(strings.TrimSpace(n.Name), "lo") {
			continue
		}
		mac, nerr := normalizeMACInput(n.MAC)
		if nerr != nil || mac == "" || isZeroMAC(mac) {
			continue
		}
		key := strings.ToLower(mac)
		if _, ok := have[key]; ok {
			continue
		}
		have[key] = struct{}{}
		name := strings.TrimSpace(n.Name)
		entry := map[string]any{
			"id":          name,
			"name":        name,
			"mac_address": mac,
			"source":      liveOSClaimSource,
		}
		if n.State != "" {
			entry["state"] = n.State
		}
		if len(n.Addresses) > 0 {
			entry["ipv4"] = n.Addresses
		}
		network = append(network, entry)
		added++
	}

	netRaw, err := json.Marshal(network)
	if err != nil {
		return 0, "", err
	}
	root["network"] = netRaw

	if raw, ok := root["counts"]; ok && len(raw) > 0 && string(raw) != "null" {
		var counts map[string]any
		if err := json.Unmarshal(raw, &counts); err == nil {
			counts["network"] = len(network)
			if cRaw, err := json.Marshal(counts); err == nil {
				root["counts"] = cRaw
			}
		}
	}

	formatted, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return 0, "", err
	}
	return added, string(formatted), nil
}
