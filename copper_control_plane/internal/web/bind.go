package web

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strings"

	"copper_control_plane/internal/agent"
	"copper_control_plane/internal/store"
	"copper_dhcp"
)

// dhcpLeaseView is a normalized OOB lease for binding.
type dhcpLeaseView struct {
	MAC string
	IP  string
}

// BindResult summarizes DHCP → inventory fulfillment (+ optional LLDP enrich).
type BindResult struct {
	LeasesSeen   int
	Bound        int
	Updated      int
	Unmatched    int
	LLDPSeen     int
	LLDPEnriched int
	Error        string
}

// reconcileOOBDHCP pulls agent DHCP leases and binds matching declared/imported servers.
// Match priority: MAC first, then BMC IP. Then LLDP enriches chassis_id / MAC gaps.
// When the DHCP role is off, lease bind is skipped but LLDP enrich still runs.
func (s *Server) reconcileOOBDHCP() BindResult {
	var out BindResult
	enabled, err := s.agent.DHCPRoleEnabled()
	if err != nil {
		out.Error = err.Error()
		// Still try LLDP; agent may be up for capture only.
	} else if enabled {
		leases, err := s.fetchAgentLeases()
		if err != nil {
			out.Error = err.Error()
		} else {
			out.LeasesSeen = len(leases)
			claimed := map[int64]bool{}
			for _, lease := range leases {
				mac := strings.TrimSpace(lease.MAC)
				ip := strings.TrimSpace(lease.IP)
				if mac == "" && ip == "" {
					continue
				}
				if mac != "" {
					if hw, err := net.ParseMAC(mac); err == nil {
						mac = hw.String()
					}
				}

				srv, how, err := s.matchServerForLease(mac, ip, claimed)
				if err != nil {
					log.Printf("bind: match error mac=%s ip=%s: %v", mac, ip, err)
					continue
				}
				if srv == nil {
					out.Unmatched++
					continue
				}
				changed, err := s.store.BindFromLease(srv, mac, ip)
				if err != nil {
					log.Printf("bind: update server %d: %v", srv.ID, err)
					continue
				}
				claimed[srv.ID] = true
				out.Bound++
				if changed {
					out.Updated++
					log.Printf("bind: server %d (%s) fulfilled via %s lease mac=%s ip=%s", srv.ID, srv.Name, how, mac, ip)
				}
			}
		}
	}

	lldp := s.enrichFromLLDP()
	out.LLDPSeen = lldp.LLDPSeen
	out.LLDPEnriched = lldp.LLDPEnriched
	if lldp.Error != "" {
		log.Printf("lldp enrich: %s", lldp.Error)
		if out.Error == "" {
			out.Error = "lldp: " + lldp.Error
		}
	}
	return out
}

// enrichFromLLDP matches agent LLDP neighbors to inventory and fills chassis_id / MAC.
func (s *Server) enrichFromLLDP() BindResult {
	var out BindResult
	neighbors, err := s.agent.LLDPNeighbors()
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.LLDPSeen = len(neighbors)
	for _, n := range neighbors {
		srv, how, err := s.matchServerForLLDP(n)
		if err != nil {
			log.Printf("lldp: match error chassis=%s mac=%s: %v", n.ChassisID, n.SrcMAC, err)
			continue
		}
		if srv == nil {
			continue
		}
		changed, err := s.store.EnrichFromLLDP(srv, n.ChassisID, n.SrcMAC, n.MgmtAddrs)
		if err != nil {
			log.Printf("lldp: enrich server %d: %v", srv.ID, err)
			continue
		}
		if changed {
			out.LLDPEnriched++
			log.Printf("lldp: server %d (%s) enriched via %s chassis=%s mac=%s",
				srv.ID, srv.Name, how, n.ChassisID, n.SrcMAC)
		}
	}
	return out
}

func (s *Server) matchServerForLLDP(n agent.LLDPNeighbor) (*store.Server, string, error) {
	mac := strings.TrimSpace(n.SrcMAC)
	if mac != "" {
		if hw, err := net.ParseMAC(mac); err == nil {
			mac = hw.String()
		}
		srv, err := s.store.FindServerByMAC(mac)
		if err != nil {
			return nil, "", err
		}
		if srv != nil {
			return srv, "mac", nil
		}
		// Chassis ID that is itself a MAC (common BMC LLDP subtype).
		if hw, err := net.ParseMAC(n.ChassisID); err == nil {
			srv, err := s.store.FindServerByMAC(hw.String())
			if err != nil {
				return nil, "", err
			}
			if srv != nil {
				return srv, "chassis-mac", nil
			}
		}
	}
	if n.ChassisID != "" {
		srv, err := s.store.FindServerByChassisID(n.ChassisID)
		if err != nil {
			return nil, "", err
		}
		if srv != nil {
			return srv, "chassis_id", nil
		}
	}
	for _, ip := range n.MgmtAddrs {
		srv, err := s.store.FindServerByBMCIP(strings.TrimSpace(ip))
		if err != nil {
			return nil, "", err
		}
		if srv != nil {
			return srv, "mgmt-ip", nil
		}
	}
	return nil, "", nil
}

func (s *Server) matchServerForLease(mac, ip string, claimed map[int64]bool) (*store.Server, string, error) {
	if mac != "" {
		srv, err := s.store.FindServerByMAC(mac)
		if err != nil {
			return nil, "", err
		}
		if srv != nil && !claimed[srv.ID] {
			return srv, "mac", nil
		}
	}
	if ip != "" {
		srv, err := s.store.FindServerByBMCIP(ip)
		if err != nil {
			return nil, "", err
		}
		if srv != nil && !claimed[srv.ID] {
			return srv, "ip", nil
		}
	}
	return nil, "", nil
}

func (s *Server) fetchAgentLeases() ([]dhcpLeaseView, error) {
	raw, err := s.agent.DHCPLeases()
	if err != nil {
		return nil, fmt.Errorf("agent DHCP leases: %w", err)
	}
	return parseLeasesPayload(raw)
}

func parseLeasesPayload(raw map[string]any) ([]dhcpLeaseView, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var typed struct {
		Leases []copper_dhcp.Lease `json:"leases"`
	}
	if err := json.Unmarshal(b, &typed); err != nil {
		// Fallback: loose map parsing
		return parseLeasesLoose(raw)
	}
	out := make([]dhcpLeaseView, 0, len(typed.Leases))
	for _, l := range typed.Leases {
		ip := ""
		if l.IP != nil {
			ip = l.IP.String()
		}
		out = append(out, dhcpLeaseView{MAC: l.MAC, IP: ip})
	}
	return out, nil
}

func parseLeasesLoose(raw map[string]any) ([]dhcpLeaseView, error) {
	arr, _ := raw["leases"].([]any)
	if arr == nil {
		return nil, nil
	}
	var out []dhcpLeaseView
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		mac, _ := m["mac"].(string)
		ip := ""
		switch v := m["ip"].(type) {
		case string:
			if parsed := net.ParseIP(v); parsed != nil {
				ip = parsed.String()
			} else {
				ip = strings.TrimSpace(v)
			}
		case []any:
			if len(v) >= 4 {
				parts := make([]string, 0, 4)
				for i := 0; i < 4; i++ {
					switch n := v[i].(type) {
					case float64:
						parts = append(parts, fmt.Sprintf("%d", int(n)))
					}
				}
				if len(parts) == 4 {
					ip = strings.Join(parts, ".")
				}
			}
		}
		out = append(out, dhcpLeaseView{MAC: mac, IP: ip})
	}
	return out, nil
}
