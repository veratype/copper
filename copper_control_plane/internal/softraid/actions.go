package softraid

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"

	"copper_control_plane/internal/storage"
)

// Supported RAID levels for create (mdadm --level=).
var SupportedLevels = []string{"0", "1", "4", "5", "6", "10", "linear"}

// LevelRule documents minimum device counts for the Soft RAID UI.
type LevelRule struct {
	Min  int  `json:"min"`
	Even bool `json:"even"` // RAID10 typically wants an even count
}

// LevelRules maps mdadm --level= values to device constraints.
var LevelRules = map[string]LevelRule{
	"0":      {Min: 2},
	"1":      {Min: 2},
	"4":      {Min: 3},
	"5":      {Min: 3},
	"6":      {Min: 4},
	"10":     {Min: 4, Even: true},
	"linear": {Min: 2},
}

// ExecPreview validates an action and returns the remote shell command without running it.
func ExecPreview(a Action) (string, error) {
	a.Op = strings.ToLower(strings.TrimSpace(a.Op))
	return buildCommand(a)
}

// Exec runs an allowlisted Soft RAID action on the live host.
func Exec(ctx context.Context, h storage.Host, a Action) (string, error) {
	cmd, err := ExecPreview(a)
	if err != nil {
		return "", err
	}
	out, err := storage.RunSSH(ctx, h.IP, h.Creds, storage.SudoBashLC(cmd))
	return out, err
}

func buildCommand(a Action) (string, error) {
	switch a.Op {
	case "create":
		return buildCreate(a)
	case "stop":
		dev, err := mdPath(a.Name)
		if err != nil {
			return "", err
		}
		return "mdadm --stop " + storage.ShellQuote(dev), nil
	case "remove":
		// Stop if active, then mdadm --remove (clears md device node when inactive).
		dev, err := mdPath(a.Name)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf(
			"mdadm --stop %s 2>/dev/null || true; mdadm --remove %s 2>/dev/null || true; echo REMOVE_DONE",
			storage.ShellQuote(dev), storage.ShellQuote(dev),
		), nil
	case "fail":
		arr, err := mdPath(a.Name)
		if err != nil {
			return "", err
		}
		disk, err := diskPath(a.Device)
		if err != nil {
			return "", err
		}
		return "mdadm " + storage.ShellQuote(arr) + " --fail " + storage.ShellQuote(disk), nil
	case "remove-device":
		arr, err := mdPath(a.Name)
		if err != nil {
			return "", err
		}
		disk, err := diskPath(a.Device)
		if err != nil {
			return "", err
		}
		return "mdadm " + storage.ShellQuote(arr) + " --remove " + storage.ShellQuote(disk), nil
	case "add":
		arr, err := mdPath(a.Name)
		if err != nil {
			return "", err
		}
		disk, err := diskPath(a.Device)
		if err != nil {
			return "", err
		}
		return "mdadm " + storage.ShellQuote(arr) + " --add " + storage.ShellQuote(disk), nil
	case "add-spare":
		arr, err := mdPath(a.Name)
		if err != nil {
			return "", err
		}
		disk, err := diskPath(a.Device)
		if err != nil {
			return "", err
		}
		// mdadm --add-spare when supported; fall back to --add.
		return fmt.Sprintf(
			`if mdadm %s --add-spare %s 2>/tmp/copper-md-spare.err; then echo ADD_SPARE_OK; else mdadm %s --add %s; fi`,
			storage.ShellQuote(arr), storage.ShellQuote(disk),
			storage.ShellQuote(arr), storage.ShellQuote(disk),
		), nil
	case "assemble":
		dev, err := mdPath(a.Name)
		if err != nil {
			return "", err
		}
		if len(a.Devices) == 0 {
			return "", fmt.Errorf("assemble requires devices")
		}
		var b strings.Builder
		b.WriteString("mdadm --assemble ")
		if a.Force {
			b.WriteString("--force ")
		}
		b.WriteString(storage.ShellQuote(dev))
		for _, d := range a.Devices {
			p, err := diskPath(d)
			if err != nil {
				return "", err
			}
			b.WriteByte(' ')
			b.WriteString(storage.ShellQuote(p))
		}
		return b.String(), nil
	case "assemble-scan":
		// mdadm exits 1 when nothing is found — that is not a Copper failure.
		force := ""
		if a.Force {
			force = " --force"
		}
		return fmt.Sprintf(`
set +e
out=$(mdadm --assemble --scan%s 2>&1)
rc=$?
printf '%%s\n' "$out"
if echo "$out" | grep -qiE 'No arrays found|no arrays found'; then
  echo ASSEMBLE_SCAN_NONE
  exit 0
fi
exit $rc
`, force), nil
	case "zero-superblock":
		disk, err := diskPath(a.Device)
		if err != nil {
			return "", err
		}
		return "mdadm --zero-superblock " + storage.ShellQuote(disk), nil
	case "grow":
		arr, err := mdPath(a.Name)
		if err != nil {
			return "", err
		}
		if a.GrowRaidDevices < 1 {
			return "", fmt.Errorf("grow_raid_devices must be >= 1")
		}
		return fmt.Sprintf(
			"mdadm --grow %s --raid-devices=%d",
			storage.ShellQuote(arr), a.GrowRaidDevices,
		), nil
	default:
		return "", fmt.Errorf("unsupported softraid op %q", a.Op)
	}
}

