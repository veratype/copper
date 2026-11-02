package provision

import (
	"strings"
	"testing"
)

func TestRequestValidateStillRequiresDisk(t *testing.T) {
	r := Request{
		ImageURL:   "http://example/img",
		TargetDisk: "/dev/sda1",
		Cidata:     Cidata{Username: "u", Password: "p", Interface: "eth0"},
	}
	if err := r.Validate(); err == nil {
		t.Fatal("expected partition target to fail")
	}
}

func TestNetworkConfigStillSeparate(t *testing.T) {
	c := Cidata{Username: "u", Password: "p", Interface: "ens3", NetworkMode: NetDHCP}
	if strings.Contains(c.UserData(), "dhcp4:") {
		t.Fatal("user-data must not embed network")
	}
	if !strings.Contains(c.NetworkConfig(), "dhcp4: true") {
		t.Fatal("network-config missing dhcp")
	}
}
