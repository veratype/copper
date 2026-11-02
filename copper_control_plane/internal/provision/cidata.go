package provision

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// hostnameOK is a conservative hostname or FQDN (no braces / spaces).
var hostnameOK = regexp.MustCompile(`(?i)^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)

// NetworkMode is how the installed OS gets an address on first boot.
type NetworkMode string

const (
	NetDHCP   NetworkMode = "dhcp"
	NetStatic NetworkMode = "static"
)

// Cidata is the NoCloud seed the admin fills in before provision.
type Cidata struct {
	Hostname    string      `json:"hostname"`
	Username    string      `json:"username"`
	Password    string      `json:"password"`
	NetworkMode NetworkMode `json:"network_mode"` // dhcp | static
	Interface   string      `json:"interface"`    // e.g. ens3 / eth0 (display / set-name hint)
	MAC         string      `json:"mac,omitempty"` // optional; preferred for netplan match
	AddressCIDR string      `json:"address_cidr,omitempty"` // static: 10.0.0.20/24
	Gateway     string      `json:"gateway,omitempty"`
	DNS         []string    `json:"dns,omitempty"`
}

func (c Cidata) Normalize() Cidata {
	out := c
	out.Hostname = strings.TrimSpace(out.Hostname)
	out.Username = strings.TrimSpace(out.Username)
	out.Interface = strings.TrimSpace(out.Interface)
	out.MAC = normalizeMAC(out.MAC)
	out.AddressCIDR = strings.TrimSpace(out.AddressCIDR)
	out.Gateway = strings.TrimSpace(out.Gateway)
	out.NetworkMode = NetworkMode(strings.ToLower(strings.TrimSpace(string(out.NetworkMode))))
	if out.NetworkMode == "" {
		out.NetworkMode = NetDHCP
	}
	var dns []string
	for _, d := range out.DNS {
		d = strings.TrimSpace(d)
		if d != "" {
			dns = append(dns, d)
		}
	}
	out.DNS = dns
	return out
}

func normalizeMAC(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.ReplaceAll(s, "-", ":")
	return s
}

// ValidateCredentials checks username/password only (before per-host NIC/address expand).
func (c Cidata) ValidateCredentials() error {
	c = c.Normalize()
	if c.Username == "" {
		return fmt.Errorf("username is required")
	}
	if c.Password == "" {
		return fmt.Errorf("password is required")
	}
	return nil
}

func (c Cidata) Validate() error {
	c = c.Normalize()
	if err := c.ValidateCredentials(); err != nil {
		return err
	}
	if c.Hostname != "" && !hostnameOK.MatchString(c.Hostname) {
		return fmt.Errorf("invalid hostname %q (use letters, digits, hyphens; patterns like {name}-{n} expand before validate)", c.Hostname)
	}
	if c.Interface == "" && c.MAC == "" {
		return fmt.Errorf("network interface (or MAC) is required")
	}
	if c.MAC != "" {
		if hw, err := net.ParseMAC(c.MAC); err != nil || hw == nil {
			return fmt.Errorf("invalid MAC %q", c.MAC)
		}
	}
	switch c.NetworkMode {
	case NetDHCP:
		// ok
	case NetStatic:
		if c.AddressCIDR == "" {
			return fmt.Errorf("static address (CIDR) is required")
		}
		ip, ipNet, err := net.ParseCIDR(c.AddressCIDR)
		if err != nil || ip == nil || ipNet == nil {
			return fmt.Errorf("invalid address CIDR %q", c.AddressCIDR)
		}
		if c.Gateway != "" && net.ParseIP(c.Gateway) == nil {
			return fmt.Errorf("invalid gateway %q", c.Gateway)
		}
	default:
		return fmt.Errorf("network_mode must be dhcp or static")
	}
	return nil
}

// UserData returns cloud-config YAML (users/hostname only — network is NetworkConfig).
func (c Cidata) UserData() string {
	c = c.Normalize()
	var b strings.Builder
	b.WriteString("#cloud-config\n")
	if c.Hostname != "" {
		b.WriteString("hostname: " + yamlStr(c.Hostname) + "\n")
		b.WriteString("manage_etc_hosts: true\n")
	}
	b.WriteString("users:\n")
	b.WriteString("  - name: " + yamlStr(c.Username) + "\n")
	b.WriteString("    sudo: ALL=(ALL) NOPASSWD:ALL\n")
	b.WriteString("    shell: /bin/bash\n")
	b.WriteString("    lock_passwd: false\n")
	b.WriteString("chpasswd:\n")
	b.WriteString("  list: |\n")
	b.WriteString("    " + c.Username + ":" + c.Password + "\n")
	b.WriteString("  expire: false\n")
	b.WriteString("ssh_pwauth: true\n")
	return b.String()
}

// NetworkConfig returns NoCloud network-config (netplan version 2, unwrapped).
// Ubuntu cloud images apply this from /var/lib/cloud/seed/nocloud/network-config;
// putting network: inside user-data is often ignored for NoCloud.
//
// Do NOT put this file's contents under cloud.cfg.d's top-level `network:` key —
// that overrides the datasource and must be either a full v1/v2 config or
// exactly `config: disabled` (never `config: enabled`).
func (c Cidata) NetworkConfig() string {
	c = c.Normalize()
	var b strings.Builder
	b.WriteString("version: 2\n")
	b.WriteString("ethernets:\n")

	id := c.Interface
	if id == "" {
		id = "id0"
	}
	b.WriteString("  " + id + ":\n")
	if c.MAC != "" {
		b.WriteString("    match:\n")
		b.WriteString("      macaddress: " + yamlStr(c.MAC) + "\n")
		if c.Interface != "" {
			b.WriteString("    set-name: " + yamlStr(c.Interface) + "\n")
		}
	}
	if c.NetworkMode == NetStatic {
		b.WriteString("    dhcp4: false\n")
		b.WriteString("    addresses:\n")
		b.WriteString("      - " + c.AddressCIDR + "\n")
		if c.Gateway != "" {
			b.WriteString("    routes:\n")
			b.WriteString("      - to: default\n")
			b.WriteString("        via: " + c.Gateway + "\n")
		}
		if len(c.DNS) > 0 {
			b.WriteString("    nameservers:\n")
			b.WriteString("      addresses:\n")
			for _, d := range c.DNS {
				b.WriteString("        - " + d + "\n")
			}
		}
	} else {
		b.WriteString("    dhcp4: true\n")
	}
	return b.String()
}

// NetplanConfig returns the same v2 config wrapped for /etc/netplan/*.yaml
// (netplan requires a top-level `network:` key; NoCloud network-config does not).
func (c Cidata) NetplanConfig() string {
	var b strings.Builder
	b.WriteString("network:\n")
	for _, line := range strings.Split(c.NetworkConfig(), "\n") {
		if line == "" {
			continue
		}
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}

// MetaData returns NoCloud meta-data.
func (c Cidata) MetaData() string {
	c = c.Normalize()
	id := c.Hostname
	if id == "" {
		id = "copper-" + c.Username
	}
	var b strings.Builder
	b.WriteString("instance-id: " + id + "\n")
	b.WriteString("local-hostname: " + id + "\n")
	return b.String()
}

func yamlStr(s string) string {
	if s == "" {
		return `""`
	}
	needQuote := strings.ContainsAny(s, ":#{}[],&*?|-<>=!%@`'\\\" \t")
	if !needQuote {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}
