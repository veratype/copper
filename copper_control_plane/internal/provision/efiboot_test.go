package provision

import (
	"strings"
	"testing"
)

// Fixture shaped like the hp-360-2 log: -c already prepended 0024, but the
// old bash parser took the first BootXXXX line (0000 = System Utilities).
const efiBeforeCreate = `BootCurrent: 000D
Timeout: 0 seconds
BootOrder: 0000,0023,0022,001B,0020,0019,0017,000F,0021,000A,000B,000C,000E,0006,0007,0008,0009,0001,0002,0003,0004,0005,0013,0014,0015,001C,001E,0011,0018,001A,001D,001F,0010,000D,0012,0016
Boot0000* System Utilities	FvVol(cdbb7b35-6833-4ed6-9ab2-57d2acddf6f0)/FvFile(1fd631e5-44e0-2f91-10ab-f88f3568ef30)
Boot000D* iLO Virtual USB 3 : iLO Virtual CD-ROM	PciRoot(0x0)/Pci(0x1c,0x4)/Pci(0x0,0x4)/USB(1,0)
Boot001B* -host--02	HD(15,GPT,6ec49856-0bd6-4fc8-891c-2ab8f712b077,0x2800,0x35000)/File(\EFI\ubuntu\shimx64.efi)
Boot0022* -myhost--2	HD(15,GPT,6ec49856-0bd6-4fc8-891c-2ab8f712b077,0x2800,0x35000)/File(\EFI\ubuntu\shimx64.efi)
Boot0023* hp-4-02	HD(15,GPT,6ec49856-0bd6-4fc8-891c-2ab8f712b077,0x2800,0x35000)/File(\EFI\ubuntu\shimx64.efi)
`

const efiAfterCreate = `BootCurrent: 000D
Timeout: 0 seconds
BootOrder: 0024,0000,0023,0022,001B,0020,0019,0017,000F,0021,000A,000B,000C,000E,0006,0007,0008,0009,0001,0002,0003,0004,0005,0013,0014,0015,001C,001E,0011,0018,001A,001D,001F,0010,000D,0012,0016
Boot0000* System Utilities	FvVol(cdbb7b35-6833-4ed6-9ab2-57d2acddf6f0)/FvFile(1fd631e5-44e0-2f91-10ab-f88f3568ef30)
Boot000D* iLO Virtual USB 3 : iLO Virtual CD-ROM	PciRoot(0x0)/Pci(0x1c,0x4)/Pci(0x0,0x4)/USB(1,0)
Boot001B* -host--02	HD(15,GPT,6ec49856-0bd6-4fc8-891c-2ab8f712b077,0x2800,0x35000)/File(\EFI\ubuntu\shimx64.efi)
Boot0022* -myhost--2	HD(15,GPT,6ec49856-0bd6-4fc8-891c-2ab8f712b077,0x2800,0x35000)/File(\EFI\ubuntu\shimx64.efi)
Boot0023* hp-4-02	HD(15,GPT,6ec49856-0bd6-4fc8-891c-2ab8f712b077,0x2800,0x35000)/File(\EFI\ubuntu\shimx64.efi)
Boot0024* hp-360-2	HD(15,GPT,6ec49856-0bd6-4fc8-891c-2ab8f712b077,0x2800,0x35000)/File(\EFI\ubuntu\shimx64.efi)
`

func TestNewBootIDFromDiff_HPLogRegression(t *testing.T) {
	got, err := newBootIDFromDiff(efiBeforeCreate, efiAfterCreate)
	if err != nil {
		t.Fatal(err)
	}
	if got != "0024" {
		t.Fatalf("got %q want 0024 (old bug returned 0000 System Utilities)", got)
	}
}

func TestResolveNewBootID_RejectsWrongLabel(t *testing.T) {
	// Simulate old bug: if someone passed 0000 as "new", entryMatchesLabel fails.
	id, err := resolveNewBootID(efiBeforeCreate, efiAfterCreate, "hp-360-2")
	if err != nil {
		t.Fatal(err)
	}
	if id != "0024" {
		t.Fatalf("got %q", id)
	}
	if entryMatchesLabel(efiAfterCreate, "0000", "hp-360-2") {
		t.Fatal("0000 must not match label")
	}
}

func TestNewBootIDFromDiff_NoChange(t *testing.T) {
	_, err := newBootIDFromDiff(efiAfterCreate, efiAfterCreate)
	if err == nil {
		t.Fatal("expected error when nothing was added")
	}
}

func TestBootIDForLabel_LastMatch(t *testing.T) {
	got := bootIDForLabel(efiAfterCreate, "hp-360-2")
	if got != "0024" {
		t.Fatalf("got %q want 0024", got)
	}
	got = bootIDForLabel(efiAfterCreate, "System Utilities")
	if got != "0000" {
		t.Fatalf("got %q want 0000", got)
	}
}

