package redfishx

import "testing"

func TestUsableMAC(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"AA:BB:CC:DD:EE:FF", "AA:BB:CC:DD:EE:FF"},
		{"  aa-bb-cc-dd-ee-01 ", "aa-bb-cc-dd-ee-01"},
		{"", ""},
		{"00:00:00:00:00:00", ""},
		{"00-00-00-00-00-00", ""},
		{"not-a-mac", ""},
	}
	for _, tc := range tests {
		if got := usableMAC(tc.in); got != tc.want {
			t.Fatalf("usableMAC(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestNICAccumulatorDedupeAndMerge(t *testing.T) {
	acc := newNICAccumulator()
	acc.add(NICInfo{
		ID: "1", Name: "eth0", MACAddress: "AA:BB:CC:DD:EE:01",
		Source: "system_ethernet", IPv4: []string{"10.0.0.1"},
	})
	acc.add(NICInfo{
		ID: "slot2", Name: "NIC.Slot.2-1", MACAddress: "aa:bb:cc:dd:ee:01",
		PermanentMAC: "AA:BB:CC:DD:EE:01", Source: "pcie_function",
		IPv4: []string{"10.0.0.1", "10.0.0.2"},
	})
	acc.add(NICInfo{MACAddress: "00:00:00:00:00:00", Name: "junk"})
	acc.add(NICInfo{
		ID: "2", Name: "eth1", MACAddress: "AA:BB:CC:DD:EE:02",
		Source: "network_adapter",
	})

	got := acc.slice()
	if len(got) != 2 {
		t.Fatalf("len=%d want 2 %#v", len(got), got)
	}
	if got[0].ID != "1" || got[0].Source != "system_ethernet" {
		t.Fatalf("first entry should keep system_ethernet identity: %#v", got[0])
	}
	if got[0].PermanentMAC != "AA:BB:CC:DD:EE:01" {
		t.Fatalf("permanent mac not merged: %#v", got[0])
	}
	if len(got[0].IPv4) != 2 {
		t.Fatalf("ipv4 merge: %#v", got[0].IPv4)
	}
	if got[1].MACAddress != "AA:BB:CC:DD:EE:02" {
		t.Fatalf("second: %#v", got[1])
	}
}

func TestNICAccumulatorPermanentOnly(t *testing.T) {
	acc := newNICAccumulator()
	acc.add(NICInfo{ID: "x", PermanentMAC: "11:22:33:44:55:66", Source: "network_adapter"})
	got := acc.slice()
	if len(got) != 1 || got[0].MACAddress != "11:22:33:44:55:66" {
		t.Fatalf("got %#v", got)
	}
}

func TestIsBMCLinkEthernet(t *testing.T) {
	if !isBMCLinkEthernet("ToManager", "Manager Ethernet Interface") {
		t.Fatal("ToManager must be skipped (Supermicro System EthernetInterfaces)")
	}
	if !isBMCLinkEthernet("ToHost", "Manager Ethernet Interface") {
		t.Fatal("ToHost must be skipped")
	}
	if isBMCLinkEthernet("1", "AOC_NIC1") {
		t.Fatal("real host NIC must not be skipped")
	}
}

func TestFilterBMCMACsFromHostNICs(t *testing.T) {
	nics := []NICInfo{
		{ID: "1", MACAddress: "90:5a:08:11:b6:fe"},
		{ID: "ToManager", MACAddress: "BE:3A:F2:B6:05:9F"},
	}
	bmcs := []BMCInfo{{
		MACAddresses: []string{"7C:C2:55:60:4C:AC", "BE:3A:F2:B6:05:9F"},
	}}
	got := filterBMCMACsFromHostNICs(nics, bmcs)
	if len(got) != 1 || got[0].MACAddress != "90:5a:08:11:b6:fe" {
		t.Fatalf("got %#v", got)
	}
}

func TestBoardSerialFromOEM(t *testing.T) {
	raw := []byte(`{"Supermicro":{"BoardSerialNumber":"UM246S004707","GUID":"x"}}`)
	if got := boardSerialFromOEM(raw); got != "UM246S004707" {
		t.Fatalf("got %q", got)
	}
	if got := boardSerialFromOEM([]byte(`{"Other":{"boardserialnumber":"ABC"}}`)); got != "ABC" {
		t.Fatalf("case-insensitive key got %q", got)
	}
	if got := boardSerialFromOEM(nil); got != "" {
		t.Fatalf("empty got %q", got)
	}
}
