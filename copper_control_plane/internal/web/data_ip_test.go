package web

import (
	"net"
	"testing"

	"copper_dhcp"
)

func TestMatchLeaseToInventoryNICsUsesLiveLease(t *testing.T) {
	leases := leaseIPsByMAC([]copper_dhcp.Lease{
		{MAC: "40:a6:b7:b6:72:bc", IP: net.ParseIP("10.10.96.107")},
	})
	nics := []rawInvNIC{
		{mac: "40:a6:b7:b6:70:00", name: "ens1"}, // no lease
		{mac: "40:A6:B7:B6:72:BC", name: "141"},  // live lease
	}
	ip, mac := matchLeaseToInventoryNICs(nics, leases)
	if ip != "10.10.96.107" {
		t.Fatalf("want live lease 10.10.96.107, got %q", ip)
	}
	if mac != "40:a6:b7:b6:72:bc" {
		t.Fatalf("want normalized mac, got %q", mac)
	}
}

func TestMatchLeaseToInventoryNICsIgnoresStaleGuess(t *testing.T) {
	// No live lease for this server's NICs → empty (UI must not fall back to Detect .104).
	leases := leaseIPsByMAC([]copper_dhcp.Lease{
		{MAC: "aa:bb:cc:dd:ee:ff", IP: net.ParseIP("10.10.96.104")},
	})
	nics := []rawInvNIC{{mac: "40:a6:b7:b6:72:bc", name: "141"}}
	ip, mac := matchLeaseToInventoryNICs(nics, leases)
	if ip != "" || mac != "" {
		t.Fatalf("expected no match, got ip=%q mac=%q", ip, mac)
	}
}

func TestLeaseIPsByMACSkipsNilIP(t *testing.T) {
	got := leaseIPsByMAC([]copper_dhcp.Lease{
		{MAC: "aa:bb:cc:dd:ee:01", IP: nil},
		{MAC: "aa:bb:cc:dd:ee:02", IP: net.ParseIP("10.0.0.2")},
	})
	if len(got) != 1 || got["aa:bb:cc:dd:ee:02"] != "10.0.0.2" {
		t.Fatalf("got %#v", got)
	}
}
