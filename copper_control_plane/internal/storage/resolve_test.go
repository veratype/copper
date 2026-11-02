package storage

import (
	"strings"
	"testing"
)

func TestNormalizeDiskID(t *testing.T) {
	a := NormalizeDiskID("0x600508B1001CDA9BF177ECE22D4DB2CA")
	b := NormalizeDiskID("600508b1001cda9bf177ece22d4db2ca")
	if a == "" || a != b {
		t.Fatalf("got %q vs %q", a, b)
	}
}

func TestBindControllerDisksByWWN(t *testing.T) {
	// Reproduces the reported bug: stale LD Disk Name=/dev/sda while live RAID is /dev/sdg.
	sda, sdf, sdg := "/dev/sda", "/dev/sdf", "/dev/sdg"
	uid := "600508B1001CDA9BF177ECE22D4DB2CA"
	c := Controller{
		Family: FamilySmartArray,
		Arrays: []Array{{
			Name:   "A",
			LD:     LogicalDrive{ID: 1, RAID: "1", Size: "838.33 GB", Disk: &sda, WWN: uid},
			Drives: []string{"1I:1:1", "1I:1:2"},
		}},
		Drives: []Drive{
			{Addr: "1I:1:1", Bay: 1, Size: "900 GB", Type: "SAS HDD"},
			{Addr: "1I:1:2", Bay: 2, Size: "900 GB", Type: "SAS HDD"},
			{Addr: "1I:1:3", Bay: 3, Size: "900 GB", Type: "SAS HDD", Disk: &sdf},
			{Addr: "2I:1:5", Bay: 5, Size: "900 GB", Type: "SAS HDD", Disk: &sdg}, // wrong stale path
		},
	}
	disks := []BlockDisk{
		{Name: "sda", Path: "/dev/sda", Size: "838.4G", Type: "disk", Model: "EH000900JWHPP", WWN: "0x5000039d38116759"},
		{Name: "sdf", Path: "/dev/sdf", Size: "838.4G", Type: "disk", Model: "EH000900JWHPP", WWN: "0x5000039d2829d7bd"},
		{Name: "sdg", Path: "/dev/sdg", Size: "838.3G", Type: "disk", Model: "LOGICAL VOLUME", WWN: "0x600508b1001cda9bf177ece22d4db2ca"},
	}
	BindControllerDisks(&c, disks)

	if c.Arrays[0].LD.Disk == nil || *c.Arrays[0].LD.Disk != "/dev/sdg" {
		t.Fatalf("LD disk=%v want /dev/sdg", c.Arrays[0].LD.Disk)
	}
	// Array members must not keep OS paths.
	for _, d := range c.Drives {
		if d.Addr == "1I:1:1" || d.Addr == "1I:1:2" {
			if d.Disk != nil {
				t.Fatalf("array member %s still has disk=%v", d.Addr, d.Disk)
			}
		}
	}
	// Stale PD claim on the LD path must be cleared.
	for _, d := range c.Drives {
		if d.Addr == "2I:1:5" && d.Disk != nil && *d.Disk == "/dev/sdg" {
			t.Fatalf("PD %s still claims LD path", d.Addr)
		}
	}

	targets := SuggestTargets(c)
	if len(targets) < 1 || targets[0].Device != "/dev/sdg" || targets[0].DeviceID == "" {
		t.Fatalf("targets=%+v", targets)
	}
	if !strings.Contains(targets[0].Label, "/dev/sdg") {
		t.Fatalf("label=%q", targets[0].Label)
	}
}

func TestBindControllerDisksPrefersLogicalWithoutWWN(t *testing.T) {
	sda := "/dev/sda"
	c := Controller{
		Family: FamilySmartArray,
		Arrays: []Array{{
			Name:   "A",
			LD:     LogicalDrive{ID: 1, RAID: "1", Size: "838.33 GB", Disk: &sda},
			Drives: []string{"1I:1:1", "1I:1:2"},
		}},
		Drives: []Drive{
			{Addr: "1I:1:1", Bay: 1},
			{Addr: "1I:1:2", Bay: 2},
		},
	}
	disks := []BlockDisk{
		{Name: "sda", Path: "/dev/sda", Size: "838.4G", Type: "disk", Model: "EH000900JWHPP"},
		{Name: "sdg", Path: "/dev/sdg", Size: "838.3G", Type: "disk", Model: "LOGICAL VOLUME"},
	}
	BindControllerDisks(&c, disks)
	if c.Arrays[0].LD.Disk == nil || *c.Arrays[0].LD.Disk != "/dev/sdg" {
		t.Fatalf("LD disk=%v want /dev/sdg (logical volume)", c.Arrays[0].LD.Disk)
	}
}

