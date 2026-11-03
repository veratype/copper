package storage

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIsESPPart(t *testing.T) {
	if !isESPPart("C12A7328-F81F-11D2-BA4B-00A0C93EC93B") {
		t.Fatal("expected GPT ESP GUID")
	}
	if !isESPPart("0xEF") {
		t.Fatal("expected DOS EFI type")
	}
	if isESPPart("0fc63daf-8483-4772-8e79-3d69d8477de4") {
		t.Fatal("linux filesystem GUID must not be ESP")
	}
}

func TestSysfsListingIncludesMDAndRaidMemberNVMe(t *testing.T) {
	// Mirrors the airgapped host: nvme3n1/nvme5n1 hold md127 members; md127 itself
	// is a top-level lsblk row. Older scans only kept TYPE=disk and dropped these.
	raw := "" +
		"DISK\tnvme0n1\t3200631791616\tDISK0\t\tnvme\tdisk\n" +
		"DISK\tnvme3n1\t3200631791616\tDISK3\t\tnvme\tdisk\n" +
		"PART\tnvme3n1\tnvme3n1p3\t3198530641920\tlinux_raid_member\t\t\t\t\t\tpart\n" +
		"DISK\tnvme5n1\t3200631791616\tDISK5\t\tnvme\tdisk\n" +
		"PART\tnvme5n1\tnvme5n1p3\t3198530641920\tlinux_raid_member\t\t\t\t\t\tpart\n" +
		"DISK\tmd127\t3198530641920\t\t\t\traid1\n" +
		"PART\tmd127\tmd127p1\t3198530641920\text4\t\t\t\t/\t\tpart\n"
	disks, err := parseHostBlockListing(raw)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]DiskParts{}
	for _, d := range disks {
		byName[d.Name] = d
	}
	for _, want := range []string{"nvme0n1", "nvme3n1", "nvme5n1", "md127"} {
		if _, ok := byName[want]; !ok {
			t.Fatalf("missing %s in %+v", want, byName)
		}
	}
	if len(byName["nvme3n1"].Partitions) != 1 || byName["nvme3n1"].Partitions[0].Name != "nvme3n1p3" {
		t.Fatalf("nvme3n1 parts: %+v", byName["nvme3n1"].Partitions)
	}
	if len(byName["md127"].Partitions) != 1 || byName["md127"].Partitions[0].Path != "/dev/md127p1" {
		t.Fatalf("md127 parts: %+v", byName["md127"].Partitions)
	}
}

func TestSysfsListingAttachesPartitionsAndExtraDisks(t *testing.T) {
	// Matches the host-prep layout: many JBOD disks + a small sdk + NVMe with ESP.
	raw := "" +
		"DISK\tsda\t20000588955648\tST20000NM002D\t\tsas\tdisk\n" +
		"PART\tsda\tsda1\t2097152\t\t21686148-6449-6e6f-744e-656564454649\t\t11111111-1111-1111-1111-111111111111\t\t\tpart\n" +
		"PART\tsda\tsda2\t19998491787264\t\t0fc63daf-8483-4772-8e79-3d69d8477de4\t\t22222222-2222-2222-2222-222222222222\t\t\tpart\n" +
		"DISK\tsdk\t107374182400\t\t\t\tdisk\n" +
		"PART\tsdk\tsdk1\t104857600\tvfat\tc12a7328-f81f-11d2-ba4b-00a0c93ec93b\tEFI\tuuid-sdk1\t\t\tpart\n" +
		"PART\tsdk\tsdk5\t4294967296\t\t\t\t\t\t\tpart\n" +
		"DISK\tnvme1n1\t3200631791616\tDell Ent NVMe AGN MU AIC 3.2TB\t\tnvme\tdisk\n" +
		"PART\tnvme1n1\tnvme1n1p1\t2097152\t\t21686148-6449-6e6f-744e-656564454649\t\t\t\t\tpart\n" +
		"PART\tnvme1n1\tnvme1n1p2\t3198530641920\t\tc12a7328-f81f-11d2-ba4b-00a0c93ec93b\t\t44444444-4444-4444-4444-444444444444\t\t\tpart\n" +
		"DISK\tsr0\t401604608\t\t\t\trom\n"
	disks, err := parseHostBlockListing(raw)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]DiskParts{}
	for _, d := range disks {
		byName[d.Name] = d
	}
	if len(byName["sda"].Partitions) != 2 {
		t.Fatalf("sda: %+v", byName["sda"].Partitions)
	}
	sdk := byName["sdk"]
	if sdk.Path != "/dev/sdk" || len(sdk.Partitions) != 2 || !sdk.Partitions[0].ESP {
		t.Fatalf("sdk: %+v", sdk)
	}
	if len(byName["nvme1n1"].Partitions) != 2 || !byName["nvme1n1"].Partitions[1].ESP {
		t.Fatalf("nvme: %+v", byName["nvme1n1"])
	}
	if !byName["sr0"].Protected {
		t.Fatal("sr0 should be protected")
	}
}

