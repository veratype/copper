package license

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// FingerprintSchema identifies the canonical fact set hashed into an ID.
const FingerprintSchema = "fp_v1"

// Facts are the stable machine attributes that form a fingerprint.
// Empty fields are omitted from the hash input (missing sysfs is fine).
type Facts struct {
	Schema        string   `json:"schema"`
	MachineID     string   `json:"machine_id,omitempty"`
	ProductUUID   string   `json:"product_uuid,omitempty"`
	BoardSerial   string   `json:"board_serial,omitempty"`
	ProductSerial string   `json:"product_serial,omitempty"`
	SysVendor     string   `json:"sys_vendor,omitempty"`
	ProductName   string   `json:"product_name,omitempty"`
	CPUModel      string   `json:"cpu_model,omitempty"`
	CPUSockets    int      `json:"cpu_sockets,omitempty"`
	MACs          []string `json:"macs,omitempty"`
}

// Collect reads local OS/hardware identity from sysfs and /proc.
func Collect() Facts {
	return collectFrom("/", "/proc/cpuinfo")
}

func collectFrom(root, cpuinfoPath string) Facts {
	dmi := filepath.Join(root, "sys/class/dmi/id")
	f := Facts{
		Schema:        FingerprintSchema,
		MachineID:     readTrim(filepath.Join(root, "etc/machine-id")),
		ProductUUID:   normalizeDMI(readTrim(filepath.Join(dmi, "product_uuid"))),
		BoardSerial:   normalizeDMI(readTrim(filepath.Join(dmi, "board_serial"))),
		ProductSerial: normalizeDMI(readTrim(filepath.Join(dmi, "product_serial"))),
		SysVendor:     normalizeDMI(readTrim(filepath.Join(dmi, "sys_vendor"))),
		ProductName:   normalizeDMI(readTrim(filepath.Join(dmi, "product_name"))),
	}
	f.CPUModel, f.CPUSockets = readCPU(cpuinfoPath)
	f.MACs = physicalMACs(filepath.Join(root, "sys/class/net"))
	return f
}

// Fingerprint returns the canonical machine fingerprint (fp_v1:<hex>).
func Fingerprint() string {
	return Collect().ID()
}

// ID hashes Facts into a stable fingerprint string.
func (f Facts) ID() string {
	sum := sha256.Sum256([]byte(f.canonical()))
	return FingerprintSchema + ":" + hex.EncodeToString(sum[:])
}

// canonical builds a deterministic, line-oriented payload for hashing.
// Field order and normalization must stay stable across releases of fp_v1.
func (f Facts) canonical() string {
	var b strings.Builder
	writeKV := func(k, v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(v)
		b.WriteByte('\n')
	}
	writeKV("schema", FingerprintSchema)
	writeKV("machine_id", strings.ToLower(f.MachineID))
	writeKV("product_uuid", strings.ToLower(f.ProductUUID))
	writeKV("board_serial", f.BoardSerial)
	writeKV("product_serial", f.ProductSerial)
	writeKV("sys_vendor", f.SysVendor)
	writeKV("product_name", f.ProductName)
	writeKV("cpu_model", f.CPUModel)
	if f.CPUSockets > 0 {
		writeKV("cpu_sockets", strconv.Itoa(f.CPUSockets))
	}
	macs := append([]string(nil), f.MACs...)
	for i := range macs {
		macs[i] = strings.ToLower(strings.TrimSpace(macs[i]))
	}
	sort.Strings(macs)
	for _, m := range macs {
		if m == "" || m == "00:00:00:00:00:00" {
			continue
		}
		writeKV("mac", m)
	}
	return b.String()
}

func readTrim(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func normalizeDMI(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	low := strings.ToLower(s)
	switch low {
	case "none", "not specified", "not available", "default string",
		"to be filled by o.e.m.", "to be filled by o.e.m", "system serial number",
		"0123456789", "chassis serial number":
		return ""
	}
	return s
}

func readCPU(path string) (model string, sockets int) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", 0
	}
	seen := map[string]struct{}{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if model == "" {
			if v, ok := cpuField(line, "model name"); ok {
				model = v
			} else if v, ok := cpuField(line, "Hardware"); ok {
				// ARM boards often use Hardware instead of model name.
				model = v
			}
		}
		if v, ok := cpuField(line, "physical id"); ok {
			seen[v] = struct{}{}
		}
	}
	sockets = len(seen)
	if sockets == 0 && model != "" {
		sockets = 1
	}
	return model, sockets
}

func cpuField(line, key string) (string, bool) {
	if !strings.HasPrefix(line, key) {
		return "", false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(line, key))
	if !strings.HasPrefix(rest, ":") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(rest, ":")), true
}

func physicalMACs(netRoot string) []string {
	ents, err := os.ReadDir(netRoot)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		name := e.Name()
		if name == "lo" {
			continue
		}
		dir := filepath.Join(netRoot, name)
		// Physical NICs expose a device symlink; bridges/veth/docker usually do not.
		if _, err := os.Stat(filepath.Join(dir, "device")); err != nil {
			continue
		}
		mac := strings.ToLower(readTrim(filepath.Join(dir, "address")))
		if mac == "" || mac == "00:00:00:00:00:00" {
			continue
		}
		out = append(out, mac)
	}
	sort.Strings(out)
	return uniqueStrings(out)
}

func uniqueStrings(in []string) []string {
	if len(in) == 0 {
		return in
	}
	out := in[:0]
	var prev string
	for i, s := range in {
		if i > 0 && s == prev {
			continue
		}
		out = append(out, s)
		prev = s
	}
	return out
}

// FormatFacts returns a human-readable dump of collected facts (for CLI).
func FormatFacts(f Facts) string {
	var b strings.Builder
	fmt.Fprintf(&b, "schema:         %s\n", f.Schema)
	fmt.Fprintf(&b, "machine_id:     %s\n", dash(f.MachineID))
	fmt.Fprintf(&b, "product_uuid:   %s\n", dash(f.ProductUUID))
	fmt.Fprintf(&b, "board_serial:   %s\n", dash(f.BoardSerial))
	fmt.Fprintf(&b, "product_serial: %s\n", dash(f.ProductSerial))
	fmt.Fprintf(&b, "sys_vendor:     %s\n", dash(f.SysVendor))
	fmt.Fprintf(&b, "product_name:   %s\n", dash(f.ProductName))
	fmt.Fprintf(&b, "cpu_model:      %s\n", dash(f.CPUModel))
	if f.CPUSockets > 0 {
		fmt.Fprintf(&b, "cpu_sockets:    %d\n", f.CPUSockets)
	} else {
		fmt.Fprintf(&b, "cpu_sockets:    -\n")
	}
	if len(f.MACs) == 0 {
		fmt.Fprintf(&b, "macs:           -\n")
	} else {
		fmt.Fprintf(&b, "macs:           %s\n", strings.Join(f.MACs, ", "))
	}
	fmt.Fprintf(&b, "fingerprint:    %s\n", f.ID())
	return b.String()
}

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
