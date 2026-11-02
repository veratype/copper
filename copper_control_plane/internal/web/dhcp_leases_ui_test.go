package web

import (
	"testing"
)

func TestFillDataDHCPFormTextsIncludesOrphanLeases(t *testing.T) {
	s := &Server{}
	pd := pageData{}
	leaseByMAC := map[string]string{
		"aa:bb:cc:dd:ee:01": "10.0.0.50",
		"aa:bb:cc:dd:ee:02": "10.0.0.51",
	}
	s.fillDataDHCPFormTexts(&pd, nil, leaseByMAC)

	if len(pd.HostServers) != 1 {
		t.Fatalf("want 1 orphan group, got %d", len(pd.HostServers))
	}
	g := pd.HostServers[0]
	if g.Name != "Leases without inventory" {
		t.Fatalf("unexpected group name %q", g.Name)
	}
	if !g.HasLease || !g.Open {
		t.Fatalf("orphan group should be open with lease")
	}
	if g.NICCount != 2 || g.LeaseCount != 2 {
		t.Fatalf("want 2 leased NICs, got nic=%d lease=%d", g.NICCount, g.LeaseCount)
	}
	if pd.HostBoundCount != 1 {
		t.Fatalf("want HostBoundCount=1, got %d", pd.HostBoundCount)
	}
}

func TestEnrichDataLeasesSetsServerID(t *testing.T) {
	s := &Server{}
	pd := pageData{
		Leases: []leaseRow{{MAC: "aa:bb:cc:dd:ee:01", IP: "10.0.0.50"}},
	}
	s.enrichDataLeasesWith([]inventoryNIC{{
		ServerID: 42, ServerName: "node-a", MAC: "aa:bb:cc:dd:ee:01", NICName: "eth0", Hardware: "HPE",
	}}, &pd)
	if pd.Leases[0].ServerID != 42 || pd.Leases[0].ServerName != "node-a" {
		t.Fatalf("enrich missing server id/name: %+v", pd.Leases[0])
	}
	if pd.Leases[0].NIC != "eth0" || pd.Leases[0].Hardware != "HPE" {
		t.Fatalf("enrich missing nic/hw: %+v", pd.Leases[0])
	}
}

func TestFillDataDHCPFormTextsInventoryLeaseNotDuplicatedAsOrphan(t *testing.T) {
	s := &Server{}
	pd := pageData{}
	hostNICs := []inventoryNIC{{
		ServerID:   1,
		ServerName: "node-a",
		MAC:        "aa:bb:cc:dd:ee:01",
		NICName:    "eth0",
	}}
	leaseByMAC := map[string]string{
		"aa:bb:cc:dd:ee:01": "10.0.0.50",
		"aa:bb:cc:dd:ee:99": "10.0.0.99",
	}
	s.fillDataDHCPFormTexts(&pd, hostNICs, leaseByMAC)

	if len(pd.HostServers) != 2 {
		t.Fatalf("want inventory + orphan groups, got %d %+v", len(pd.HostServers), pd.HostServers)
	}
	var orphan, inv *hostServerGroup
	for i := range pd.HostServers {
		g := &pd.HostServers[i]
		if g.Name == "Leases without inventory" {
			orphan = g
		}
		if g.Name == "node-a" {
			inv = g
		}
	}
	if inv == nil || orphan == nil {
		t.Fatalf("missing groups: inv=%v orphan=%v", inv != nil, orphan != nil)
	}
	if orphan.NICCount != 1 || orphan.NICs[0].MAC != "aa:bb:cc:dd:ee:99" {
		t.Fatalf("orphan should only hold ee:99, got %+v", orphan.NICs)
	}
	if !inv.HasLease || inv.NICs[0].LeaseIP != "10.0.0.50" {
		t.Fatalf("inventory NIC should show lease, got %+v", inv.NICs[0])
	}
}