func TestPairsAttachPartitionsOnPlainDisks(t *testing.T) {
	// Mirrors a live lsblk where HBA/JBOD/NVMe disks have partition tables.
	// lsblk -P prints those partitions as sibling rows, not only under RAID volumes.
	raw := `
NAME="sda" PKNAME="" PATH="/dev/sda" SIZE="20000588955648" TYPE="disk" TRAN="sas" MODEL="ST20000NM002D" FSTYPE="" PARTTYPE="" PARTLABEL="" MOUNTPOINT="" PARTUUID=""
NAME="sda1" PKNAME="sda" PATH="/dev/sda1" SIZE="2097152" TYPE="part" TRAN="" MODEL="" FSTYPE="" PARTTYPE="21686148-6449-6e6f-744e-656564454649" PARTLABEL="" MOUNTPOINT="" PARTUUID="11111111-1111-1111-1111-111111111111"
NAME="sda2" PKNAME="sda" PATH="/dev/sda2" SIZE="19998491787264" TYPE="part" TRAN="" MODEL="" FSTYPE="" PARTTYPE="0fc63daf-8483-4772-8e79-3d69d8477de4" PARTLABEL="" MOUNTPOINT="" PARTUUID="22222222-2222-2222-2222-222222222222"
NAME="sdb" PKNAME="" PATH="/dev/sdb" SIZE="20000588955648" TYPE="disk" MODEL="ST20000NM002D" PARTTYPE=""
NAME="sdb1" PKNAME="sdb" PATH="/dev/sdb1" SIZE="20000586858496" TYPE="part" PARTTYPE="0fc63daf-8483-4772-8e79-3d69d8477de4" PARTUUID="33333333-3333-3333-3333-333333333333"
NAME="nvme0n1" PKNAME="" PATH="/dev/nvme0n1" SIZE="3200631791616" TYPE="disk" TRAN="nvme" MODEL="Dell Ent NVMe AGN MU AIC 3.2TB"
NAME="nvme1n1" PKNAME="" PATH="/dev/nvme1n1" SIZE="3200631791616" TYPE="disk" TRAN="nvme" MODEL="Dell Ent NVMe AGN MU AIC 3.2TB"
NAME="nvme1n1p1" PKNAME="nvme1n1" PATH="/dev/nvme1n1p1" SIZE="2097152" TYPE="part" PARTTYPE="21686148-6449-6e6f-744e-656564454649"
NAME="nvme1n1p2" PKNAME="nvme1n1" PATH="/dev/nvme1n1p2" SIZE="3198530641920" TYPE="part" PARTTYPE="c12a7328-f81f-11d2-ba4b-00a0c93ec93b" PARTUUID="44444444-4444-4444-4444-444444444444"
NAME="loop0" PKNAME="" PATH="/dev/loop0" SIZE="1048576" TYPE="loop"
NAME="sr0" PKNAME="" PATH="/dev/sr0" SIZE="401604608" TYPE="rom"
sudo: ignored
`
	disks, err := parseHostBlockListing(raw)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]DiskParts{}
	for _, d := range disks {
		byName[d.Name] = d
	}
	if _, ok := byName["loop0"]; ok {
		t.Fatal("loop devices are not wipe targets")
	}
	if _, ok := byName["sr0"]; !ok {
		t.Fatal("optical devices should appear (marked protected by ListDiskPartitions)")
	}
	sda := byName["sda"]
	if len(sda.Partitions) != 2 || sda.Partitions[0].Path != "/dev/sda1" || sda.Partitions[1].Path != "/dev/sda2" {
		t.Fatalf("sda partitions: %+v", sda.Partitions)
	}
	if sda.Model != "ST20000NM002D" {
		t.Fatalf("model %q", sda.Model)
	}
	if len(byName["sdb"].Partitions) != 1 || byName["sdb"].Partitions[0].Name != "sdb1" {
		t.Fatalf("sdb: %+v", byName["sdb"].Partitions)
	}
	if len(byName["nvme0n1"].Partitions) != 0 {
		t.Fatalf("nvme0n1 should have no partitions, got %+v", byName["nvme0n1"].Partitions)
	}
	nv := byName["nvme1n1"]
	if nv.Tran != "nvme" || !strings.Contains(nv.Model, "NVMe") {
		t.Fatalf("nvme disk: %+v", nv)
	}
	if len(nv.Partitions) != 2 || nv.Partitions[1].Path != "/dev/nvme1n1p2" || !nv.Partitions[1].ESP {
		t.Fatalf("nvme1n1 partitions: %+v", nv.Partitions)
	}
}

