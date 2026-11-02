package web

import (
	"encoding/json"
	"strings"
	"testing"

	"copper_control_plane/internal/storage"
)

func TestMergeLiveOSNICsIntoInventory(t *testing.T) {
	payload := `{
  "system": {"manufacturer": "Supermicro", "serial_number": "SN1", "uuid": "U-1"},
  "network": [
    {"id": "1", "name": "eth0", "mac_address": "AA:BB:CC:DD:EE:01", "source": "system_ethernet"}
  ],
  "counts": {"network": 1, "cpu": 2}
}`
	live := []storage.NICInfo{
		{Name: "eth0", MAC: "aa:bb:cc:dd:ee:01"},
		{Name: "eth1", MAC: "11:22:33:44:55:66", State: "up", Addresses: []string{"10.10.96.105/24"}},
		{Name: "lo", MAC: "00:00:00:00:00:00"},
	}
	added, out, err := mergeLiveOSNICsIntoInventory(payload, live)
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Fatalf("added=%d want 1\n%s", added, out)
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(out), &root); err != nil {
		t.Fatal(err)
	}
	net, _ := root["network"].([]any)
	if len(net) != 2 {
		t.Fatalf("network len=%d", len(net))
	}
	counts, _ := root["counts"].(map[string]any)
	if int(counts["network"].(float64)) != 2 {
		t.Fatalf("counts.network=%v", counts["network"])
	}
	if !strings.Contains(out, liveOSClaimSource) {
		t.Fatalf("missing source: %s", out)
	}
	_, nics := parseInventoryHostNICs(out)
	if len(nics) < 2 {
		t.Fatalf("parseInventoryHostNICs got %d", len(nics))
	}
}

func TestLiveHostMatchesServerByKnownMACOnHost(t *testing.T) {
	known := map[string]struct{}{
		"aa:bb:cc:dd:ee:01": {},
		"aa:bb:cc:dd:ee:02": {},
	}
	live := []storage.NICInfo{
		{Name: "eth0", MAC: "AA:BB:CC:DD:EE:01"},
		{Name: "eth1", MAC: "40:A6:B7:B6:70:14"},
	}
	if !liveHostMatchesServer(live, liveDMI{}, known, hostIdentity{}) {
		t.Fatal("expected match via known internal MAC on host")
	}
	if liveHostMatchesServer([]storage.NICInfo{{MAC: "40:A6:B7:B6:70:14"}}, liveDMI{}, known, hostIdentity{}) {
		t.Fatal("orphan-only host must not match without known MAC/DMI")
	}
}

func TestLiveHostMatchesServerViaChassisSerial(t *testing.T) {
	ident := hostIdentity{ChassisSerials: []string{"CN711025FM"}}
	dmi := liveDMI{ChassisSerial: "CN711025FM"}
	if !liveHostMatchesServer(nil, dmi, nil, ident) {
		t.Fatal("expected match via chassis serial")
	}
	if liveHostMatchesServer(nil, liveDMI{ChassisSerial: "OTHER"}, nil, ident) {
		t.Fatal("wrong chassis serial must not match")
	}
	// Placeholder DMI values must not match.
	if liveHostMatchesServer(nil, liveDMI{ChassisSerial: "None"}, nil, hostIdentity{ChassisSerials: []string{"None"}}) {
		t.Fatal("placeholder chassis serial must not match")
	}
}

// HPE / SuperMicro: Redfish often only has system.serial_number in an old
// snapshot (chassis[] null), while live OS exposes the same FRU as chassis_serial.
func TestLiveHostMatchesServerCrossFieldIdentity(t *testing.T) {
	// Real serials from lab BMCs 172.16.113.201 / .210.
	payload := `{"system":{"serial_number":"CN711025FM","uuid":"38393638-3435-4E43-3731-31303235464D"},"network":[]}`
	ident := inventoryHostIdentity(payload)
	if ident.Serial != "CN711025FM" || len(ident.ChassisSerials) != 0 {
		t.Fatalf("fixture identity %+v", ident)
	}
	// Live DMI only fills chassis_serial (product_serial empty) — must still match.
	if !liveHostMatchesServer(nil, liveDMI{ChassisSerial: "CN711025FM"}, nil, ident) {
		t.Fatal("live chassis_serial must match inventory system.serial_number")
	}
	// And the reverse: inventory chassis[] only, live product_serial only.
	ident2 := hostIdentity{ChassisSerials: []string{"CN711025FL"}}
	if !liveHostMatchesServer(nil, liveDMI{ProductSerial: "CN711025FL"}, nil, ident2) {
		t.Fatal("live product_serial must match inventory chassis[].serial_number")
	}
	// UUID either side.
	if !liveHostMatchesServer(nil, liveDMI{ProductUUID: "38393638-3435-4E43-3731-31303235464D"}, nil, ident) {
		t.Fatal("live product_uuid must match inventory system.uuid")
	}
	if liveHostMatchesServer(nil, liveDMI{ChassisSerial: "CN711025FL"}, nil, ident) {
		t.Fatal("wrong serial must not match")
	}
}