func buildCreate(a Action) (string, error) {
	level := strings.TrimSpace(a.Level)
	if level == "" {
		return "", fmt.Errorf("level is required")
	}
	// Accept raid1 / RAID1 / 1
	level = strings.TrimPrefix(strings.ToLower(level), "raid")
	if _, ok := LevelRules[level]; !ok {
		return "", fmt.Errorf("unsupported level %q (want one of %s)", a.Level, strings.Join(SupportedLevels, ","))
	}
	rule := LevelRules[level]
	devs := normalizeDeviceList(a.Devices)
	if len(devs) < rule.Min {
		return "", fmt.Errorf("RAID %s needs at least %d devices (got %d)", level, rule.Min, len(devs))
	}
	if rule.Even && len(devs)%2 != 0 {
		return "", fmt.Errorf("RAID %s needs an even number of devices", level)
	}
	name := strings.TrimSpace(a.Name)
	if name == "" {
		name = "md0"
	}
	md, err := mdPath(name)
	if err != nil {
		return "", err
	}
	meta := strings.TrimSpace(a.Metadata)
	if meta == "" {
		// 1.0 keeps superblock at end — friendlier for BIOS/UEFI boot on RAID1/10.
		meta = "1.0"
	}
	switch meta {
	case "0.90", "1.0", "1.1", "1.2":
	default:
		return "", fmt.Errorf("unsupported metadata %q", meta)
	}
	raidDevs := a.RaidDevices
	if raidDevs <= 0 {
		raidDevs = len(devs)
	}
	if raidDevs > len(devs)+len(a.SpareDevices) {
		return "", fmt.Errorf("raid_devices (%d) exceeds member+spare count", raidDevs)
	}
	if len(a.SpareDevices) > 0 {
		return buildCreateWithSpares(a, level, meta, md, raidDevs, devs)
	}

	var b strings.Builder
	b.WriteString("set -e; ")
	// Clear leftover signatures that block --create.
	for _, d := range devs {
		p, err := diskPath(d)
		if err != nil {
			return "", err
		}
		b.WriteString("mdadm --zero-superblock ")
		b.WriteString(storage.ShellQuote(p))
		b.WriteString(" 2>/dev/null || true; ")
		b.WriteString("wipefs -a ")
		b.WriteString(storage.ShellQuote(p))
		b.WriteString(" 2>/dev/null || true; ")
	}
	b.WriteString("mdadm --create ")
	b.WriteString(storage.ShellQuote(md))
	b.WriteString(" --level=")
	b.WriteString(level)
	b.WriteString(" --raid-devices=")
	b.WriteString(strconv.Itoa(raidDevs))
	b.WriteString(" --metadata=")
	b.WriteString(meta)
	if layout := strings.TrimSpace(a.Layout); layout != "" {
		if !safeToken(layout) {
			return "", fmt.Errorf("invalid layout %q", layout)
		}
		b.WriteString(" --layout=")
		b.WriteString(layout)
	}
	// --run starts the array; --force for dirty disks. Do not pass --yes (not portable).
	b.WriteString(" --run")
	if a.Force {
		b.WriteString(" --force")
	}
	for _, d := range devs {
		p, _ := diskPath(d)
		b.WriteByte(' ')
		b.WriteString(storage.ShellQuote(p))
	}
	b.WriteString("; echo CREATE_OK path=")
	b.WriteString(md)
	return b.String(), nil
}