func TestFlatJSONStillAttachesPartitions(t *testing.T) {
	// lsblk --json without a tree column lists partitions as top-level blockdevices.
	// Ignoring every non-disk row is what made the UI say "no partitions".
	raw := `{
  "blockdevices": [
    {"name":"sda","path":"/dev/sda","size":20000588955648,"type":"disk","model":"ST20000NM002D"},
    {"name":"sda1","pkname":"sda","path":"/dev/sda1","size":2097152,"type":"part","parttype":"21686148-6449-6e6f-744e-656564454649"},
    {"name":"sda2","pkname":"sda","path":"/dev/sda2","size":19998491787264,"type":"part"},
    {"name":"nvme1n1","path":"/dev/nvme1n1","size":3200631791616,"type":"disk","tran":"nvme"},
    {"name":"nvme1n1p1","pkname":"nvme1n1","path":"/dev/nvme1n1p1","size":2097152,"type":"part"},
    {"name":"nvme1n1p2","pkname":"nvme1n1","path":"/dev/nvme1n1p2","size":3198530641920,"type":"part","parttype":"c12a7328-f81f-11d2-ba4b-00a0c93ec93b"}
  ]
}`
	disks, err := parseHostBlockListing(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(disks) != 2 {
		t.Fatalf("disks: %+v", disks)
	}
	if len(disks[0].Partitions) != 2 || disks[0].Partitions[0].Path != "/dev/sda1" {
		t.Fatalf("sda: %+v", disks[0].Partitions)
	}
	if len(disks[1].Partitions) != 2 || !disks[1].Partitions[1].ESP {
		t.Fatalf("nvme: %+v", disks[1].Partitions)
	}
}

func TestPairsQuotedModelAndHexEscape(t *testing.T) {
	line := `NAME="sda" MODEL="Dell Ent NVMe AGN MU AIC 3.2TB" LABEL="a\x22b" TYPE="disk" SIZE="100"`
	kv := parsePairsLine(line)
	if kv["model"] != "Dell Ent NVMe AGN MU AIC 3.2TB" {
		t.Fatalf("model %q", kv["model"])
	}
	if kv["label"] != `a"b` {
		t.Fatalf("label %q", kv["label"])
	}
}

func TestGuessPartParent(t *testing.T) {
	cases := map[string]string{
		"sda1": "sda", "sdb1": "sdb", "sdk5": "sdk", "xvda2": "xvda",
		"nvme0n1p1": "nvme0n1", "nvme1n1p2": "nvme1n1", "mmcblk0p1": "mmcblk0",
		"md127p1": "md127", "md0p2": "md0",
		"sda": "", "nvme0n1": "", "nvme1n1": "", "md127": "", "sr0": "",
	}
	for name, want := range cases {
		if got := guessPartParent(name); got != want {
			t.Fatalf("%s → %q, want %q", name, got, want)
		}
	}
}

func TestCollectPartsFromLsblk(t *testing.T) {
	raw := `{
  "blockdevices": [
    {
      "name": "sda",
      "path": "/dev/sda",
      "size": "1000000000000",
      "type": "disk",
      "model": "DISK",
      "children": [
        {
          "name": "sda1",
          "path": "/dev/sda1",
          "size": "536870912",
          "type": "part",
          "fstype": "vfat",
          "parttype": "c12a7328-f81f-11d2-ba4b-00a0c93ec93b",
          "partuuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
          "partlabel": "EFI"
        },
        {
          "name": "sda2",
          "path": "/dev/sda2",
          "size": "500000000000",
          "type": "part",
          "fstype": "ext4",
          "parttype": "0fc63daf-8483-4772-8e79-3d69d8477de4",
          "partuuid": "11111111-2222-3333-4444-555555555555"
        }
      ]
    }
  ]
}`
	var tree struct {
		Blockdevices []wipeLsblkNode `json:"blockdevices"`
	}
	if err := json.Unmarshal([]byte(raw), &tree); err != nil {
		t.Fatal(err)
	}
	parts := collectParts(tree.Blockdevices[0].Children)
	if len(parts) != 2 {
		t.Fatalf("got %d parts", len(parts))
	}
	if !parts[0].ESP {
		t.Fatal("sda1 should be ESP")
	}
	if parts[1].ESP {
		t.Fatal("sda2 should not be ESP")
	}
	disks, err := parseHostBlockListing(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(disks) != 1 || len(disks[0].Partitions) != 2 || !disks[0].Partitions[0].ESP {
		t.Fatalf("nested json disks: %+v", disks)
	}
}

func TestSameBlockDev(t *testing.T) {
	if !sameBlockDev("/dev/sda", "sda") || !sameBlockDev("/dev/nvme0n1", "/dev/nvme0n1") {
		t.Fatal("expected match")
	}
	if sameBlockDev("/dev/sda", "/dev/sdb") {
		t.Fatal("expected mismatch")
	}
}

func TestBuildWipeScriptToleratesEmptyEFIBootmgr(t *testing.T) {
	s := buildWipeScript()
	if !strings.Contains(s, "grep -E '^Boot[0-9A-Fa-f]{4}' || true") {
		t.Fatal("efibootmgr Boot#### grep must tolerate no matches under pipefail")
	}
	if !strings.Contains(s, "EFI vars unavailable") {
		t.Fatal("expected empty efibootmgr -v to skip NVRAM cleanup instead of aborting")
	}
	if !strings.Contains(s, "WIPE_OK") {
		t.Fatal("expected WIPE_OK marker")
	}
}

func TestExtractJSONObject(t *testing.T) {
	got := extractJSONObject("sudo: ok\n{\"blockdevices\":[]}\n")
	if got != `{"blockdevices":[]}` {
		t.Fatalf("got %q", got)
	}
	// Nested children: must take the outer object, not the last '{'.
	nested := "noise\n{\n \"blockdevices\": [\n  {\n   \"name\": \"sda\",\n   \"type\": \"disk\",\n   \"children\": [\n    {\n     \"name\": \"sda1\",\n     \"type\": \"part\"\n    }\n   ]\n  }\n ]\n}\ntrailing"
	got = extractJSONObject(nested)
	var tree struct {
		Blockdevices []wipeLsblkNode `json:"blockdevices"`
	}
	if err := json.Unmarshal([]byte(got), &tree); err != nil {
		t.Fatalf("unmarshal extracted: %v\nblob=%q", err, got)
	}
	if len(tree.Blockdevices) != 1 || tree.Blockdevices[0].Name != "sda" {
		t.Fatalf("unexpected tree: %+v", tree)
	}
	if len(tree.Blockdevices[0].Children) != 1 || tree.Blockdevices[0].Children[0].Name != "sda1" {
		t.Fatalf("missing child: %+v", tree.Blockdevices[0])
	}
}