// Reproduces multi-controller host: MegaRAID sliced VDs on one SAS HDD must not
// be painted onto unrelated NVMe namespaces from another controller / bus.
func TestBindControllerDisksRejectsNVMeForMegaRAID(t *testing.T) {
	nvme0, nvme1 := "/dev/nvme0n1", "/dev/nvme1n1"
	c := Controller{
		Family: FamilyMegaRAID,
		Arrays: []Array{
			{Name: "A", DG: "1", LD: LogicalDrive{ID: 236, RAID: "0", Size: "400.000 MB", Disk: &nvme1}, Drives: []string{"252:2"}},
			{Name: "B", DG: "1", LD: LogicalDrive{ID: 237, RAID: "0", Size: "200.000 MB", Disk: &nvme0}, Drives: []string{"252:2"}},
			{Name: "C", DG: "1", LD: LogicalDrive{ID: 238, RAID: "0", Size: "100.000 MB"}, Drives: []string{"252:2"}},
		},
		Drives: []Drive{{Addr: "252:2", Bay: 3, Type: "SAS HDD", Size: "18.189 TB", Status: "Onln", DG: "1"}},
	}
	disks := []BlockDisk{
		{Name: "sda", Path: "/dev/sda", Size: "18.2T", Type: "disk", Tran: "sas", WWN: "0x6003048023b5e303327503cb072146aa"},
		{Name: "sdb", Path: "/dev/sdb", Size: "100M", Type: "disk", Tran: "sas", WWN: "0x6003048023b5e30332750a9091f164bc"},
		{Name: "sdc", Path: "/dev/sdc", Size: "200M", Type: "disk", Tran: "sas", WWN: "0x6003048023b5e30332750a999f329faa"},
		{Name: "sdd", Path: "/dev/sdd", Size: "400M", Type: "disk", Tran: "sas", WWN: "0x6003048023b5e30332750a9fa789b07f"},
		{Name: "nvme1n1", Path: "/dev/nvme1n1", Size: "2.9T", Type: "disk", Tran: "nvme", WWN: "eui.364a3130543000360025384100000002"},
		{Name: "nvme0n1", Path: "/dev/nvme0n1", Size: "2.9T", Type: "disk", Tran: "nvme", WWN: "eui.364a313052c005580025384100000002"},
	}
	BindControllerDisks(&c, disks)

	want := map[int]string{236: "/dev/sdd", 237: "/dev/sdc", 238: "/dev/sdb"}
	for _, a := range c.Arrays {
		got := ""
		if a.LD.Disk != nil {
			got = *a.LD.Disk
		}
		if got != want[a.LD.ID] {
			t.Fatalf("LD %d disk=%q want %q", a.LD.ID, got, want[a.LD.ID])
		}
		if strings.Contains(got, "nvme") {
			t.Fatalf("LD %d still bound to NVMe %q", a.LD.ID, got)
		}
	}
}

func TestBindControllerDisksByVDWWN(t *testing.T) {
	c := Controller{
		Family: FamilyMegaRAID,
		Arrays: []Array{{
			Name: "A", DG: "1",
			LD:     LogicalDrive{ID: 236, RAID: "0", Size: "400.000 MB", WWN: "6003048023b5e30332750a9fa789b07f"},
			Drives: []string{"252:2"},
		}},
	}
	disks := []BlockDisk{
		{Name: "sdd", Path: "/dev/sdd", Size: "400M", Type: "disk", WWN: "0x6003048023b5e30332750a9fa789b07f"},
		{Name: "nvme0n1", Path: "/dev/nvme0n1", Size: "2.9T", Type: "disk", Tran: "nvme", WWN: "eui.364a313052c005580025384100000002"},
	}
	BindControllerDisks(&c, disks)
	if c.Arrays[0].LD.Disk == nil || *c.Arrays[0].LD.Disk != "/dev/sdd" {
		t.Fatalf("disk=%v", c.Arrays[0].LD.Disk)
	}
}

// MegaRAID: tiny sliced VDs with controller model as lsblk MODEL and no NAA yet
// must still bind by size (including legacy raw-byte SizeStr values).
func TestBindMegaRAIDVDBySizeWithControllerModel(t *testing.T) {
	c := Controller{
		Family: FamilyMegaRAID,
		Arrays: []Array{
			{Name: "A", DG: "1", LD: LogicalDrive{ID: 236, RAID: "0", Size: "400.000 MB", Status: "Optl"}, Drives: []string{"252:2"}},
			{Name: "B", DG: "1", LD: LogicalDrive{ID: 237, RAID: "0", Size: "200.000 MB", Status: "Optl"}, Drives: []string{"252:2"}},
			{Name: "C", DG: "1", LD: LogicalDrive{ID: 238, RAID: "0", Size: "100.000 MB", Status: "Optl"}, Drives: []string{"252:2"}},
		},
		Drives: []Drive{{Addr: "252:2", Bay: 3, Type: "SAS HDD", Size: "18.189 TB", Status: "Onln", DG: "1"}},
	}
	// Mimic ListBlockDisks after SizeStr fix ("400.0M") and the old raw-byte bug.
	disks := []BlockDisk{
		{Name: "sda", Path: "/dev/sda", Size: "18.2T", Type: "disk", Tran: "sas", Model: "ST18000NM004J"},
		{Name: "sdb", Path: "/dev/sdb", Size: "100.0M", Type: "disk", Tran: "sas", Model: "MR9560-16i"},
		{Name: "sdc", Path: "/dev/sdc", Size: "200.0M", Type: "disk", Tran: "sas", Model: "MR9560-16i"},
		{Name: "sdd", Path: "/dev/sdd", Size: "419430400", Type: "disk", Tran: "sas", Model: "MR9560-16i"}, // raw bytes ≈ 400 MiB
		{Name: "nvme0n1", Path: "/dev/nvme0n1", Size: "2.9T", Type: "disk", Tran: "nvme", Model: "Samsung SSD"},
	}
	BindControllerDisks(&c, disks)
	want := map[int]string{236: "/dev/sdd", 237: "/dev/sdc", 238: "/dev/sdb"}
	for _, a := range c.Arrays {
		got := ""
		if a.LD.Disk != nil {
			got = *a.LD.Disk
		}
		if got != want[a.LD.ID] {
			t.Fatalf("MegaRAID VD %d disk=%q want %q", a.LD.ID, got, want[a.LD.ID])
		}
	}
}

