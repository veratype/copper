package netx

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// LLDPNeighbor is one sighted LLDP speaker (typically a BMC on the OOB plane).
type LLDPNeighbor struct {
	ChassisID      string    `json:"chassis_id"`
	ChassisSubtype string    `json:"chassis_subtype"`
	PortID         string    `json:"port_id"`
	PortSubtype    string    `json:"port_subtype"`
	TTL            uint16    `json:"ttl"`
	SysName        string    `json:"sys_name,omitempty"`
	SysDescription string    `json:"sys_description,omitempty"`
	MgmtAddrs      []string  `json:"mgmt_addrs,omitempty"`
	SrcMAC         string    `json:"src_mac"` // Ethernet source of the LLDP frame
	Iface          string    `json:"iface"`
	LastSeen       time.Time `json:"last_seen"`
}

// LLDPTable is a thread-safe cache of recent LLDP neighbors.
type LLDPTable struct {
	mu   sync.RWMutex
	byKey map[string]LLDPNeighbor // key = iface|src_mac
}

func NewLLDPTable() *LLDPTable {
	return &LLDPTable{byKey: make(map[string]LLDPNeighbor)}
}

func neighborKey(iface, srcMAC string) string {
	return strings.ToLower(iface) + "|" + strings.ToLower(srcMAC)
}

// Upsert records or refreshes a neighbor.
func (t *LLDPTable) Upsert(n LLDPNeighbor) {
	if n.SrcMAC == "" {
		return
	}
	n.LastSeen = time.Now().UTC()
	t.mu.Lock()
	t.byKey[neighborKey(n.Iface, n.SrcMAC)] = n
	t.mu.Unlock()
}

// List returns neighbors still within TTL (default 120s if TTL is 0).
func (t *LLDPTable) List() []LLDPNeighbor {
	t.mu.RLock()
	defer t.mu.RUnlock()
	now := time.Now().UTC()
	out := make([]LLDPNeighbor, 0, len(t.byKey))
	for _, n := range t.byKey {
		ttl := time.Duration(n.TTL) * time.Second
		if ttl <= 0 {
			ttl = 120 * time.Second
		}
		// Keep a little grace beyond advertised TTL.
		if now.Sub(n.LastSeen) > ttl*2 {
			continue
		}
		out = append(out, n)
	}
	return out
}

// FindByMAC returns a neighbor whose Ethernet src MAC or Chassis ID (MAC subtype) matches.
func (t *LLDPTable) FindByMAC(mac string) (LLDPNeighbor, bool) {
	mac = canonMAC(mac)
	if mac == "" {
		return LLDPNeighbor{}, false
	}
	for _, n := range t.List() {
		if canonMAC(n.SrcMAC) == mac {
			return n, true
		}
		if strings.Contains(strings.ToLower(n.ChassisSubtype), "mac") && canonMAC(n.ChassisID) == mac {
			return n, true
		}
	}
	return LLDPNeighbor{}, false
}

// FindByChassisID returns a neighbor with matching chassis_id string.
func (t *LLDPTable) FindByChassisID(id string) (LLDPNeighbor, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return LLDPNeighbor{}, false
	}
	for _, n := range t.List() {
		if strings.EqualFold(n.ChassisID, id) {
			return n, true
		}
	}
	return LLDPNeighbor{}, false
}

// FindByIP returns a neighbor advertising ip as an LLDP management address.
func (t *LLDPTable) FindByIP(ip string) (LLDPNeighbor, bool) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return LLDPNeighbor{}, false
	}
	for _, n := range t.List() {
		for _, a := range n.MgmtAddrs {
			if a == ip {
				return n, true
			}
		}
	}
	return LLDPNeighbor{}, false
}

func canonMAC(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	hw, err := net.ParseMAC(s)
	if err != nil {
		return strings.ToLower(s)
	}
	return hw.String()
}

// ResolveLLDPIface picks a capture interface from the media-plane bind config.
func ResolveLLDPIface(ifaceName, bindAddr string) (string, error) {
	ifaceName = strings.TrimSpace(ifaceName)
	bindAddr = strings.TrimSpace(bindAddr)
	if ifaceName != "" && ifaceName != "all" && ifaceName != "custom" {
		return ifaceName, nil
	}
	if bindAddr != "" && bindAddr != "0.0.0.0" {
		ip := net.ParseIP(bindAddr)
		if ip == nil {
			return "", fmt.Errorf("invalid bind addr %q", bindAddr)
		}
		ifaces, err := net.Interfaces()
		if err != nil {
			return "", err
		}
		for _, iface := range ifaces {
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, a := range addrs {
				var cand net.IP
				switch v := a.(type) {
				case *net.IPNet:
					cand = v.IP
				case *net.IPAddr:
					cand = v.IP
				}
				if cand != nil && cand.Equal(ip) {
					return iface.Name, nil
				}
			}
		}
		return "", fmt.Errorf("no interface owns %s", bindAddr)
	}
	return "", fmt.Errorf("LLDP needs a concrete OOB NIC (media bind is %q)", ifaceName)
}
