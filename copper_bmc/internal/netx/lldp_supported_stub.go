//go:build !linux

package netx

// LLDPSupported reports whether this binary can capture LLDP (AF_PACKET).
func LLDPSupported() bool { return false }