func TestStaleBootIDs_ByESPUUIDAndLabel(t *testing.T) {
	uuid := "6ec49856-0bd6-4fc8-891c-2ab8f712b077"
	got := staleBootIDs(efiAfterCreate, uuid, "hp-360-2")
	want := map[string]bool{"001B": true, "0022": true, "0023": true, "0024": true}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for _, id := range got {
		if !want[id] {
			t.Fatalf("unexpected %s in %v", id, got)
		}
	}
	// Label-only cleanup when UUID unknown.
	got = staleBootIDs(efiAfterCreate, "", "hp-360-2")
	if len(got) != 1 || got[0] != "0024" {
		t.Fatalf("label-only got %v", got)
	}
}

func TestBuildBootOrder_PutsNewFirst(t *testing.T) {
	order := parseBootOrderLine(efiAfterCreate)
	got := buildBootOrder("0024", order)
	if !bootOrderStartsWith(got, "0024") {
		t.Fatalf("order must start with 0024: %s", got)
	}
	if strings.Count(got, "0024") != 1 {
		t.Fatalf("0024 must appear once: %s", got)
	}
	bad := buildBootOrder("0000", order)
	if !bootOrderStartsWith(bad, "0000") {
		t.Fatalf("got %s", bad)
	}
}

func TestEFILoaderPath_SingleBackslash(t *testing.T) {
	cases := []struct{ in, want string }{
		{"EFI/ubuntu/shimx64.efi", `\EFI\ubuntu\shimx64.efi`},
		{`\EFI\ubuntu\shimx64.efi`, `\EFI\ubuntu\shimx64.efi`},
		{`\\EFI\ubuntu\shimx64.efi`, `\EFI\ubuntu\shimx64.efi`},
		{"/EFI/BOOT/BOOTX64.EFI", `\EFI\BOOT\BOOTX64.EFI`},
		{"", `\EFI\ubuntu\shimx64.efi`},
	}
	for _, tc := range cases {
		got := efiLoaderPath(tc.in)
		if got != tc.want {
			t.Fatalf("efiLoaderPath(%q)=%q want %q", tc.in, got, tc.want)
		}
		if strings.HasPrefix(got, `\\`) {
			t.Fatalf("double leading backslash: %q", got)
		}
	}
}

func TestSanitizeEFIBootLabel(t *testing.T) {
	if got := sanitizeEFIBootLabel("hp-360-2"); got != "hp-360-2" {
		t.Fatalf("got %q", got)
	}
	if got := sanitizeEFIBootLabel("my host/02"); got != "my host-02" {
		t.Fatalf("got %q", got)
	}
	if got := sanitizeEFIBootLabel(""); got != "Copper-OS" {
		t.Fatalf("got %q", got)
	}
	long := strings.Repeat("a", 50)
	if got := sanitizeEFIBootLabel(long); len(got) != 40 {
		t.Fatalf("len=%d", len(got))
	}
}

func TestExtractMarkedBlock(t *testing.T) {
	out := "noise\nEFIBOOT_BEFORE_BEGIN\nBootOrder: 1\nBoot0001* x\nEFIBOOT_BEFORE_END\nmore"
	got := extractMarkedBlock(out, "EFIBOOT_BEFORE_BEGIN", "EFIBOOT_BEFORE_END")
	if !strings.Contains(got, "BootOrder: 1") || strings.Contains(got, "EFIBOOT_BEFORE") {
		t.Fatalf("got %q", got)
	}
}

func TestRemoteEFIBootScripts_NoLocalParsing(t *testing.T) {
	probe := buildEFIBootProbeScript("/dev/sdg")
	for _, n := range []string{"EFIBOOT_PROBE_OK", "EFIBOOT_LISTING_BEGIN", "LOADER_REL=", "ESP_UUID="} {
		if !strings.Contains(probe, n) {
			t.Fatalf("probe missing %q", n)
		}
	}
	create := buildEFIBootCreateScript("/dev/sdg", "15", "hp-360-2", `\EFI\ubuntu\shimx64.efi`, []string{"001B", "0023"})
	for _, n := range []string{"EFIBOOT_CREATE_OK", "EFIBOOT_BEFORE_BEGIN", "EFIBOOT_AFTER_BEGIN", "removing Boot001B", "-c -d"} {
		if !strings.Contains(create, n) {
			t.Fatalf("create missing %q", n)
		}
	}
	// Remote create/order scripts must not reimplement Go decision logic.
	for _, bad := range []string{"comm -13", "BEFORE_IDS=", "NEW_BOOT=", "awk -v lab", "awk -v uuid"} {
		if strings.Contains(create, bad) {
			t.Fatalf("create must not contain %q", bad)
		}
	}
	order := buildEFIBootOrderScript("0024,0000,000D")
	if !strings.Contains(order, "EFIBOOT_ORDER_OK") || !strings.Contains(order, "0024,0000,000D") {
		t.Fatal("order script")
	}
	for _, bad := range []string{"comm -13", "awk "} {
		if strings.Contains(order, bad) {
			t.Fatalf("order must not contain %q", bad)
		}
	}
}