func TestLiveHostMatchesServerViaPCIeInventoryMACs(t *testing.T) {
	payload := `{
  "network": [],
  "pcie": [{"name": "NIC", "mac_addresses": ["AA:BB:CC:DD:EE:01", "AA:BB:CC:DD:EE:02"]}],
  "chassis": [{"id": "1", "serial_number": "CHASSIS-1"}],
  "system": {"serial_number": "SM1"}
}`
	known := inventoryMACSet(payload)
	ident := inventoryHostIdentity(payload)
	live := []storage.NICInfo{
		{Name: "eth0", MAC: "aa:bb:cc:dd:ee:01"},
		{Name: "eth1", MAC: "40:a6:b7:b6:70:14"},
	}
	if !liveHostMatchesServer(live, liveDMI{}, known, ident) {
		t.Fatal("expected match via pcie mac_addresses harvest")
	}
	if !liveHostMatchesServer(nil, liveDMI{ChassisSerial: "CHASSIS-1"}, nil, ident) {
		t.Fatal("expected match via inventory chassis serial")
	}
}

func TestLiveHostMatchesServerDMIBackup(t *testing.T) {
	known := map[string]struct{}{"aa:bb:cc:dd:ee:01": {}}
	ident := hostIdentity{Serial: "SN-99", UUID: "ABCD"}
	if !liveHostMatchesServer([]storage.NICInfo{{MAC: "11:22:33:44:55:66"}}, liveDMI{ProductSerial: "SN-99"}, known, ident) {
		t.Fatal("expected serial match")
	}
}

func TestCountOrphanHits(t *testing.T) {
	orphans := map[string]struct{}{"40:a6:b7:b6:70:14": {}}
	n := countOrphanHits([]storage.NICInfo{
		{MAC: "aa:bb:cc:dd:ee:01"},
		{MAC: "40:A6:B7:B6:70:14"},
	}, orphans)
	if n != 1 {
		t.Fatalf("got %d", n)
	}
}

func TestInventoryHostIdentity(t *testing.T) {
	id := inventoryHostIdentity(`{
  "system":{"serial_number":" E497 ","uuid":"Uu"},
  "chassis":[
    {"id":"1","serial_number":" CHASSIS-A ", "board_serial":" BOARD-1 "},
    {"id":"2","serial_number":"None"},
    {"id":"3","serial_number":"CHASSIS-A"}
  ]
}`)
	if id.Serial != "E497" || id.UUID != "Uu" {
		t.Fatalf("system %+v", id)
	}
	if len(id.ChassisSerials) != 1 || id.ChassisSerials[0] != "CHASSIS-A" {
		t.Fatalf("chassis serials %+v (must not use chassis_id; dedupe; skip None)", id.ChassisSerials)
	}
	if len(id.BoardSerials) != 1 || id.BoardSerials[0] != "BOARD-1" {
		t.Fatalf("board serials %+v", id.BoardSerials)
	}
}

// Real Supermicro X13 shape from 172.16.113.211: system / chassis / board FRUs differ.
func TestLiveHostMatchesServerSupermicroDistinctFRUs(t *testing.T) {
	payload := `{
  "system":{"serial_number":"E497867X4901901","uuid":"1B734000-432A-11EF-8000-7CC255604CAC"},
  "chassis":[{"id":"1","serial_number":"CLA26FN29BH1101","board_serial":"UM246S004707"}],
  "network":[{"id":"1","mac_address":"90:5a:08:11:b6:fe"}]
}`
	ident := inventoryHostIdentity(payload)
	known := inventoryMACSet(payload)
	tokens := inventoryIdentityTokens(ident)
	for _, want := range []string{"e497867x4901901", "cla26fn29bh1101", "um246s004707", "1b734000-432a-11ef-8000-7cc255604cac"} {
		if _, ok := tokens[want]; !ok {
			t.Fatalf("missing identity token %q in %#v", want, tokens)
		}
	}
	// Live OS may expose any one of the three DMI serial files.
	if !liveHostMatchesServer(nil, liveDMI{ProductSerial: "E497867X4901901"}, nil, ident) {
		t.Fatal("product_serial must match system.serial_number")
	}
	if !liveHostMatchesServer(nil, liveDMI{ChassisSerial: "CLA26FN29BH1101"}, nil, ident) {
		t.Fatal("chassis_serial must match chassis.serial_number")
	}
	if !liveHostMatchesServer(nil, liveDMI{BoardSerial: "UM246S004707"}, nil, ident) {
		t.Fatal("board_serial must match chassis.board_serial")
	}
	// Known host NIC still matches.
	if !liveHostMatchesServer([]storage.NICInfo{{MAC: "90:5a:08:11:b6:fe"}}, liveDMI{}, known, ident) {
		t.Fatal("inventory MAC must still match")
	}
	if liveHostMatchesServer(nil, liveDMI{ChassisSerial: "OTHER-CHASSIS", ProductSerial: "OTHER-SYS", BoardSerial: "OTHER-BOARD"}, nil, ident) {
		t.Fatal("unrelated FRUs must not match")
	}
}

func TestClaimExcludeOmitsOtherLeaseOwners(t *testing.T) {
	s := &Server{}
	s.markClaimTaken(7, "10.0.0.1")
	s.markClaimTaken(8, "10.0.0.2")
	ex := s.claimExcludeIDs("10.0.0.3")
	if _, ok := ex[7]; !ok {
		t.Fatal("server 7 should be omitted")
	}
	if _, ok := ex[8]; !ok {
		t.Fatal("server 8 should be omitted")
	}
	// Same lease IP may re-match its own server (retry).
	exSame := s.claimExcludeIDs("10.0.0.1")
	if _, ok := exSame[7]; ok {
		t.Fatal("server 7 must not be omitted for its own lease IP")
	}
	if _, ok := exSame[8]; !ok {
		t.Fatal("server 8 still omitted for a different lease")
	}
}
