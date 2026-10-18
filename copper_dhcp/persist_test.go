package copper_dhcp

import (
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestLeasePersistRoundTrip(t *testing.T) {
	cfg := Config{
		Enabled:    true,
		Listen:     "0.0.0.0:67",
		ServerIP:   "192.168.10.2",
		SubnetCIDR: "192.168.10.0/24",
		RangeStart: "192.168.10.100",
		RangeEnd:   "192.168.10.120",
		LeaseTime:  "1h",
		AllowAll:   true,
	}
	n, err := cfg.Normalize()
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "leases.json")
	store := NewStore(n)
	store.SetPersistPath(path)

	mac, _ := net.ParseMAC("aa:bb:cc:dd:ee:01")
	ip, err := store.Allocate(mac, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ip.String() != "192.168.10.100" {
		t.Fatalf("got ip %s", ip)
	}

	store2 := NewStore(n)
	store2.SetPersistPath(path)
	if err := store2.Load(); err != nil {
		t.Fatal(err)
	}
	leases := store2.Leases()
	if len(leases) != 1 {
		t.Fatalf("want 1 lease, got %d", len(leases))
	}
	if leases[0].MAC != "aa:bb:cc:dd:ee:01" || leases[0].IP.String() != "192.168.10.100" {
		t.Fatalf("unexpected lease %+v", leases[0])
	}
}

func TestLeasePersistSkipsExpired(t *testing.T) {
	cfg := Config{
		Enabled:    true,
		Listen:     "0.0.0.0:67",
		ServerIP:   "192.168.10.2",
		SubnetCIDR: "192.168.10.0/24",
		RangeStart: "192.168.10.100",
		RangeEnd:   "192.168.10.120",
		LeaseTime:  "1h",
		AllowAll:   true,
	}
	n, err := cfg.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "leases.json")
	store := NewStore(n)
	store.SetPersistPath(path)
	store.mu.Lock()
	store.byMAC["aa:bb:cc:dd:ee:02"] = &Lease{
		MAC:       "aa:bb:cc:dd:ee:02",
		IP:        net.ParseIP("192.168.10.101").To4(),
		ExpiresAt: time.Now().Add(-time.Minute),
	}
	store.byIP["192.168.10.101"] = store.byMAC["aa:bb:cc:dd:ee:02"]
	store.saveLocked()
	store.mu.Unlock()

	store2 := NewStore(n)
	store2.SetPersistPath(path)
	if err := store2.Load(); err != nil {
		t.Fatal(err)
	}
	if got := len(store2.Leases()); got != 0 {
		t.Fatalf("want 0 leases, got %d", got)
	}
}
