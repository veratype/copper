package license

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFactsIDStable(t *testing.T) {
	f := Facts{
		Schema:        FingerprintSchema,
		MachineID:     "AaBbCcDd",
		ProductUUID:   "11111111-2222-3333-4444-555555555555",
		BoardSerial:   "Board-1",
		ProductSerial: "Prod-1",
		SysVendor:     "Vendor",
		ProductName:   "Product",
		CPUModel:      "Test CPU",
		CPUSockets:    2,
		MACs:          []string{"aa:bb:cc:dd:ee:ff", "11:22:33:44:55:66"},
	}
	a := f.ID()
	// Shuffle MAC order — canonical form sorts them.
	f.MACs = []string{"11:22:33:44:55:66", "aa:bb:cc:dd:ee:ff"}
	f.MachineID = "aabbccdd"
	b := f.ID()
	if a != b {
		t.Fatalf("fingerprint not stable:\n%s\n%s", a, b)
	}
	if !strings.HasPrefix(a, "fp_v1:") || len(a) != len("fp_v1:")+64 {
		t.Fatalf("bad fingerprint shape: %q", a)
	}
}

func TestNormalizeDMIDropsPlaceholders(t *testing.T) {
	if normalizeDMI("To Be Filled By O.E.M.") != "" {
		t.Fatal("expected empty")
	}
	if normalizeDMI("REAL-SERIAL") != "REAL-SERIAL" {
		t.Fatal("expected keep")
	}
}

func TestCollectFromTree(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "etc/machine-id"), "abc123\n")
	dmi := filepath.Join(root, "sys/class/dmi/id")
	mustWrite(t, filepath.Join(dmi, "product_uuid"), "UUID-1")
	mustWrite(t, filepath.Join(dmi, "board_serial"), "BOARD")
	mustWrite(t, filepath.Join(dmi, "product_serial"), "None")
	mustWrite(t, filepath.Join(dmi, "sys_vendor"), "Acme")
	mustWrite(t, filepath.Join(dmi, "product_name"), "Box")

	net := filepath.Join(root, "sys/class/net")
	mustWrite(t, filepath.Join(net, "lo/address"), "00:00:00:00:00:00")
	phys := filepath.Join(net, "eth0")
	mustWrite(t, filepath.Join(phys, "address"), "AA:BB:CC:DD:EE:FF")
	if err := os.MkdirAll(filepath.Join(phys, "device"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Virtual NIC without device/ — ignored.
	mustWrite(t, filepath.Join(net, "docker0/address"), "02:42:00:00:00:01")

	cpu := filepath.Join(root, "cpuinfo")
	mustWrite(t, cpu, `
processor	: 0
vendor_id	: GenuineIntel
cpu family	: 6
model name	: Test CPU @ 2.00GHz
physical id	: 0

processor	: 1
model name	: Test CPU @ 2.00GHz
physical id	: 0
`)

	f := collectFrom(root, cpu)
	if f.MachineID != "abc123" {
		t.Fatalf("machine_id=%q", f.MachineID)
	}
	if f.ProductSerial != "" {
		t.Fatalf("placeholder serial should drop, got %q", f.ProductSerial)
	}
	if f.CPUModel != "Test CPU @ 2.00GHz" || f.CPUSockets != 1 {
		t.Fatalf("cpu=%q sockets=%d", f.CPUModel, f.CPUSockets)
	}
	if len(f.MACs) != 1 || f.MACs[0] != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("macs=%v", f.MACs)
	}
	id := f.ID()
	if !strings.HasPrefix(id, "fp_v1:") {
		t.Fatalf("id=%q", id)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
