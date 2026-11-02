package softraid

import (
	"strings"
	"testing"
)

func TestBuildCreateRAID1(t *testing.T) {
	cmd, err := buildCommand(Action{
		Op:       "create",
		Name:     "md0",
		Level:    "1",
		Devices:  []string{"/dev/sdb", "/dev/sdc"},
		Metadata: "1.0",
		Force:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"mdadm --create '/dev/md0'",
		"--level=1",
		"--raid-devices=2",
		"--metadata=1.0",
		"--force",
		"'/dev/sdb'",
		"'/dev/sdc'",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("missing %q in:\n%s", want, cmd)
		}
	}
	if strings.Contains(cmd, "--yes") {
		t.Fatalf("--yes is not portable; got:\n%s", cmd)
	}
}

func TestBuildCreateRAID5MinDevices(t *testing.T) {
	_, err := buildCommand(Action{
		Op:      "create",
		Name:    "md0",
		Level:   "5",
		Devices: []string{"/dev/sdb", "/dev/sdc"},
	})
	if err == nil {
		t.Fatal("expected error for RAID5 with 2 disks")
	}
}

func TestBuildCreateRAID10Even(t *testing.T) {
	_, err := buildCommand(Action{
		Op:      "create",
		Name:    "md0",
		Level:   "10",
		Devices: []string{"/dev/sdb", "/dev/sdc", "/dev/sdd"},
	})
	if err == nil {
		t.Fatal("expected error for odd RAID10")
	}
	cmd, err := buildCommand(Action{
		Op:      "create",
		Name:    "md0",
		Level:   "10",
		Devices: []string{"/dev/sdb", "/dev/sdc", "/dev/sdd", "/dev/sde"},
		Layout:  "n2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd, "--layout=n2") {
		t.Fatalf("layout missing: %s", cmd)
	}
}

func TestBuildLifecycleOps(t *testing.T) {
	cases := []struct {
		a    Action
		want string
	}{
		{Action{Op: "stop", Name: "md0"}, "mdadm --stop '/dev/md0'"},
		{Action{Op: "fail", Name: "md0", Device: "/dev/sdb"}, "mdadm '/dev/md0' --fail '/dev/sdb'"},
		{Action{Op: "remove-device", Name: "md0", Device: "sdb"}, "mdadm '/dev/md0' --remove '/dev/sdb'"},
		{Action{Op: "add", Name: "md0", Device: "/dev/sdd"}, "--add '/dev/sdd'"},
		{Action{Op: "assemble-scan", Force: true}, "mdadm --assemble --scan --force"},
		{Action{Op: "assemble-scan", Force: true}, "ASSEMBLE_SCAN_NONE"},
		{Action{Op: "zero-superblock", Device: "/dev/sdb"}, "mdadm --zero-superblock '/dev/sdb'"},
		{Action{Op: "grow", Name: "md0", GrowRaidDevices: 3}, "mdadm --grow '/dev/md0' --raid-devices=3"},
	}
	for _, tc := range cases {
		cmd, err := buildCommand(tc.a)
		if err != nil {
			t.Fatalf("%s: %v", tc.a.Op, err)
		}
		if !strings.Contains(cmd, tc.want) {
			t.Fatalf("%s: want substring %q in %q", tc.a.Op, tc.want, cmd)
		}
	}
}

func TestRejectBadPaths(t *testing.T) {
	_, err := buildCommand(Action{Op: "fail", Name: "md0", Device: "/dev/sda;rm -rf /"})
	if err == nil {
		t.Fatal("expected reject")
	}
}
