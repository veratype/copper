package config

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Listener is a bind address + port on a specific network plane.
type Listener struct {
	Port      int
	BindAddr  string // e.g. "0.0.0.0" or a NIC IPv4
	Interface string // NIC name, "all", or "custom"
}

// Config is collected interactively at startup.
//
// Bare-metal networks are split:
//   - MediaListener   → OOB / BMC plane (ISO reachable by hardware BMCs)
//   - CommandListener → control-plane plane (API reachable by copper_control_plane)
//
// Redfish credentials are NOT stored here — control plane sends a BMC target
// with every command so this agent can drive a collection of BMCs.
type Config struct {
	MediaFolder     string
	MediaListener   Listener
	CommandListener Listener
	// DHCPRole means this agent accepts DHCP config from copper_control_plane
	// and may run a DHCPv4 server on the OOB plane. Config never comes from YAML.
	DHCPRole bool
	// LLDPRole means this agent passively captures LLDP on the OOB media NIC
	// for chassis/MAC enrichment. Needs CAP_NET_RAW / root.
	LLDPRole bool
}

type nicOption struct {
	Name string
	Addr string
}

// Prompt asks the admin for OOB media and control-plane command listeners only.
func Prompt(in *bufio.Reader) (*Config, error) {
	fmt.Println("=== copper_bmc — bare-metal agent ===")
	fmt.Println("BMC/OOB and control-plane networks are configured separately.")
	fmt.Println("Redfish credentials come from copper_control_plane per request.")
	fmt.Println()

	fmt.Println("--- Media server (OOB / BMC network) ---")
	fmt.Println("Hardware BMCs must reach this address to download images.")
	mediaPort, err := askPort(in, "Media listen port", 8080)
	if err != nil {
		return nil, err
	}
	folder, err := askFolder(in)
	if err != nil {
		return nil, err
	}
	mediaBind, mediaIface, err := askBind(in, "Media bind NIC (OOB / BMC-facing)")
	if err != nil {
		return nil, err
	}

	fmt.Println()
	fmt.Println("--- Command API (control-plane network) ---")
	fmt.Println("copper_control_plane must reach this address; often NOT the BMC VLAN.")
	cmdPort, err := askPort(in, "Command API listen port", 9090)
	if err != nil {
		return nil, err
	}
	cmdBind, cmdIface, err := askBind(in, "Command bind NIC (control-plane-facing)")
	if err != nil {
		return nil, err
	}

	fmt.Println()
	fmt.Println("--- DHCP role (optional) ---")
	fmt.Println("If enabled, this agent can run DHCPv4 on the OOB/BMC network.")
	fmt.Println("Policy (pool, MAC filters, …) is configured only in copper_control_plane.")
	dhcpRole, err := askYesNo(in, "Enable DHCP server role on this agent?", false)
	if err != nil {
		return nil, err
	}

	fmt.Println()
	fmt.Println("--- LLDP capture (optional) ---")
	fmt.Println("If enabled, this agent listens for LLDP on the OOB media NIC to help")
	fmt.Println("fill chassis_id / MAC in the control plane inventory.")
	fmt.Println("Requires root or CAP_NET_RAW. Saying no skips LLDP; everything else still runs.")
	lldpRole, err := askYesNo(in, "Enable LLDP capture on this agent?", false)
	if err != nil {
		return nil, err
	}

	return &Config{
		MediaFolder: folder,
		MediaListener: Listener{
			Port:      mediaPort,
			BindAddr:  mediaBind,
			Interface: mediaIface,
		},
		CommandListener: Listener{
			Port:      cmdPort,
			BindAddr:  cmdBind,
			Interface: cmdIface,
		},
		DHCPRole: dhcpRole,
		LLDPRole: lldpRole,
	}, nil
}

func askYesNo(in *bufio.Reader, label string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		fmt.Printf("%s [%s]: ", label, hint)
		line, err := readLine(in)
		if err != nil {
			return false, err
		}
		if line == "" {
			return def, nil
		}
		switch strings.ToLower(line) {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			fmt.Println("  Enter y or n.")
		}
	}
}

func (l Listener) Addr() string {
	return net.JoinHostPort(l.BindAddr, strconv.Itoa(l.Port))
}

func askPort(in *bufio.Reader, label string, def int) (int, error) {
	for {
		fmt.Printf("%s [%d]: ", label, def)
		line, err := readLine(in)
		if err != nil {
			return 0, err
		}
		if line == "" {
			return def, nil
		}
		port, err := strconv.Atoi(line)
		if err != nil || port < 1 || port > 65535 {
			fmt.Println("  Invalid port. Enter a number between 1 and 65535.")
			continue
		}
		return port, nil
	}
}

func askFolder(in *bufio.Reader) (string, error) {
	for {
		fmt.Print("Folder to serve (absolute or relative path): ")
		line, err := readLine(in)
		if err != nil {
			return "", err
		}
		if line == "" {
			fmt.Println("  A folder path is required.")
			continue
		}

		abs, err := filepath.Abs(line)
		if err != nil {
			fmt.Printf("  Cannot resolve path: %v\n", err)
			continue
		}

		info, err := os.Stat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Println("  Folder does not exist.")
			} else {
				fmt.Printf("  Cannot access folder: %v\n", err)
			}
			continue
		}
		if !info.IsDir() {
			fmt.Println("  Path exists but is not a directory.")
			continue
		}
		return abs, nil
	}
}

func askBind(in *bufio.Reader, label string) (bindAddr, ifaceName string, err error) {
	nics, err := listNICs()
	if err != nil {
		return "", "", fmt.Errorf("list network interfaces: %w", err)
	}

	fmt.Println()
	fmt.Println(label + ":")
	fmt.Println("  0) all interfaces (0.0.0.0)")
	for i, n := range nics {
		fmt.Printf("  %d) %s — %s\n", i+1, n.Name, n.Addr)
	}

	for {
		fmt.Print("Select NIC number, or type an IP / 0.0.0.0 [0]: ")
		line, err := readLine(in)
		if err != nil {
			return "", "", err
		}
		if line == "" {
			line = "0"
		}

		if idx, err := strconv.Atoi(line); err == nil {
			if idx == 0 {
				return "0.0.0.0", "all", nil
			}
			if idx >= 1 && idx <= len(nics) {
				n := nics[idx-1]
				return n.Addr, n.Name, nil
			}
			fmt.Println("  Invalid selection.")
			continue
		}

		ip := net.ParseIP(line)
		if ip == nil || ip.To4() == nil {
			fmt.Println("  Enter a list number, an IPv4 address, or 0.0.0.0.")
			continue
		}
		addr := ip.String()
		name := "custom"
		for _, n := range nics {
			if n.Addr == addr {
				name = n.Name
				break
			}
		}
		if addr == "0.0.0.0" {
			name = "all"
		}
		return addr, name, nil
	}
}

func listNICs() ([]nicOption, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	var out []nicOption
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() || ip.To4() == nil {
				continue
			}
			out = append(out, nicOption{Name: iface.Name, Addr: ip.String()})
		}
	}
	return out, nil
}

func readLine(in *bufio.Reader) (string, error) {
	line, err := in.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
