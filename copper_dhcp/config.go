package copper_dhcp

import (
	"fmt"
	"net"
	"strings"
	"time"
)

// Reservation binds a client MAC to a fixed IPv4.
type Reservation struct {
	MAC string `yaml:"mac" json:"mac"`
	IP  string `yaml:"ip" json:"ip"`
}

// Config is the declarative DHCP policy used by both copper_bmc and
// copper_control_plane. Each process runs its own server instance with its
// own Config (different NIC / pool / MAC filters).
type Config struct {
	// Enabled turns the server on. When false, callers should not start DHCP.
	Enabled bool `yaml:"enabled" json:"enabled"`

	// Interface is the NIC name to bind (required by server4). Empty = any.
	Interface string `yaml:"interface" json:"interface"`

	// Listen is host:port for the UDP socket. Default 0.0.0.0:67.
	Listen string `yaml:"listen" json:"listen"`

	// ServerIP is the DHCP server identifier (option 54).
	ServerIP string `yaml:"server_ip" json:"server_ip"`

	// SubnetCIDR is the network served, e.g. "192.168.10.0/24".
	SubnetCIDR string `yaml:"subnet" json:"subnet"`

	// RangeStart / RangeEnd bound the dynamic pool (inclusive), e.g. .100–.120.
	RangeStart string `yaml:"range_start" json:"range_start"`
	RangeEnd   string `yaml:"range_end" json:"range_end"`

	// Gateway is option 3 (routers). Optional.
	Gateway string `yaml:"gateway" json:"gateway"`

	// DNS is option 6. Optional.
	DNS []string `yaml:"dns" json:"dns"`

	// LeaseTime is a Go duration string, e.g. "1h", "30m". Defaults to 1h.
	LeaseTime string `yaml:"lease_time" json:"lease_time"`

	// NextServer is siaddr / PXE next-server. Empty → ServerIP.
	// Used by data-plane / PXE DHCP only — leave empty for OOB/BMC addressing.
	NextServer string `yaml:"next_server" json:"next_server"`

	// BootFile is option 67 (and legacy file field). For PXE/iPXE on the
	// data NIC; leave empty for OOB/BMC addressing.
	BootFile string `yaml:"boot_file" json:"boot_file"`

	// AllowAll answers any MAC in the pool. When false, only AllowMACs
	// (and reservation MACs) are served.
	AllowAll bool `yaml:"allow_all" json:"allow_all"`

	// AllowMACs is an allowlist (ignored when AllowAll is true).
	AllowMACs []string `yaml:"allow_macs" json:"allow_macs"`

	// DenyMACs is always refused, even if AllowAll or listed in AllowMACs.
	DenyMACs []string `yaml:"deny_macs" json:"deny_macs"`

	// Reservations are fixed MAC→IP bindings (IPs may be outside the dynamic range).
	Reservations []Reservation `yaml:"reservations" json:"reservations"`
}

// Normalized is Config with parsed fields ready for the server.
type Normalized struct {
	Interface  string
	ListenHost net.IP
	ListenPort int
	ServerIP   net.IP
	Subnet     *net.IPNet
	Mask       net.IPMask
	RangeStart net.IP
	RangeEnd   net.IP
	Gateway    net.IP
	DNS        []net.IP
	LeaseTime  time.Duration
	NextServer net.IP
	BootFile   string
	AllowAll   bool
	AllowMACs  map[string]struct{}
	DenyMACs   map[string]struct{}
	ReserveMAC map[string]net.IP // mac key → IP
	ReserveIP  map[string]string // ip string → mac key
}

