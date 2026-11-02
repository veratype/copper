package store

import (
	"net"
	"path/filepath"
	"testing"
	"time"

	"copper_dhcp"
)

func TestDataDHCPLeasesRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	backend := st.NewDataDHCPLeaseBackend()
	leases := []copper_dhcp.Lease{
		{
			MAC:       "aa:bb:cc:dd:ee:01",
			IP:        net.ParseIP("10.0.0.50").To4(),
			ExpiresAt: time.Now().Add(time.Hour).UTC(),
			Reserved:  true,
		},
		{
			MAC:       "aa:bb:cc:dd:ee:02",
			IP:        net.ParseIP("10.0.0.51").To4(),
			ExpiresAt: time.Now().Add(-time.Minute).UTC(), // expired — skipped on save
		},
	}
	if err := backend.SaveLeases(leases); err != nil {
		t.Fatal(err)
	}
	got, err := backend.LoadLeases()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 lease, got %d", len(got))
	}
	if got[0].MAC != "aa:bb:cc:dd:ee:01" || got[0].IP.String() != "10.0.0.50" || !got[0].Reserved {
		t.Fatalf("unexpected %#v", got[0])
	}

	// Manager-style restore through copper_dhcp.Store.
	cfg := copper_dhcp.Config{
		Enabled:    true,
		Listen:     "0.0.0.0:67",
		ServerIP:   "10.0.0.2",
		SubnetCIDR: "10.0.0.0/24",
		RangeStart: "10.0.0.100",
		RangeEnd:   "10.0.0.200",
		LeaseTime:  "1h",
		AllowAll:   true,
		Reservations: []copper_dhcp.Reservation{
			{MAC: "aa:bb:cc:dd:ee:01", IP: "10.0.0.50"},
		},
	}
	n, err := cfg.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	dhcpStore := copper_dhcp.NewStore(n)
	dhcpStore.SetLeaseBackend(backend)
	if err := dhcpStore.Load(); err != nil {
		t.Fatal(err)
	}
	restored := dhcpStore.Leases()
	if len(restored) != 1 || restored[0].IP.String() != "10.0.0.50" {
		t.Fatalf("restore failed: %#v", restored)
	}
}
