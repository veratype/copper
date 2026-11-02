package provision

import (
	"strings"
	"testing"
)

func TestCidataUserDataDHCP(t *testing.T) {
	c := Cidata{
		Hostname:    "node1",
		Username:    "ubuntu",
		Password:    "secret",
		NetworkMode: NetDHCP,
		Interface:   "ens3",
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	ud := c.UserData()
	for _, want := range []string{"#cloud-config", "hostname: node1", "name: ubuntu"} {
		if !strings.Contains(ud, want) {
			t.Fatalf("user-data missing %q\n%s", want, ud)
		}
	}
	// Network belongs in network-config, not user-data.
	if strings.Contains(ud, "network:") || strings.Contains(ud, "dhcp4:") {
		t.Fatalf("user-data must not embed network config:\n%s", ud)
	}
	nc := c.NetworkConfig()
	for _, want := range []string{"version: 2", "ens3:", "dhcp4: true"} {
		if !strings.Contains(nc, want) {
			t.Fatalf("network-config missing %q\n%s", want, nc)
		}
	}
}

func TestCidataNetworkConfigStaticWithMAC(t *testing.T) {
	c := Cidata{
		Username:    "admin",
		Password:    "x",
		NetworkMode: NetStatic,
		Interface:   "eth0",
		MAC:         "aa:bb:cc:dd:ee:ff",
		AddressCIDR: "10.0.0.5/24",
		Gateway:     "10.0.0.1",
		DNS:         []string{"1.1.1.1"},
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	nc := c.NetworkConfig()
	for _, want := range []string{
		"dhcp4: false", "10.0.0.5/24", "macaddress:", "aa:bb:cc:dd:ee:ff",
		"set-name: eth0", "via: 10.0.0.1",
	} {
		if !strings.Contains(nc, want) {
			t.Fatalf("network-config missing %q\n%s", want, nc)
		}
	}
	// NoCloud seed is unwrapped; netplan drop-in must wrap under network:.
	if strings.HasPrefix(strings.TrimSpace(nc), "network:") {
		t.Fatalf("NoCloud network-config must not wrap under network:\n%s", nc)
	}
	np := c.NetplanConfig()
	for _, want := range []string{"network:", "  version: 2", "  ethernets:", "aa:bb:cc:dd:ee:ff"} {
		if !strings.Contains(np, want) {
			t.Fatalf("netplan missing %q\n%s", want, np)
		}
	}
}

func TestLooksLikePartition(t *testing.T) {
	cases := map[string]bool{
		"sda":       false,
		"sda1":      true,
		"nvme0n1":   false,
		"nvme0n1p1": true,
		"vda":       false,
		"vda2":      true,
		"md0":       false,
		"md127":     false,
		"md0p1":     true,
	}
	for name, want := range cases {
		if got := looksLikePartition(name); got != want {
			t.Errorf("%s: got %v want %v", name, got, want)
		}
	}
}

func TestCidataRejectsBadHostname(t *testing.T) {
	c := Cidata{
		Hostname:    "{myhost}-1",
		Username:    "ubuntu",
		Password:    "x",
		NetworkMode: NetDHCP,
		Interface:   "ens3",
	}
	if err := c.Validate(); err == nil {
		t.Fatal("expected invalid hostname error")
	}
}
