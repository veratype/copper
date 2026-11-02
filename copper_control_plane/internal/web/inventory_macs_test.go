package web

import (
	"strings"
	"testing"
)

func TestParseInventoryHostNICs(t *testing.T) {
	payload := `{
  "system": {
    "manufacturer": "Dell Inc.",
    "model": "PowerEdge R740",
    "serial_number": "ABC123"
  },
  "network": [
    {"id": "NIC.1", "name": "NIC.Integrated.1-1", "mac_address": "AA:BB:CC:DD:EE:01", "permanent_mac": "AA:BB:CC:DD:EE:01"},
    {"id": "NIC.2", "name": "NIC.Slot.2-1", "mac_address": "aa:bb:cc:dd:ee:02"}
  ]
}`
	hw, nics := parseInventoryHostNICs(payload)
	if hw != "Dell Inc. PowerEdge R740 · SN ABC123" {
		t.Fatalf("hardware: got %q", hw)
	}
	// Deduped: ee:01 once (permanent==current) + ee:02.
	if len(nics) != 2 {
		t.Fatalf("nics: got %d %#v", len(nics), nics)
	}
}

func TestCollectMACsFromJSONNestedPCIeAndBMC(t *testing.T) {
	// Supermicro-style: host NICs only under pcie[].mac_addresses; BMC under bmc.
	// BMC MACs must NOT be harvested — they pollute Data DHCP server mapping.
	payload := `{
  "bmc": [{"mac_addresses": ["B4:7A:F1:60:71:86", "0A:CA:FE:F0:0D:04"]}],
  "counts": {"network": 0, "pcie": 1},
  "cpu": [],
  "network": [],
  "pcie": [
    {
      "id": "NIC1",
      "name": "AOC-ETH",
      "mac_addresses": ["40:A6:B7:B6:70:14", "40:A6:B7:B6:70:15"]
    }
  ],
  "system": {"manufacturer": "Supermicro", "serial_number": "SM1"}
}`
	got := collectHostMACsFromJSON(payload)
	want := map[string]bool{
		"40:a6:b7:b6:70:14": true,
		"40:a6:b7:b6:70:15": true,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d MACs %#v (bmc must be skipped)", len(got), got)
	}
	for _, m := range got {
		if !want[strings.ToLower(m)] {
			t.Fatalf("unexpected MAC %q in %#v", m, got)
		}
	}

	set := inventoryMACSet(payload)
	if len(set) != 2 {
		t.Fatalf("inventoryMACSet size=%d want 2", len(set))
	}
	if _, ok := set["40:a6:b7:b6:70:14"]; !ok {
		t.Fatal("pcie MAC missing from inventoryMACSet — Resolve would fail")
	}
	if _, ok := set["b4:7a:f1:60:71:86"]; ok {
		t.Fatal("BMC MAC must not enter host inventoryMACSet")
	}

	hw, nics := parseInventoryHostNICs(payload)
	if !strings.Contains(hw, "Supermicro") {
		t.Fatalf("hardware %q", hw)
	}
	if len(nics) != 2 {
		t.Fatalf("parseInventoryHostNICs got %d want 2 (pcie only, no bmc)", len(nics))
	}
}

func TestCollectMACsFromJSONSkipsZeroAndNonMAC(t *testing.T) {
	payload := `{
  "network": [{"mac_address": "00:00:00:00:00:00"}],
  "system": {"serial_number": "NOT-A-MAC", "uuid": "12345678-1234-1234-1234-123456789abc"},
  "pcie": [{"mac_addresses": ["AA:BB:CC:DD:EE:FF", "garbage"]}]
}`
	got := collectMACsFromJSON(payload)
	if len(got) != 1 || strings.ToLower(got[0]) != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("got %#v", got)
	}
}
