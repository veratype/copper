package redfishx

import (
	"strings"

	"github.com/stmcginnis/gofish"
	"github.com/stmcginnis/gofish/schemas"
)

// collectHostNICs gathers data-plane NIC MACs from standard Redfish locations.
// Order prefers Systems EthernetInterfaces (HPE/Dell/iDRAC style), then
// Chassis/System NetworkAdapters → NetworkDeviceFunctions / NetworkPorts,
// Systems NetworkInterfaces, and finally PCIeFunctions that link Ethernet /
// NetworkDeviceFunction resources (common for add-in NICs on Supermicro and others).
// Results are deduped by MAC; empty / all-zero MACs are skipped. Vendor-agnostic.
func collectHostNICs(api *gofish.APIClient, sys *schemas.ComputerSystem) []NICInfo {
	acc := newNICAccumulator()
	if sys != nil {
		if nics, err := sys.EthernetInterfaces(); err == nil {
			for _, n := range nics {
				acc.addFromEthernetInterface(n, "system_ethernet")
			}
		}
		if nifaces, err := sys.NetworkInterfaces(); err == nil {
			for _, ni := range nifaces {
				addFromNetworkInterface(acc, ni)
			}
		}
		if devices, err := sys.PCIeDevices(); err == nil {
			for _, d := range devices {
				addFromPCIeDevice(acc, d)
			}
		}
	}
	if api != nil {
		if chassisList, err := api.Service.Chassis(); err == nil {
			for _, ch := range chassisList {
				if adapters, err := ch.NetworkAdapters(); err == nil {
					for _, a := range adapters {
						addFromNetworkAdapter(acc, a)
					}
				}
				if devices, err := ch.PCIeDevices(); err == nil {
					for _, d := range devices {
						addFromPCIeDevice(acc, d)
					}
				}
			}
		}
	}
	return acc.slice()
}

func addFromNetworkInterface(acc *nicAccumulator, ni *schemas.NetworkInterface) {
	if ni == nil {
		return
	}
	if ndfs, err := ni.NetworkDeviceFunctions(); err == nil {
		for _, ndf := range ndfs {
			acc.addFromNetworkDeviceFunction(ndf, "network_interface")
		}
	}
	if ports, err := ni.NetworkPorts(); err == nil {
		for _, p := range ports {
			acc.addFromNetworkPort(p, ni.ID, ni.Name, "network_interface_port")
		}
	}
}

func addFromNetworkAdapter(acc *nicAccumulator, a *schemas.NetworkAdapter) {
	if a == nil {
		return
	}
	if ndfs, err := a.NetworkDeviceFunctions(); err == nil {
		for _, ndf := range ndfs {
			acc.addFromNetworkDeviceFunction(ndf, "network_adapter")
		}
	}
	if ports, err := a.NetworkPorts(); err == nil {
		for _, p := range ports {
			acc.addFromNetworkPort(p, a.ID, a.Name, "network_adapter_port")
		}
	}
}

func addFromPCIeDevice(acc *nicAccumulator, d *schemas.PCIeDevice) {
	if d == nil {
		return
	}
	fns, err := d.PCIeFunctions()
	if err != nil {
		return
	}
	for _, fn := range fns {
		if fn == nil {
			continue
		}
		if eth, err := fn.EthernetInterfaces(); err == nil {
			for _, n := range eth {
				acc.addFromEthernetInterface(n, "pcie_function")
			}
		}
		if ndfs, err := fn.NetworkDeviceFunctions(); err == nil {
			for _, ndf := range ndfs {
				acc.addFromNetworkDeviceFunction(ndf, "pcie_function")
			}
		}
	}
}

// macsFromPCIeDevice returns Ethernet MACs linked under a PCIe device's functions.
func macsFromPCIeDevice(d *schemas.PCIeDevice) []string {
	if d == nil {
		return nil
	}
	acc := newNICAccumulator()
	addFromPCIeDevice(acc, d)
	out := make([]string, 0, len(acc.order))
	for _, key := range acc.order {
		info := acc.byKey[key]
		if info.MACAddress != "" {
			out = append(out, info.MACAddress)
		}
	}
	return out
}

type nicAccumulator struct {
	byKey map[string]NICInfo
	order []string
}

func newNICAccumulator() *nicAccumulator {
	return &nicAccumulator{byKey: map[string]NICInfo{}}
}