func buildCreateWithSpares(a Action, level, meta, md string, raidDevs int, active []string) (string, error) {
	spares := normalizeDeviceList(a.SpareDevices)
	if raidDevs <= 0 {
		raidDevs = len(active)
	}
	var b strings.Builder
	b.WriteString("set -e; ")
	all := append(append([]string{}, active...), spares...)
	for _, d := range all {
		p, _ := diskPath(d)
		b.WriteString("mdadm --zero-superblock ")
		b.WriteString(storage.ShellQuote(p))
		b.WriteString(" 2>/dev/null || true; wipefs -a ")
		b.WriteString(storage.ShellQuote(p))
		b.WriteString(" 2>/dev/null || true; ")
	}
	b.WriteString("mdadm --create ")
	b.WriteString(storage.ShellQuote(md))
	b.WriteString(" --level=")
	b.WriteString(level)
	b.WriteString(" --raid-devices=")
	b.WriteString(strconv.Itoa(raidDevs))
	b.WriteString(" --spare-devices=")
	b.WriteString(strconv.Itoa(len(spares)))
	b.WriteString(" --metadata=")
	b.WriteString(meta)
	if layout := strings.TrimSpace(a.Layout); layout != "" {
		if !safeToken(layout) {
			return "", fmt.Errorf("invalid layout %q", layout)
		}
		b.WriteString(" --layout=")
		b.WriteString(layout)
	}
	b.WriteString(" --run")
	if a.Force {
		b.WriteString(" --force")
	}
	for _, d := range active {
		p, _ := diskPath(d)
		b.WriteByte(' ')
		b.WriteString(storage.ShellQuote(p))
	}
	for _, d := range spares {
		p, _ := diskPath(d)
		b.WriteByte(' ')
		b.WriteString(storage.ShellQuote(p))
	}
	b.WriteString("; echo CREATE_OK path=")
	b.WriteString(md)
	return b.String(), nil
}

func normalizeDeviceList(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, d := range in {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		p, err := diskPath(d)
		if err != nil {
			continue
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

func mdPath(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("array name is required")
	}
	if strings.HasPrefix(name, "/dev/") {
		base := path.Base(name)
		if !safeMDName(base) && !strings.Contains(name, "/md/") {
			return "", fmt.Errorf("invalid md path %q", name)
		}
		return name, nil
	}
	if strings.HasPrefix(name, "md/") {
		return "/dev/" + name, nil
	}
	if !safeMDName(name) {
		return "", fmt.Errorf("invalid md name %q", name)
	}
	return "/dev/" + name, nil
}

func diskPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("device path is required")
	}
	if !strings.HasPrefix(p, "/dev/") {
		p = "/dev/" + p
	}
	base := path.Base(p)
	// Allow sdX, vdX, nvme, dm-, md (for nested — rare), by-id via full path.
	if strings.Contains(p, "/by-id/") || strings.Contains(p, "/by-path/") {
		if strings.ContainsAny(p, " \t\n;|&$`") {
			return "", fmt.Errorf("invalid device path")
		}
		return p, nil
	}
	if !safeDiskName(base) {
		return "", fmt.Errorf("invalid device %q", p)
	}
	return p, nil
}

func safeMDName(name string) bool {
	// md0, md127, md_d0 style
	if name == "" {
		return false
	}
	for i, c := range name {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
			continue
		}
		if i > 0 && c == '/' {
			continue
		}
		return false
	}
	return strings.HasPrefix(strings.ToLower(name), "md")
}

func safeDiskName(name string) bool {
	if name == "" || strings.ContainsAny(name, " \t\n;|&$`") {
		return false
	}
	for _, c := range name {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.' {
			continue
		}
		return false
	}
	return true
}

func safeToken(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}
