//go:build !linux

package netx

import "fmt"

// ListenLLDP is a compile-time stub on non-Linux platforms (AF_PACKET is Linux-only).
func ListenLLDP(iface string, table *LLDPTable, logf LLDPLogger) error {
	return fmt.Errorf("LLDP capture is not supported on this platform (Linux only)")
}
