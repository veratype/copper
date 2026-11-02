package softraid

import (
	"testing"

	"copper_control_plane/internal/storage"
)

func TestBatchFingerprintIgnoresHostIDs(t *testing.T) {
	a := Topology{
		Disks: []Disk{
			{Path: "/dev/sda", Size: "1.8T", State: "free", Tran: "sas", WWN: "wwn-a"},
			{Path: "/dev/sdb", Size: "1.8T", State: "free", Tran: "sas", WWN: "wwn-b"},
		},
	}
	b := Topology{
		Disks: []Disk{
			{Path: "/dev/sdc", Size: "1.8T", State: "free", Tran: "sas", WWN: "wwn-c"},
			{Path: "/dev/sdd", Size: "1.8T", State: "free", Tran: "sas", WWN: "wwn-d"},
		},
	}
	if BatchFingerprint(a) != BatchFingerprint(b) {
		t.Fatalf("expected match\na=%s\nb=%s", BatchFingerprint(a), BatchFingerprint(b))
	}
	if Fingerprint(a) == Fingerprint(b) {
		t.Fatal("per-host Fingerprint should differ when WWNs differ")
	}
}

func TestBatchFingerprintDiffersOnSize(t *testing.T) {
	a := Topology{Disks: []Disk{{Size: "1.8T", State: "free"}, {Size: "1.8T", State: "free"}}}
	b := Topology{Disks: []Disk{{Size: "1.8T", State: "free"}, {Size: "3.6T", State: "free"}}}
	if BatchFingerprint(a) == BatchFingerprint(b) {
		t.Fatal("different sizes should not match")
	}
}

func TestLayoutFromTopologyKeepsPerHostAndBatch(t *testing.T) {
	a := Topology{
		OK: true,
		Disks: []Disk{
			{Path: "/dev/sda", Size: "838.4G", State: "free", WWN: "w1"},
			{Path: "/dev/sdb", Size: "838.4G", State: "member", WWN: "w2"},
		},
		Arrays: []Array{{Name: "md0", Path: "/dev/md0", Level: "raid1", UUID: "aaa", RaidDevices: 2, Members: []Member{{Device: "/dev/sdb", Role: "active"}}}},
	}
	b := Topology{
		OK: true,
		Disks: []Disk{
			{Path: "/dev/sdc", Size: "838.4G", State: "free", WWN: "w3"},
			{Path: "/dev/sdd", Size: "838.4G", State: "member", WWN: "w4"},
		},
		Arrays: []Array{{Name: "md0", Path: "/dev/md0", Level: "raid1", UUID: "bbb", RaidDevices: 2, Members: []Member{{Device: "/dev/sdd", Role: "active"}}}},
	}
	la := LayoutFromTopology(a, "10.0.0.1")
	lb := LayoutFromTopology(b, "10.0.0.2")
	if la.Fingerprint == lb.Fingerprint {
		t.Fatal("per-host Fingerprint must differ when UUID/WWN differ")
	}
	if la.BatchFingerprint == "" || la.BatchFingerprint != lb.BatchFingerprint {
		t.Fatalf("BatchFingerprint should match\na=%s\nb=%s", la.BatchFingerprint, lb.BatchFingerprint)
	}
	if !storage.LayoutsMatch(la, lb) {
		t.Fatal("LayoutsMatch should use BatchFingerprint for soft_raid")
	}
	if la.Targets[0].Key != "md:md0" || lb.Targets[0].Key != "md:md0" {
		t.Fatalf("want md:md0 keys, got %q / %q", la.Targets[0].Key, lb.Targets[0].Key)
	}
}
