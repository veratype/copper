package copper_dhcp

import (
	"net"
	"testing"
)

func TestNormalizeAndAllocate(t *testing.T) {
	cfg := Config{
		Enabled:    true,
		ServerIP:   "192.168.10.2",
		SubnetCIDR: "192.168.10.0/24",
		RangeStart: "192.168.10.100",
		RangeEnd:   "192.168.10.102",
		Gateway:    "192.168.10.1",
		DNS:        []string{"1.1.1.1"},
		LeaseTime:  "1m",
		AllowAll:   false,
		AllowMACs:  []string{"aa:bb:cc:dd:ee:01"},
		Reservations: []Reservation{
			{MAC: "aa:bb:cc:dd:ee:99", IP: "192.168.10.50"},
		},
	}
	n, err := cfg.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(n)

	mac1, _ := net.ParseMAC("aa:bb:cc:dd:ee:01")
	ip, err := store.Allocate(mac1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !ip.Equal(net.ParseIP("192.168.10.100")) {
		t.Fatalf("expected .100 got %s", ip)
	}

	macDenied, _ := net.ParseMAC("11:22:33:44:55:66")
	if n.MACAllowed(macDenied) {
		t.Fatal("denied mac should not be allowed")
	}

	macRes, _ := net.ParseMAC("aa:bb:cc:dd:ee:99")
	if !n.MACAllowed(macRes) {
		t.Fatal("reservation mac should be allowed")
	}
	ip, err = store.Allocate(macRes, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !ip.Equal(net.ParseIP("192.168.10.50")) {
		t.Fatalf("expected reserved .50 got %s", ip)
	}
}

func TestPoolExhausted(t *testing.T) {
	cfg := Config{
		Enabled:    true,
		ServerIP:   "10.0.0.1",
		SubnetCIDR: "10.0.0.0/24",
		RangeStart: "10.0.0.10",
		RangeEnd:   "10.0.0.11",
		AllowAll:   true,
	}
	n, err := cfg.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(n)
	for i, macs := range []string{"aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02"} {
		mac, _ := net.ParseMAC(macs)
		if _, err := store.Allocate(mac, nil); err != nil {
			t.Fatalf("alloc %d: %v", i, err)
		}
	}
	mac, _ := net.ParseMAC("aa:bb:cc:dd:ee:03")
	if _, err := store.Allocate(mac, nil); err == nil {
		t.Fatal("expected pool exhausted")
	}
}
