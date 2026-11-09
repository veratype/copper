package web

import (
	"net/url"
	"testing"

	"copper_control_plane/internal/store"
)

func TestHardwareListFilterMatch(t *testing.T) {
	hw := &store.HardwareFacts{
		CPUCount:  2,
		CPUModels: []string{"Xeon Gold 6330"},
		CPUSummary: "2× Xeon Gold 6330",
		RAMGiB:    256,
		HasGPU:    true,
		GPUCount:  2,
		GPUModels: []string{"NVIDIA A100"},
		NICCount:  8,
		PSUCount:  2,
		Storage: []store.StorageFact{
			{Key: "HDD", Count: 20},
			{Key: "NVMe", Count: 2},
			{Key: "SATA-SSD", Count: 1},
		},
	}
	cases := []struct {
		q    string
		want bool
	}{
		{"", true},
		{"cpu=Xeon&ram_min=128", true},
		{"cpu=EPYC", false},
		{"ram_min=512", false},
		{"gpu=any&cpu=6330&ram_min=128", true},
		{"gpu=A100&hdd_min=20&nvme_min=2", true},
		{"gpu=none", false},
		{"nic_min=8&psu_min=2&ssd_min=3", true},
		{"ssd_min=4", false},
		{"hdd_min=24", false},
	}
	for _, tc := range cases {
		f := parseHardwareListFilter(mustQuery(t, tc.q))
		if got := f.Match(hw); got != tc.want {
			t.Fatalf("%q: got %v want %v (filter=%+v)", tc.q, got, tc.want, f)
		}
	}
	if parseHardwareListFilter(mustQuery(t, "gpu=any")).Match(nil) {
		t.Fatal("nil hardware should fail closed")
	}
}

func mustQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	v, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
