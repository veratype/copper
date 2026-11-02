package storage

import (
	"strings"
	"testing"
)

func TestSuggestTargetsPrefersArrayNaming(t *testing.T) {
	sda, sdb := "/dev/sda", "/dev/sdb"
	c := Controller{
		Family: FamilySmartArray,
		Arrays: []Array{{
			Name:   "A",
			LD:     LogicalDrive{ID: 1, RAID: "1", Size: "838.33 GB", Disk: &sda},
			Drives: []string{"1I:1:1", "1I:1:2"},
		}},
		Drives: []Drive{
			{Addr: "1I:1:1", Bay: 1, Size: "900 GB", Type: "SAS HDD"},
			{Addr: "1I:1:2", Bay: 2, Size: "900 GB", Type: "SAS HDD"},
			{Addr: "1I:1:3", Bay: 3, Size: "900 GB", Type: "SAS HDD", Disk: &sdb},
		},
	}
	targets := SuggestTargets(c)
	if len(targets) != 2 {
		t.Fatalf("want 2 targets (1 array + 1 raw), got %d %#v", len(targets), targets)
	}
	if targets[0].Source != "array" || targets[0].Device != "/dev/sda" {
		t.Fatalf("first target=%+v", targets[0])
	}
	for _, want := range []string{"Array A", "LD 1", "RAID 1", "1I:1:1", "/dev/sda"} {
		if !strings.Contains(targets[0].Label, want) {
			t.Fatalf("array label missing %q: %q", want, targets[0].Label)
		}
	}
	if targets[1].Source != "drive" {
		t.Fatalf("second target=%+v", targets[1])
	}
	for _, want := range []string{"1I:1:3", "bay 3", "/dev/sdb"} {
		if !strings.Contains(targets[1].Label, want) {
			t.Fatalf("drive label missing %q: %q", want, targets[1].Label)
		}
	}
}

func TestSuggestTargetsIncludesArrayWithoutDev(t *testing.T) {
	c := Controller{
		Arrays: []Array{{Name: "A", LD: LogicalDrive{ID: 1, RAID: "1", Size: "838.33 GB"}, Drives: []string{"1I:1:1", "1I:1:2"}}},
	}
	targets := SuggestTargets(c)
	if len(targets) != 1 || targets[0].Device != "" {
		t.Fatalf("%+v", targets)
	}
	if !strings.Contains(targets[0].Label, "no /dev yet") {
		t.Fatalf("label=%q", targets[0].Label)
	}
}

func TestLayoutFingerprintStable(t *testing.T) {
	c := Controller{
		Family: FamilySmartArray,
		Slot:   0,
		Drives: []Drive{
			{Addr: "1I:1:2", Bay: 2, Size: "1.8TB", GB: 1800, Type: "SAS"},
			{Addr: "1I:1:1", Bay: 1, Size: "1.8TB", GB: 1800, Type: "SAS"},
		},
		Arrays: []Array{
			{Name: "A", LD: LogicalDrive{ID: 1, RAID: "1", Size: "1.8TB"}, Drives: []string{"1I:1:2", "1I:1:1"}},
		},
	}
	fp1 := LayoutFingerprint(c)
	// Shuffle order — fingerprint should sort.
	c.Drives[0], c.Drives[1] = c.Drives[1], c.Drives[0]
	fp2 := LayoutFingerprint(c)
	if fp1 != fp2 {
		t.Fatalf("fingerprint not stable:\n%s\n%s", fp1, fp2)
	}
	if !LayoutsMatch(Layout{Fingerprint: fp1}, Layout{Fingerprint: fp2}) {
		t.Fatal("LayoutsMatch failed")
	}
}
