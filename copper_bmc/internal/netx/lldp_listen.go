package netx

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/afpacket"
	"github.com/google/gopacket/layers"
	"golang.org/x/net/bpf"
)

// LLDPLogger is optional activity logging for the sniffer.
type LLDPLogger func(format string, args ...any)

// ListenLLDP passively captures LLDP on iface into table until the handle fails.
// Uses AF_PACKET (no libpcap). Requires CAP_NET_RAW / root. Blocks the caller.
func ListenLLDP(iface string, table *LLDPTable, logf LLDPLogger) error {
	if table == nil {
		return fmt.Errorf("lldp table is nil")
	}
	iface = strings.TrimSpace(iface)
	if iface == "" {
		return fmt.Errorf("lldp iface is empty")
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}

	handle, err := afpacket.NewTPacket(
		afpacket.OptInterface(iface),
		afpacket.OptFrameSize(65536),
		afpacket.OptBlockSize(1<<20),
		afpacket.OptNumBlocks(64),
		afpacket.OptPollTimeout(time.Second),
	)
	if err != nil {
		return fmt.Errorf("lldp open %s: %w (need CAP_NET_RAW / root)", iface, err)
	}
	defer handle.Close()

	// Kernel filter: EtherType 0x88CC only.
	if err := handle.SetBPF(lldpBPFFilter()); err != nil {
		return fmt.Errorf("lldp bpf: %w", err)
	}
	logf("listening for LLDP on %s (AF_PACKET)", iface)

	for {
		data, _, err := handle.ZeroCopyReadPacketData()
		if err != nil {
			if err == afpacket.ErrTimeout {
				continue
			}
			return fmt.Errorf("lldp read %s: %w", iface, err)
		}
		pkt := gopacket.NewPacket(data, layers.LayerTypeEthernet, gopacket.Default)
		n, ok := parseLLDPPacket(pkt, iface)
		if !ok {
			continue
		}
		table.Upsert(n)
		logf("neighbor chassis=%s subtype=%s src_mac=%s port=%s sys=%q",
			n.ChassisID, n.ChassisSubtype, n.SrcMAC, n.PortID, n.SysName)
	}
}

func lldpBPFFilter() []bpf.RawInstruction {
	// tcpdump: ether proto 0x88cc
	return []bpf.RawInstruction{
		{Op: 0x28, Jt: 0, Jf: 0, K: 12},       // ldh [12]
		{Op: 0x15, Jt: 0, Jf: 1, K: 0x88cc},   // jeq #0x88cc
		{Op: 0x6, Jt: 0, Jf: 0, K: 0x40000},   // ret #262144
		{Op: 0x6, Jt: 0, Jf: 0, K: 0},         // ret #0
	}
}

func parseLLDPPacket(pkt gopacket.Packet, iface string) (LLDPNeighbor, bool) {
	lldpLayer := pkt.Layer(layers.LayerTypeLinkLayerDiscovery)
	if lldpLayer == nil {
		return LLDPNeighbor{}, false
	}
	lldp := lldpLayer.(*layers.LinkLayerDiscovery)

	n := LLDPNeighbor{
		ChassisID:      formatChassisID(lldp.ChassisID.ID, lldp.ChassisID.Subtype),
		ChassisSubtype: lldp.ChassisID.Subtype.String(),
		PortID:         formatPortID(lldp.PortID),
		PortSubtype:    lldp.PortID.Subtype.String(),
		TTL:            lldp.TTL,
		Iface:          iface,
	}

	if ethLayer := pkt.Layer(layers.LayerTypeEthernet); ethLayer != nil {
		eth := ethLayer.(*layers.Ethernet)
		n.SrcMAC = eth.SrcMAC.String()
	}
	if n.SrcMAC == "" {
		return LLDPNeighbor{}, false
	}

	if infoLayer := pkt.Layer(layers.LayerTypeLinkLayerDiscoveryInfo); infoLayer != nil {
		info := infoLayer.(*layers.LinkLayerDiscoveryInfo)
		n.SysName = strings.TrimSpace(info.SysName)
		n.SysDescription = strings.TrimSpace(info.SysDescription)
		if addr := formatMgmtAddr(info.MgmtAddress); addr != "" {
			n.MgmtAddrs = append(n.MgmtAddrs, addr)
		}
	}
	return n, true
}

func formatChassisID(id []byte, sub layers.LLDPChassisIDSubType) string {
	switch sub {
	case layers.LLDPChassisIDSubTypeMACAddr:
		return net.HardwareAddr(id).String()
	case layers.LLDPChassisIDSubTypeNetworkAddr:
		if len(id) > 1 {
			return net.IP(id[1:]).String()
		}
		return fmt.Sprintf("%x", id)
	default:
		if isPrintable(id) {
			return string(id)
		}
		return fmt.Sprintf("%x", id)
	}
}

func formatPortID(p layers.LLDPPortID) string {
	if p.Subtype == layers.LLDPPortIDSubtypeMACAddr {
		return net.HardwareAddr(p.ID).String()
	}
	if isPrintable(p.ID) {
		return string(p.ID)
	}
	return fmt.Sprintf("%x", p.ID)
}

func formatMgmtAddr(m layers.LLDPMgmtAddress) string {
	switch len(m.Address) {
	case 4, 16:
		return net.IP(m.Address).String()
	default:
		if len(m.Address) == 0 {
			return ""
		}
		return fmt.Sprintf("%x", m.Address)
	}
}

func isPrintable(b []byte) bool {
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return len(b) > 0
}
