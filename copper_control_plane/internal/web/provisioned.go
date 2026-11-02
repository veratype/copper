package web

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// Single DNS label or dotted FQDN (no braces/spaces) — matches deploy cidata intent.
var provisionedHostnameOK = regexp.MustCompile(`(?i)^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)

type provisionedFields struct {
	Hostname string
	IPCIDR   string
	GW       string
	DNS      string // normalized comma+space joined
}

func parseProvisionedForm(hostname, ipCIDR, gw, dnsJoined string) (provisionedFields, error) {
	out := provisionedFields{
		Hostname: strings.TrimSpace(hostname),
		IPCIDR:   strings.TrimSpace(ipCIDR),
		GW:       strings.TrimSpace(gw),
	}
	if out.Hostname != "" {
		if len(out.Hostname) > 253 || !provisionedHostnameOK.MatchString(out.Hostname) {
			return out, fmt.Errorf("invalid provisioned hostname %q", out.Hostname)
		}
	}
	if out.IPCIDR != "" {
		ip, ipNet, err := net.ParseCIDR(out.IPCIDR)
		if err != nil || ip == nil || ipNet == nil || ip.To4() == nil {
			return out, fmt.Errorf("invalid provisioned IP CIDR %q (need IPv4 like 10.0.0.20/24)", out.IPCIDR)
		}
		ones, bits := ipNet.Mask.Size()
		if bits != 32 {
			return out, fmt.Errorf("invalid provisioned IP CIDR %q", out.IPCIDR)
		}
		out.IPCIDR = fmt.Sprintf("%s/%d", ip.To4().String(), ones)
	}
	if out.GW != "" {
		ip := net.ParseIP(out.GW)
		if ip == nil || ip.To4() == nil {
			return out, fmt.Errorf("invalid provisioned gateway %q", out.GW)
		}
		out.GW = ip.To4().String()
	}
	var dns []string
	for _, part := range strings.Split(dnsJoined, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		ip := net.ParseIP(part)
		if ip == nil || ip.To4() == nil {
			return out, fmt.Errorf("invalid provisioned DNS %q", part)
		}
		dns = append(dns, ip.To4().String())
	}
	out.DNS = strings.Join(dns, ", ")
	return out, nil
}

func splitProvisionedDNS(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