// Normalize validates and parses Config.
func (c Config) Normalize() (*Normalized, error) {
	if !c.Enabled {
		return nil, fmt.Errorf("dhcp is disabled")
	}
	if strings.TrimSpace(c.ServerIP) == "" {
		return nil, fmt.Errorf("server_ip is required")
	}
	serverIP := net.ParseIP(c.ServerIP).To4()
	if serverIP == nil {
		return nil, fmt.Errorf("invalid server_ip %q", c.ServerIP)
	}
	if strings.TrimSpace(c.SubnetCIDR) == "" {
		return nil, fmt.Errorf("subnet is required")
	}
	_, subnet, err := net.ParseCIDR(c.SubnetCIDR)
	if err != nil {
		return nil, fmt.Errorf("invalid subnet %q: %w", c.SubnetCIDR, err)
	}
	start := net.ParseIP(c.RangeStart).To4()
	end := net.ParseIP(c.RangeEnd).To4()
	if start == nil || end == nil {
		return nil, fmt.Errorf("range_start and range_end must be IPv4")
	}
	if !subnet.Contains(start) || !subnet.Contains(end) {
		return nil, fmt.Errorf("pool range must lie inside subnet")
	}
	if ipToUint(start) > ipToUint(end) {
		return nil, fmt.Errorf("range_start must be <= range_end")
	}

	listen := c.Listen
	if listen == "" {
		listen = "0.0.0.0:67"
	}
	host, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		return nil, fmt.Errorf("invalid listen %q: %w", listen, err)
	}
	listenIP := net.ParseIP(host)
	if listenIP == nil {
		return nil, fmt.Errorf("invalid listen host %q", host)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid listen port %q", portStr)
	}

	lease := time.Hour
	if strings.TrimSpace(c.LeaseTime) != "" {
		d, err := time.ParseDuration(c.LeaseTime)
		if err != nil {
			return nil, fmt.Errorf("invalid lease_time %q: %w", c.LeaseTime, err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("lease_time must be positive")
		}
		lease = d
	}

	next := serverIP
	if ns := strings.TrimSpace(c.NextServer); ns != "" {
		next = net.ParseIP(ns).To4()
		if next == nil {
			return nil, fmt.Errorf("invalid next_server %q", c.NextServer)
		}
	}

	var gw net.IP
	if g := strings.TrimSpace(c.Gateway); g != "" {
		gw = net.ParseIP(g).To4()
		if gw == nil {
			return nil, fmt.Errorf("invalid gateway %q", c.Gateway)
		}
	}

	var dns []net.IP
	for _, d := range c.DNS {
		ip := net.ParseIP(d).To4()
		if ip == nil {
			return nil, fmt.Errorf("invalid dns %q", d)
		}
		dns = append(dns, ip)
	}

	allow := map[string]struct{}{}
	for _, m := range c.AllowMACs {
		k, err := normalizeMAC(m)
		if err != nil {
			return nil, fmt.Errorf("allow_macs: %w", err)
		}
		allow[k] = struct{}{}
	}
	deny := map[string]struct{}{}
	for _, m := range c.DenyMACs {
		k, err := normalizeMAC(m)
		if err != nil {
			return nil, fmt.Errorf("deny_macs: %w", err)
		}
		deny[k] = struct{}{}
	}

	resMAC := map[string]net.IP{}
	resIP := map[string]string{}
	for _, r := range c.Reservations {
		mac, err := normalizeMAC(r.MAC)
		if err != nil {
			return nil, fmt.Errorf("reservation mac: %w", err)
		}
		ip := net.ParseIP(r.IP).To4()
		if ip == nil {
			return nil, fmt.Errorf("reservation ip invalid: %q", r.IP)
		}
		if !subnet.Contains(ip) {
			return nil, fmt.Errorf("reservation %s not in subnet", r.IP)
		}
		if other, ok := resIP[ip.String()]; ok && other != mac {
			return nil, fmt.Errorf("reservation IP %s claimed by two MACs", r.IP)
		}
		resMAC[mac] = ip
		resIP[ip.String()] = mac
		allow[mac] = struct{}{} // reservations imply allow
	}

	if !c.AllowAll && len(allow) == 0 {
		return nil, fmt.Errorf("allow_all is false but allow_macs/reservations are empty — server would answer nobody")
	}

	return &Normalized{
		Interface:  c.Interface,
		ListenHost: listenIP,
		ListenPort: port,
		ServerIP:   serverIP,
		Subnet:     subnet,
		Mask:       subnet.Mask,
		RangeStart: start,
		RangeEnd:   end,
		Gateway:    gw,
		DNS:        dns,
		LeaseTime:  lease,
		NextServer: next,
		BootFile:   c.BootFile,
		AllowAll:   c.AllowAll,
		AllowMACs:  allow,
		DenyMACs:   deny,
		ReserveMAC: resMAC,
		ReserveIP:  resIP,
	}, nil
}

func (n *Normalized) MACAllowed(mac net.HardwareAddr) bool {
	key := macKey(mac)
	if key == "" {
		return false
	}
	if _, denied := n.DenyMACs[key]; denied {
		return false
	}
	if n.AllowAll {
		return true
	}
	_, ok := n.AllowMACs[key]
	return ok
}

func normalizeMAC(s string) (string, error) {
	hw, err := net.ParseMAC(strings.TrimSpace(s))
	if err != nil {
		return "", fmt.Errorf("invalid mac %q: %w", s, err)
	}
	return macKey(hw), nil
}

func macKey(hw net.HardwareAddr) string {
	if len(hw) == 0 {
		return ""
	}
	return strings.ToLower(hw.String())
}

func ipToUint(ip net.IP) uint32 {
	v := ip.To4()
	if v == nil {
		return 0
	}
	return uint32(v[0])<<24 | uint32(v[1])<<16 | uint32(v[2])<<8 | uint32(v[3])
}

func uintToIP(v uint32) net.IP {
	return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v)).To4()
}
