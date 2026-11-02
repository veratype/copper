package web

import (
	"strings"
	"testing"

	"copper_control_plane/internal/provision"
	"copper_control_plane/internal/store"
)

func TestParseProvisionedForm(t *testing.T) {
	got, err := parseProvisionedForm("node-01", "10.0.0.20/24", "10.0.0.1", "1.1.1.1, 8.8.8.8")
	if err != nil {
		t.Fatal(err)
	}
	if got.Hostname != "node-01" || got.IPCIDR != "10.0.0.20/24" || got.GW != "10.0.0.1" {
		t.Fatalf("got %+v", got)
	}
	if got.DNS != "1.1.1.1, 8.8.8.8" {
		t.Fatalf("dns %q", got.DNS)
	}
	if _, err := parseProvisionedForm("", "not-a-cidr", "", ""); err == nil {
		t.Fatal("expected CIDR error")
	}
}

func TestPreferProvisionedNetworkWins(t *testing.T) {
	srv := &store.Server{
		Name:                "rack1",
		ProvisionedHostname: "prod-01",
		ProvisionedIPCIDR:   "10.0.0.50/24",
		ProvisionedGW:       "10.0.0.1",
		ProvisionedDNS:      "9.9.9.9",
	}
	tmpl := provision.Cidata{
		Hostname:    "node-{nn}",
		Username:    "ubuntu",
		Password:    "x",
		NetworkMode: provision.NetStatic,
		Interface:   "ens3",
		AddressCIDR: "10.0.0.{n+10}/24",
		Gateway:     "10.0.0.254",
		DNS:         []string{"1.1.1.1"},
	}
	got, err := preferProvisionedNetwork(tmpl, srv, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Hostname != "prod-01" {
		t.Fatalf("hostname %q", got.Hostname)
	}
	if got.AddressCIDR != "10.0.0.50/24" {
		t.Fatalf("cidr %q", got.AddressCIDR)
	}
	if got.Gateway != "10.0.0.1" {
		t.Fatalf("gw %q", got.Gateway)
	}
	if len(got.DNS) != 1 || got.DNS[0] != "9.9.9.9" {
		t.Fatalf("dns %+v", got.DNS)
	}
}

func TestPreferProvisionedNetworkBlankFallsBack(t *testing.T) {
	srv := &store.Server{Name: "rack1"}
	tmpl := provision.Cidata{
		Hostname:    "node-{n}",
		Username:    "ubuntu",
		Password:    "x",
		NetworkMode: provision.NetStatic,
		Interface:   "ens3",
		AddressCIDR: "10.0.0.{n+10}/24",
		Gateway:     "10.0.0.1",
		DNS:         []string{"1.1.1.1"},
	}
	got, err := preferProvisionedNetwork(tmpl, srv, 2, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Hostname != "node-2" {
		t.Fatalf("hostname %q", got.Hostname)
	}
	if got.AddressCIDR != "10.0.0.12/24" {
		t.Fatalf("cidr %q", got.AddressCIDR)
	}
	if got.Gateway != "10.0.0.1" {
		t.Fatalf("gw %q", got.Gateway)
	}
}

func TestPreferProvisionedIPForcesStatic(t *testing.T) {
	srv := &store.Server{
		Name:              "rack1",
		ProvisionedIPCIDR: "10.0.0.9/24",
		ProvisionedGW:     "10.0.0.1",
	}
	tmpl := provision.Cidata{
		Hostname:    "",
		Username:    "ubuntu",
		Password:    "x",
		NetworkMode: provision.NetDHCP,
		Interface:   "ens3",
	}
	got, err := preferProvisionedNetwork(tmpl, srv, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.NetworkMode != provision.NetStatic {
		t.Fatalf("mode %q", got.NetworkMode)
	}
	if got.Hostname != "rack1" {
		t.Fatalf("hostname fallback %q", got.Hostname)
	}
	if got.AddressCIDR != "10.0.0.9/24" || got.Gateway != "10.0.0.1" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseServersCSV_ProvisionedColumns(t *testing.T) {
	in := `chassis_id,bmc_ip,mac_address,username,password,provisioned_hostname,provisioned_ip_cidr,provisioned_gw,provisioned_dns
ch-1,,,admin,secret,node-1,10.0.0.11/24,10.0.0.1,"1.1.1.1,8.8.8.8"
`
	rows, res := parseServersCSV(strings.NewReader(in))
	if res.ParseErr != "" {
		t.Fatalf("parse: %s", res.ParseErr)
	}
	if len(res.RowErrs) != 0 {
		t.Fatalf("row errs: %+v", res.RowErrs)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	r := rows[0]
	if r.ProvisionedHostname != "node-1" || r.ProvisionedIPCIDR != "10.0.0.11/24" || r.ProvisionedGW != "10.0.0.1" {
		t.Fatalf("row: %+v", r)
	}
	if r.ProvisionedDNS != "1.1.1.1, 8.8.8.8" {
		t.Fatalf("dns %q", r.ProvisionedDNS)
	}
}
