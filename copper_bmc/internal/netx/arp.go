package netx

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

// ResolveMAC finds the L2 address for ip on this host's neighbor table.
// It briefly dials common BMC ports so the kernel ARP/ND entry is populated
// when the agent shares L2 with the BMC (OOB plane).
func ResolveMAC(ipStr string) (string, error) {
	ip := net.ParseIP(strings.TrimSpace(ipStr))
	if ip == nil || ip.To4() == nil {
		return "", fmt.Errorf("need an IPv4 address, got %q", ipStr)
	}
	ip4 := ip.To4().String()

	// Nudge the neighbor table (ignore dial errors — BMC may refuse TCP).
	for _, port := range []string{"443", "80", "17988", "623"} {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip4, port), 800*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
	}

	mac, err := lookupProcARP(ip4)
	if err != nil {
		return "", err
	}
	return mac, nil
}

func lookupProcARP(ip4 string) (string, error) {
	f, err := os.Open("/proc/net/arp")
	if err != nil {
		return "", fmt.Errorf("read arp table: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return "", fmt.Errorf("empty arp table")
	}
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 {
			continue
		}
		if fields[0] != ip4 {
			continue
		}
		hw := fields[3]
		if hw == "00:00:00:00:00:00" || hw == "<incomplete>" {
			return "", fmt.Errorf("no complete ARP entry for %s (agent may not share L2 with this BMC)", ip4)
		}
		parsed, err := net.ParseMAC(hw)
		if err != nil {
			return "", fmt.Errorf("bad arp hw address %q: %w", hw, err)
		}
		return parsed.String(), nil
	}
	return "", fmt.Errorf("no ARP entry for %s — is copper_bmc on the same OOB VLAN?", ip4)
}