// Smart Array: LD without Unique Identifier must still bind by size to LOGICAL VOLUME.
func TestBindSmartArrayLDBySizeWithoutUID(t *testing.T) {
	c := Controller{
		Family: FamilySmartArray,
		Arrays: []Array{{
			Name:   "A",
			LD:     LogicalDrive{ID: 1, RAID: "0", Size: "400.000 MB", Status: "OK"},
			Drives: []string{"1I:1:1"},
		}},
		Drives: []Drive{{Addr: "1I:1:1", Bay: 1, Type: "SAS HDD", Size: "900 GB", Status: "OK"}},
	}
	disks := []BlockDisk{
		{Name: "sda", Path: "/dev/sda", Size: "838.4G", Type: "disk", Model: "EH000900JWHPP"},
		{Name: "sdb", Path: "/dev/sdb", Size: "419430400", Type: "disk", Model: "LOGICAL VOLUME"}, // raw bytes
		{Name: "nvme0n1", Path: "/dev/nvme0n1", Size: "2.9T", Type: "disk", Tran: "nvme"},
	}
	BindControllerDisks(&c, disks)
	if c.Arrays[0].LD.Disk == nil || *c.Arrays[0].LD.Disk != "/dev/sdb" {
		t.Fatalf("Smart Array LD disk=%v want /dev/sdb", c.Arrays[0].LD.Disk)
	}
}

func TestSizeGBParsesMB(t *testing.T) {
	if g := SizeGBLoose("400.000 MB"); g < 0.3 || g > 0.5 {
		t.Fatalf("400 MB → %v GB (old bug treated it as 400 GB)", g)
	}
	if g := SizeGBLoose("200M"); g < 0.15 || g > 0.25 {
		t.Fatalf("200M → %v", g)
	}
}

func TestSizeBytesApproxRawAndHuman(t *testing.T) {
	want := uint64(400 * 1e6) // decimal MB from controller strings
	cases := []struct {
		in   string
		lo   uint64
		hi   uint64
	}{
		{"400.000 MB", want - 20e6, want + 20e6},
		{"400.0M", uint64(400*1024*1024) - 20e6, uint64(400*1024*1024) + 20e6},
		{"419430400", 419430400, 419430400}, // raw bytes must not be read as GB
	}
	for _, tc := range cases {
		got := sizeBytesApprox(tc.in)
		if got < tc.lo || got > tc.hi {
			t.Fatalf("%q → %d want [%d,%d]", tc.in, got, tc.lo, tc.hi)
		}
	}
	// Regression: raw bytes must be compatible with controller "400.000 MB".
	if !sizesCompatible(sizeBytesApprox("400.000 MB"), sizeBytesApprox("419430400")) {
		t.Fatal("400.000 MB should match raw 419430400-byte lsblk size")
	}
}

func TestLsblkSizeStrHumanForSubGiB(t *testing.T) {
	n := lsblkNode{Size: "419430400"}
	s := n.SizeStr()
	if !strings.HasSuffix(strings.ToUpper(s), "M") && !strings.Contains(strings.ToUpper(s), "M") {
		t.Fatalf("SizeStr=%q want human MiB form, not raw bytes", s)
	}
	if !sizesCompatible(sizeBytesApprox("400.000 MB"), sizeBytesApprox(s)) {
		t.Fatalf("SizeStr %q not compatible with 400.000 MB", s)
	}
}

func TestResolveInstallPathAndKey(t *testing.T) {
	lay := Layout{
		Targets: []InstallTarget{
			{Device: "/dev/sda", DeviceID: "600508B1001CDA9BF177ECE22D4DB2CA", Key: "array:A:1", Source: "array"},
		},
	}
	t0, ok := FindTargetByKey(lay, "array:A:1")
	if !ok {
		t.Fatal("missing key")
	}
	disks := []BlockDisk{
		{Name: "sdg", Path: "/dev/sdg", WWN: "0x600508b1001cda9bf177ece22d4db2ca"},
	}
	if path := ResolveInstallPath(disks, t0); path != "/dev/sdg" {
		t.Fatalf("path=%q", path)
	}
}