func (a *nicAccumulator) slice() []NICInfo {
	out := make([]NICInfo, 0, len(a.order))
	for _, k := range a.order {
		out = append(out, a.byKey[k])
	}
	return out
}

func (a *nicAccumulator) add(info NICInfo) {
	mac := usableMAC(info.MACAddress)
	perm := usableMAC(info.PermanentMAC)
	if mac == "" && perm != "" {
		mac = perm
		info.MACAddress = perm
	}
	if mac == "" {
		return
	}
	info.MACAddress = mac
	if perm != "" {
		info.PermanentMAC = perm
	}
	key := strings.ToLower(mac)
	if prev, ok := a.byKey[key]; ok {
		a.byKey[key] = mergeNICInfo(prev, info)
		return
	}
	a.byKey[key] = info
	a.order = append(a.order, key)
}

func (a *nicAccumulator) addFromEthernetInterface(n *schemas.EthernetInterface, source string) {
	if n == nil {
		return
	}
	// Supermicro (and some others) expose a virtual System EthernetInterface that
	// only talks to the BMC (Id=ToManager). That MAC must never be treated as a
	// host data NIC for Data DHCP / Resolve.
	if isBMCLinkEthernet(n.ID, n.Name) {
		return
	}
	info := NICInfo{
		ID:           n.ID,
		Name:         n.Name,
		MACAddress:   n.MACAddress,
		SpeedMbps:    derefInt(n.SpeedMbps),
		PermanentMAC: n.PermanentMACAddress,
		Source:       source,
	}
	for _, addr := range n.IPv4Addresses {
		if addr.Address != "" {
			info.IPv4 = append(info.IPv4, addr.Address)
		}
	}
	a.add(info)
}

func (a *nicAccumulator) addFromNetworkDeviceFunction(ndf *schemas.NetworkDeviceFunction, source string) {
	if ndf == nil {
		return
	}
	a.add(NICInfo{
		ID:           ndf.ID,
		Name:         ndf.Name,
		MACAddress:   ndf.Ethernet.MACAddress,
		PermanentMAC: ndf.Ethernet.PermanentMACAddress,
		Source:       source,
	})
	if eth, err := ndf.EthernetInterface(); err == nil && eth != nil {
		a.addFromEthernetInterface(eth, source)
	}
	if ethList, err := ndf.EthernetInterfaces(); err == nil {
		for _, eth := range ethList {
			a.addFromEthernetInterface(eth, source)
		}
	}
}

func (a *nicAccumulator) addFromNetworkPort(p *schemas.NetworkPort, parentID, parentName, source string) {
	if p == nil {
		return
	}
	id := p.ID
	if id == "" {
		id = parentID
	}
	name := p.Name
	if name == "" {
		name = parentName
	}
	for _, addr := range p.AssociatedNetworkAddresses {
		a.add(NICInfo{
			ID:         id,
			Name:       name,
			MACAddress: addr,
			Source:     source,
		})
	}
}

func mergeNICInfo(prev, next NICInfo) NICInfo {
	out := prev
	if out.ID == "" {
		out.ID = next.ID
	}
	if out.Name == "" {
		out.Name = next.Name
	}
	if out.PermanentMAC == "" {
		out.PermanentMAC = next.PermanentMAC
	}
	if out.SpeedMbps == 0 {
		out.SpeedMbps = next.SpeedMbps
	}
	if out.Source == "" {
		out.Source = next.Source
	}
	seenIP := map[string]struct{}{}
	for _, ip := range out.IPv4 {
		seenIP[ip] = struct{}{}
	}
	for _, ip := range next.IPv4 {
		if _, ok := seenIP[ip]; ok {
			continue
		}
		out.IPv4 = append(out.IPv4, ip)
	}
	return out
}

func usableMAC(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	compact := strings.ToLower(strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
			return r
		default:
			return -1
		}
	}, s))
	// Require a 48-bit MAC (12 hex digits); skip WWNs / garbage / all-zero.
	if len(compact) != 12 || strings.Trim(compact, "0") == "" {
		return ""
	}
	return s
}

// isBMCLinkEthernet detects Redfish EthernetInterface members that are the
// BMC↔host internal link, not a real host NIC (Supermicro Id=ToManager /
// Managers Id=ToHost). Matching by Id keeps this vendor-safe; the display
// Name is often the generic "Manager Ethernet Interface".
func isBMCLinkEthernet(id, name string) bool {
	_ = name
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "tomanager", "tohost":
		return true
	}
	return false
}
