package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// DiskPart is one partition (or partition-like child) under a disk.
type DiskPart struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Size       string `json:"size"`
	Type       string `json:"type"` // part | crypt | lvm | …
	Fstype     string `json:"fstype,omitempty"`
	PartType   string `json:"parttype,omitempty"` // GPT type GUID or DOS id
	Label      string `json:"label,omitempty"`
	PartLabel  string `json:"partlabel,omitempty"`
	Mountpoint string `json:"mountpoint,omitempty"`
	PartUUID   string `json:"partuuid,omitempty"`
	UUID       string `json:"uuid,omitempty"`
	ESP        bool   `json:"esp"`
}

// DiskParts is one whole disk plus its partitions from a live-OS scan.
type DiskParts struct {
	Name          string     `json:"name"`
	Path          string     `json:"path"`
	Size          string     `json:"size"`
	Model         string     `json:"model,omitempty"`
	Serial        string     `json:"serial,omitempty"`
	Tran          string     `json:"tran,omitempty"`
	Partitions    []DiskPart `json:"partitions"`
	Protected     bool       `json:"protected"`
	ProtectReason string     `json:"protect_reason,omitempty"`
}

// PartitionScan is the result of listing host disks with partitions.
type PartitionScan struct {
	Disks    []DiskParts `json:"disks"`
	LiveDisk string      `json:"live_disk,omitempty"` // /dev path of the live-OS disk (if known)
	DataIP   string      `json:"data_ip,omitempty"`
}

const gptESPType = "c12a7328-f81f-11d2-ba4b-00a0c93ec93b"

type wipeLsblkNode struct {
	Name       string          `json:"name"`
	PKName     string          `json:"pkname"`
	Path       string          `json:"path"`
	Size       json.Number     `json:"size"`
	Type       string          `json:"type"`
	Tran       string          `json:"tran"`
	Model      string          `json:"model"`
	Serial     string          `json:"serial"`
	Fstype     string          `json:"fstype"`
	PartType   string          `json:"parttype"`
	Label      string          `json:"label"`
	PartLabel  string          `json:"partlabel"`
	Mountpoint string          `json:"mountpoint"`
	PartUUID   string          `json:"partuuid"`
	UUID       string          `json:"uuid"`
	Children   []wipeLsblkNode `json:"children"`
}

func (n wipeLsblkNode) sizeStr() string {
	return formatByteCount(n.Size.String())
}

func formatByteCount(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "null" {
		return ""
	}
	if v, err := strconv.ParseInt(s, 10, 64); err == nil && v > 0 {
		return formatBytes(v)
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && f > 0 && f < 1e18 {
		return formatBytes(int64(f))
	}
	return s
}

func formatBytes(v int64) string {
	const (
		ki = 1024
		mi = 1024 * ki
		gi = 1024 * mi
	)
	switch {
	case v >= gi:
		return fmt.Sprintf("%.1fG", float64(v)/float64(gi))
	case v >= mi:
		return fmt.Sprintf("%.1fM", float64(v)/float64(mi))
	case v >= ki:
		return fmt.Sprintf("%.1fK", float64(v)/float64(ki))
	default:
		return strconv.FormatInt(v, 10)
	}
}

func isESPPart(pt string) bool {
	pt = strings.ToLower(strings.TrimSpace(pt))
	if pt == gptESPType {
		return true
	}
	// DOS/MBR EFI System Partition id
	if pt == "0xef" || pt == "ef" {
		return true
	}
	return false
}

// blkRow is one lsblk device (disk or partition), independent of tree nesting.
type blkRow struct {
	Name, PKName, Path, Size, Type, Tran, Model, Serial string
	Fstype, PartType, Label, PartLabel, Mountpoint      string
	PartUUID, UUID                                      string
}

func collectParts(nodes []wipeLsblkNode) []DiskPart {
	rows := flattenLsblk(nodes, "")
	var out []DiskPart
	for _, d := range assembleDiskParts(rows) {
		out = append(out, d.Partitions...)
	}
	if len(out) > 0 {
		return out
	}
	// Partition nodes with no whole-disk parent in this slice (unit tests, and
	// lsblk JSON that nests children under the disk we already peeled off).
	for _, r := range rows {
		if !isPartType(r.Type) {
			continue
		}
		path := strings.TrimSpace(r.Path)
		if path == "" {
			path = "/dev/" + strings.TrimSpace(r.Name)
		}
		pt := strings.TrimSpace(r.PartType)
		out = append(out, DiskPart{
			Name: r.Name, Path: path, Size: strings.TrimSpace(r.Size),
			Type:   strings.ToLower(strings.TrimSpace(r.Type)),
			Fstype: strings.TrimSpace(r.Fstype), PartType: pt,
			Label: strings.TrimSpace(r.Label), PartLabel: strings.TrimSpace(r.PartLabel),
			Mountpoint: strings.TrimSpace(r.Mountpoint),
			PartUUID:   strings.TrimSpace(r.PartUUID), UUID: strings.TrimSpace(r.UUID),
			ESP: isESPPart(pt),
		})
	}
	return out
}

func flattenLsblk(nodes []wipeLsblkNode, parent string) []blkRow {
	var out []blkRow
	for _, n := range nodes {
		pk := strings.TrimSpace(n.PKName)
		if pk == "" {
			pk = parent
		}
		out = append(out, blkRow{
			Name: n.Name, PKName: pk, Path: n.Path, Size: n.sizeStr(),
			Type: n.Type, Tran: n.Tran, Model: n.Model, Serial: n.Serial,
			Fstype: n.Fstype, PartType: n.PartType, Label: n.Label, PartLabel: n.PartLabel,
			Mountpoint: n.Mountpoint, PartUUID: n.PartUUID, UUID: n.UUID,
		})
		if len(n.Children) > 0 {
			out = append(out, flattenLsblk(n.Children, n.Name)...)
		}
	}
	return out
}

var (
	reNVMePart = regexp.MustCompile(`^(nvme\d+n\d+)p\d+$`)
	reMMCPart  = regexp.MustCompile(`^(mmcblk\d+)p\d+$`)
	reTailPart = regexp.MustCompile(`^([a-zA-Z]+)(\d+)$`)
)

// guessPartParent maps a kernel partition name to its whole-disk name.
// sda1 → sda, nvme1n1p2 → nvme1n1, mmcblk0p1 → mmcblk0. Disks themselves return "".
func guessPartParent(name string) string {
	name = strings.TrimPrefix(strings.TrimSpace(name), "/dev/")
	if name == "" {
		return ""
	}
	if m := reNVMePart.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	if m := reMMCPart.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	if m := reTailPart.FindStringSubmatch(name); m != nil {
		// nvme0n1 is a disk (digits are not a trailing partition index only).
		if strings.Contains(m[1], "nvme") || strings.Contains(m[1], "mmcblk") {
			return ""
		}
		return m[1]
	}
	return ""
}

func isPartType(typ string) bool {
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "part", "partition", "crypt", "lvm", "md":
		return true
	default:
		return false
	}
}

func assembleDiskParts(rows []blkRow) []DiskParts {
	isDisk := map[string]bool{}
	pkOf := map[string]string{}
	var disks []DiskParts
	diskAt := map[string]int{}
	for _, r := range rows {
		name := strings.TrimSpace(r.Name)
		if name == "" {
			continue
		}
		if pk := strings.TrimSpace(r.PKName); pk != "" && pk != name {
			pkOf[name] = pk
		}
		if !strings.EqualFold(strings.TrimSpace(r.Type), "disk") {
			continue
		}
		if _, ok := diskAt[name]; ok {
			continue
		}
		path := strings.TrimSpace(r.Path)
		if path == "" {
			path = "/dev/" + name
		}
		isDisk[name] = true
		diskAt[name] = len(disks)
		disks = append(disks, DiskParts{
			Name: name, Path: path, Size: formatByteCount(r.Size),
			Model: strings.TrimSpace(r.Model), Serial: strings.TrimSpace(r.Serial),
			Tran:       strings.TrimSpace(r.Tran),
			Partitions: []DiskPart{},
		})
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if !isPartType(r.Type) {
			continue
		}
		name := strings.TrimSpace(r.Name)
		parent := parentDiskName(name, r.PKName, isDisk, pkOf)
		if parent == "" {
			continue
		}
		path := strings.TrimSpace(r.Path)
		if path == "" {
			path = "/dev/" + name
		}
		key := parent + "\x00" + path
		if seen[key] {
			continue
		}
		seen[key] = true
		pt := strings.TrimSpace(r.PartType)
		disks[diskAt[parent]].Partitions = append(disks[diskAt[parent]].Partitions, DiskPart{
			Name: name, Path: path, Size: formatByteCount(r.Size),
			Type:       strings.ToLower(strings.TrimSpace(r.Type)),
			Fstype:     strings.TrimSpace(r.Fstype),
			PartType:   pt,
			Label:      strings.TrimSpace(r.Label),
			PartLabel:  strings.TrimSpace(r.PartLabel),
			Mountpoint: strings.TrimSpace(r.Mountpoint),
			PartUUID:   strings.TrimSpace(r.PartUUID),
			UUID:       strings.TrimSpace(r.UUID),
			ESP:        isESPPart(pt),
		})
	}
	if disks == nil {
		disks = []DiskParts{}
	}
	return disks
}

func parentDiskName(name, pk string, isDisk map[string]bool, pkOf map[string]string) string {
	seen := map[string]bool{}
	cur := strings.TrimSpace(pk)
	if cur == "" {
		cur = guessPartParent(name)
	}
	for cur != "" && !seen[cur] {
		seen[cur] = true
		if isDisk[cur] {
			return cur
		}
		next := strings.TrimSpace(pkOf[cur])
		if next == "" || next == cur {
			next = guessPartParent(cur)
		}
		if next == "" || next == cur {
			break
		}
		cur = next
	}
	if g := guessPartParent(name); isDisk[g] {
		return g
	}
	return ""
}

// ListDiskPartitions returns whole disks with their partitions.
// Every OS-visible disk is included (HBA, JBOD, NVMe, and RAID logical drives).
// Marks the live-OS / install-media disk as Protected so the UI can skip it.
func ListDiskPartitions(ctx context.Context, h Host) (PartitionScan, error) {
	// Walk /sys/block for the authoritative disk+partition set. lsblk JSON/tree
	// mode often returns disks with an empty children[] (or flat siblings that
	// older parsers dropped), which made the UI show "no partitions" while a
	// plain `lsblk` on the host clearly had sda1/sda2/….
	script := `
set -euo pipefail
export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin${PATH:+:$PATH}"
# TSV we control (tabs never appear in kernel device names):
# DISK <name> <size_bytes> <model> <serial> <tran> <type>
# PART <parent> <name> <size_bytes> <fstype> <parttype> <partlabel> <partuuid> <mount> <uuid> <type>
field() { # lsblk -n -o COL /dev/NAME → single line, trimmed
  lsblk -n -o "$1" "$2" 2>/dev/null | head -n1 | tr -d '\r' | sed 's/^[[:space:]]*//;s/[[:space:]]*$//' || true
}
sys_size_bytes() {
  # /sys/block/*/size is 512-byte sectors
  _s=$(cat "$1/size" 2>/dev/null || echo 0)
  printf '%s\n' "$((_s * 512))"
}
for b in /sys/block/*; do
  [ -e "$b" ] || continue
  name=$(basename "$b")
  case "$name" in
    # loop/ram + NVMe multipath controller nodes (nvme0c0n1) — not wipe targets
    loop*|ram*|zram*|fd*|nvme*c*n*) continue ;;
  esac
  path="/dev/$name"
  # /sys/block can list non-block entries (e.g. nvme0c0n1); never invent a "disk".
  [ -b "$path" ] || continue
  typ=$(field TYPE "$path")
  [ -n "$typ" ] || continue
  case "$typ" in
    disk|rom|mpath) ;;
    *) continue ;;
  esac
  size=$(sys_size_bytes "$b")
  model=$(field MODEL "$path")
  [ -n "$model" ] || model=$(tr -d '\0\r\n' <"$b/device/model" 2>/dev/null | sed 's/[[:space:]]*$//' || true)
  serial=$(field SERIAL "$path")
  tran=$(field TRAN "$path")
  printf 'DISK\t%s\t%s\t%s\t%s\t%s\t%s\n' "$name" "$size" "$model" "$serial" "$tran" "$typ"
  # Partition device dirs live under the disk: sda/sda1, nvme0n1/nvme1n1p1
  for p in "$b"/"$name"*; do
    [ -e "$p" ] || continue
    pname=$(basename "$p")
    [ "$pname" = "$name" ] && continue
    ppath="/dev/$pname"
    [ -b "$ppath" ] || continue
    ptyp=$(field TYPE "$ppath")
    [ -n "$ptyp" ] || ptyp=part
    case "$ptyp" in
      part|crypt|lvm|md|raid*) ;;
      *) ptyp=part ;;
    esac
    psize=$(sys_size_bytes "$p")
    printf 'PART\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
      "$name" "$pname" "$psize" \
      "$(field FSTYPE "$ppath")" \
      "$(field PARTTYPE "$ppath")" \
      "$(field PARTLABEL "$ppath")" \
      "$(field PARTUUID "$ppath")" \
      "$(field MOUNTPOINT "$ppath")" \
      "$(field UUID "$ppath")" \
      "$ptyp"
  done
done
`
	raw, err := RunSSH(ctx, h.IP, h.Creds, SudoBashLC(script))
	if err != nil {
		return PartitionScan{}, err
	}
	disks, err := parseHostBlockListing(raw)
	if err != nil {
		return PartitionScan{}, err
	}

	liveDisk, liveReason := detectLiveDisk(ctx, h)

	for i := range disks {
		path := disks[i].Path
		if disks[i].Partitions == nil {
			disks[i].Partitions = []DiskPart{}
		}
		if liveDisk != "" && sameBlockDev(path, liveDisk) {
			disks[i].Protected = true
			disks[i].ProtectReason = liveReason
			if disks[i].ProtectReason == "" {
				disks[i].ProtectReason = "live OS / install media"
			}
		}
		// Optical / read-only style names — never wipe.
		base := strings.TrimPrefix(path, "/dev/")
		if strings.HasPrefix(base, "sr") || strings.HasPrefix(base, "cdrom") || strings.HasPrefix(base, "fd") {
			disks[i].Protected = true
			if disks[i].ProtectReason == "" {
				disks[i].ProtectReason = "optical/floppy — skipped"
			}
		}
	}
	if disks == nil {
		disks = []DiskParts{}
	}
	return PartitionScan{Disks: disks, LiveDisk: liveDisk, DataIP: h.IP}, nil
}

// parseHostBlockListing accepts sysfs TSV (preferred), lsblk -P pairs, or lsblk -J.
func parseHostBlockListing(raw string) ([]DiskParts, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty block listing")
	}
	if strings.Contains(raw, "DISK\t") || strings.HasPrefix(raw, "DISK\t") {
		disks := parseSysfsListing(raw)
		if len(disks) == 0 {
			return nil, fmt.Errorf("parse block listing: no disks in sysfs TSV")
		}
		return disks, nil
	}
	if strings.Contains(raw, `NAME="`) {
		disks := assembleDiskParts(parseLsblkPairs(raw))
		if len(disks) == 0 {
			return nil, fmt.Errorf("parse lsblk: no disks in pairs output")
		}
		return disks, nil
	}
	blob := extractJSONObject(raw)
	var tree struct {
		Blockdevices []wipeLsblkNode `json:"blockdevices"`
	}
	if err := json.Unmarshal([]byte(blob), &tree); err != nil {
		return nil, fmt.Errorf("parse lsblk: %w", err)
	}
	return assembleDiskParts(flattenLsblk(tree.Blockdevices, "")), nil
}

func parseSysfsListing(raw string) []DiskParts {
	var disks []DiskParts
	diskAt := map[string]int{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		cols := strings.Split(line, "\t")
		switch cols[0] {
		case "DISK":
			// DISK name size model serial tran type
			if len(cols) < 3 {
				continue
			}
			name := cols[1]
			if name == "" {
				continue
			}
			if _, ok := diskAt[name]; ok {
				continue
			}
			typ := ""
			if len(cols) >= 7 {
				typ = cols[6]
			}
			// Keep rom in the list (marked protected later) so the live medium is visible.
			if typ != "" && !strings.EqualFold(typ, "disk") && !strings.EqualFold(typ, "rom") && !strings.EqualFold(typ, "mpath") {
				continue
			}
			model, serial, tran := "", "", ""
			if len(cols) > 3 {
				model = cols[3]
			}
			if len(cols) > 4 {
				serial = cols[4]
			}
			if len(cols) > 5 {
				tran = cols[5]
			}
			diskAt[name] = len(disks)
			disks = append(disks, DiskParts{
				Name: name, Path: "/dev/" + name, Size: formatByteCount(cols[2]),
				Model: model, Serial: serial, Tran: tran,
				Partitions: []DiskPart{},
			})
			if strings.EqualFold(typ, "rom") {
				disks[len(disks)-1].Protected = true
				disks[len(disks)-1].ProtectReason = "optical/floppy — skipped"
			}
		case "PART":
			// PART parent name size fstype parttype partlabel partuuid mount uuid type
			if len(cols) < 3 {
				continue
			}
			parent, name := cols[1], cols[2]
			idx, ok := diskAt[parent]
			if !ok {
				continue
			}
			size, fstype, parttype, partlabel, partuuid, mount, uuid, typ := "", "", "", "", "", "", "", "part"
			if len(cols) > 3 {
				size = cols[3]
			}
			if len(cols) > 4 {
				fstype = cols[4]
			}
			if len(cols) > 5 {
				parttype = cols[5]
			}
			if len(cols) > 6 {
				partlabel = cols[6]
			}
			if len(cols) > 7 {
				partuuid = cols[7]
			}
			if len(cols) > 8 {
				mount = cols[8]
			}
			if len(cols) > 9 {
				uuid = cols[9]
			}
			if len(cols) > 10 && cols[10] != "" {
				typ = cols[10]
			}
			disks[idx].Partitions = append(disks[idx].Partitions, DiskPart{
				Name: name, Path: "/dev/" + name, Size: formatByteCount(size),
				Type: typ, Fstype: fstype, PartType: parttype, PartLabel: partlabel,
				Mountpoint: mount, PartUUID: partuuid, UUID: uuid,
				ESP: isESPPart(parttype),
			})
		}
	}
	if disks == nil {
		disks = []DiskParts{}
	}
	return disks
}

// parseLsblkPairs reads `lsblk -P` lines. Values are double-quoted; non-printable
// bytes are \xHH (util-linux export format).
func parseLsblkPairs(raw string) []blkRow {
	var rows []blkRow
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, `="`) {
			continue
		}
		kv := parsePairsLine(line)
		name := kv["name"]
		if name == "" {
			continue
		}
		rows = append(rows, blkRow{
			Name: name, PKName: kv["pkname"], Path: kv["path"], Size: kv["size"],
			Type: kv["type"], Tran: kv["tran"], Model: kv["model"], Serial: kv["serial"],
			Fstype: kv["fstype"], PartType: kv["parttype"], Label: kv["label"],
			PartLabel: kv["partlabel"], Mountpoint: kv["mountpoint"],
			PartUUID: kv["partuuid"], UUID: kv["uuid"],
		})
	}
	return rows
}

func parsePairsLine(line string) map[string]string {
	out := map[string]string{}
	i := 0
	n := len(line)
	for i < n {
		for i < n && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if i >= n {
			break
		}
		eq := strings.IndexByte(line[i:], '=')
		if eq <= 0 {
			break
		}
		key := strings.ToLower(strings.TrimSpace(line[i : i+eq]))
		i += eq + 1
		if i >= n || line[i] != '"' {
			break
		}
		i++
		var b strings.Builder
		for i < n {
			c := line[i]
			if c == '\\' && i+1 < n {
				if line[i+1] == 'x' && i+3 < n {
					if v, err := strconv.ParseUint(line[i+2:i+4], 16, 8); err == nil {
						b.WriteByte(byte(v))
						i += 4
						continue
					}
				}
				b.WriteByte(line[i+1])
				i += 2
				continue
			}
			if c == '"' {
				i++
				break
			}
			b.WriteByte(c)
			i++
		}
		if key != "" {
			out[key] = b.String()
		}
	}
	return out
}

// ExtractJSONObject returns the first complete top-level JSON object in raw.
// SSH/sudo may prepend chatter; taking the last '{' breaks nested lsblk trees
// (child object + leftover ']' → "invalid character ']' after top-level value").
func ExtractJSONObject(raw string) string {
	return extractJSONObject(raw)
}

func extractJSONObject(raw string) string {
	raw = strings.TrimSpace(raw)
	start := strings.Index(raw, "{")
	if start < 0 {
		return raw
	}
	depth := 0
	inString := false
	escape := false
	for i := start; i < len(raw); i++ {
		ch := raw[i]
		if inString {
			if escape {
				escape = false
				continue
			}
			if ch == '\\' {
				escape = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return raw[start : i+1]
			}
		}
	}
	return raw[start:]
}

func sameBlockDev(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	// /dev/sda vs sda
	a = strings.TrimPrefix(a, "/dev/")
	b = strings.TrimPrefix(b, "/dev/")
	return a == b
}

// detectLiveDisk finds the whole-disk device that backs the live OS root/media.
func detectLiveDisk(ctx context.Context, h Host) (disk, reason string) {
	script := `
set -euo pipefail
export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin${PATH:+:$PATH}"
to_disk() {
  src="$1"
  [ -z "$src" ] && return 1
  src="${src%%[*}"   # drop [partition] from btrfs etc
  src="${src#/dev/mapper/}"
  # If still a path under /dev, resolve PKNAME up to disk.
  if [ -b "$1" ] || [ -e "$1" ]; then
    cur="$1"
  elif [ -b "/dev/$1" ]; then
    cur="/dev/$1"
  else
    cur="$1"
  fi
  # Walk parents via lsblk until TYPE=disk (or rom for live ISO on sr*).
  for _ in 1 2 3 4 5 6 7 8; do
    typ=$(lsblk -no TYPE "$cur" 2>/dev/null | head -n1 | tr -d '[:space:]' || true)
    if [ "$typ" = "disk" ] || [ "$typ" = "rom" ]; then
      path=$(lsblk -no PATH "$cur" 2>/dev/null | head -n1 | tr -d '[:space:]' || true)
      [ -n "$path" ] && echo "$path" && return 0
      echo "/dev/$(lsblk -no NAME "$cur" 2>/dev/null | head -n1 | tr -d '[:space:]')"
      return 0
    fi
    pk=$(lsblk -no PKNAME "$cur" 2>/dev/null | head -n1 | tr -d '[:space:]' || true)
    [ -z "$pk" ] && break
    cur="/dev/$pk"
  done
  return 1
}

# Prefer explicit live-media mounts, then root.
for mp in /cdrom /isodevice /run/live/medium /lib/live/mount/medium /media/cdrom; do
  src=$(findmnt -n -o SOURCE "$mp" 2>/dev/null || true)
  if [ -n "$src" ]; then
    if d=$(to_disk "$src"); then
      echo "LIVE_DISK=$d"
      echo "LIVE_REASON=live media mount $mp"
      exit 0
    fi
  fi
done

src=$(findmnt -n -o SOURCE / 2>/dev/null || true)
if [ -n "$src" ]; then
  if d=$(to_disk "$src"); then
    echo "LIVE_DISK=$d"
    echo "LIVE_REASON=root filesystem"
    exit 0
  fi
fi
echo "LIVE_DISK="
echo "LIVE_REASON="
`
	out, err := RunSSH(ctx, h.IP, h.Creds, SudoBashLC(script))
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "LIVE_DISK="):
			disk = strings.TrimSpace(strings.TrimPrefix(line, "LIVE_DISK="))
		case strings.HasPrefix(line, "LIVE_REASON="):
			reason = strings.TrimSpace(strings.TrimPrefix(line, "LIVE_REASON="))
		}
	}
	return disk, reason
}

// WipeProgress receives phase/log lines while clearing partition tables.
type WipeProgress func(phase, line string)

// WipeDiskPartitions zaps GPT/MBR on every non-protected disk (ESP included).
// It does not zero the whole device — only signatures + partition tables.
// Live-OS / install-media disks are always skipped.
func WipeDiskPartitions(ctx context.Context, h Host, progress WipeProgress) error {
	if progress == nil {
		progress = func(string, string) {}
	}
	progress("wipe", "scanning disks and clearing partition tables (ESP included)")
	script := buildWipeScript()
	out, err := RunSSHRetryLines(ctx, h.IP, h.Creds, SudoBashLC(script), 3, func(line string) {
		progress("wipe", line)
	})
	if err != nil {
		if strings.TrimSpace(out) != "" {
			return fmt.Errorf("%w\n--- remote ---\n%s", err, trimWipeTail(out, 60))
		}
		return err
	}
	if !strings.Contains(out, "WIPE_OK") {
		return fmt.Errorf("wipe did not report WIPE_OK\n--- remote ---\n%s", trimWipeTail(out, 60))
	}
	progress("wipe", "done")
	return nil
}

func trimWipeTail(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) <= n {
		return strings.TrimSpace(s)
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

func buildWipeScript() string {
	// Clears partition tables on all disks except the live-OS / media disk.
	// Collects PARTUUIDs first so stale efibootmgr entries can be removed.
	return `
set -euo pipefail
export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin${PATH:+:$PATH}"

copper_bin() {
  _n="$1"
  if command -v "$_n" >/dev/null 2>&1; then command -v "$_n"; return 0; fi
  for _d in /usr/sbin /sbin /usr/local/sbin /usr/bin /bin; do
    if [ -x "$_d/$_n" ]; then printf '%s\n' "$_d/$_n"; return 0; fi
  done
  return 1
}

log() { printf '%s\n' "$*"; }

SGDISK=$(copper_bin sgdisk || true)
WIPEFS=$(copper_bin wipefs || true)
EFIBOOTMGR=$(copper_bin efibootmgr || true)
PARTED=$(copper_bin parted || true)
log "tools sgdisk=${SGDISK:-MISSING} wipefs=${WIPEFS:-MISSING} efibootmgr=${EFIBOOTMGR:-MISSING} parted=${PARTED:-MISSING}"

to_disk() {
  src="$1"
  [ -z "$src" ] && return 1
  src="${src%%[*}"
  if [ -b "$1" ] || [ -e "$1" ]; then
    cur="$1"
  elif [ -b "/dev/$1" ]; then
    cur="/dev/$1"
  else
    cur="$1"
  fi
  for _ in 1 2 3 4 5 6 7 8; do
    typ=$(lsblk -no TYPE "$cur" 2>/dev/null | head -n1 | tr -d '[:space:]' || true)
    if [ "$typ" = "disk" ] || [ "$typ" = "rom" ]; then
      path=$(lsblk -no PATH "$cur" 2>/dev/null | head -n1 | tr -d '[:space:]' || true)
      [ -n "$path" ] && echo "$path" && return 0
      echo "/dev/$(lsblk -no NAME "$cur" 2>/dev/null | head -n1 | tr -d '[:space:]')"
      return 0
    fi
    pk=$(lsblk -no PKNAME "$cur" 2>/dev/null | head -n1 | tr -d '[:space:]' || true)
    [ -z "$pk" ] && break
    cur="/dev/$pk"
  done
  return 1
}

LIVE_DISK=""
LIVE_REASON=""
for mp in /cdrom /isodevice /run/live/medium /lib/live/mount/medium /media/cdrom; do
  src=$(findmnt -n -o SOURCE "$mp" 2>/dev/null || true)
  if [ -n "$src" ]; then
    if d=$(to_disk "$src"); then
      LIVE_DISK="$d"
      LIVE_REASON="live media mount $mp"
      break
    fi
  fi
done
if [ -z "$LIVE_DISK" ]; then
  src=$(findmnt -n -o SOURCE / 2>/dev/null || true)
  if [ -n "$src" ]; then
    if d=$(to_disk "$src"); then
      LIVE_DISK="$d"
      LIVE_REASON="root filesystem"
    fi
  fi
fi
log "live_disk=${LIVE_DISK:-unknown} reason=${LIVE_REASON:-}"

# Collect PARTUUIDs we are about to destroy (for NVRAM cleanup).
PARTUUIDS=""
WIPED=0
SKIPPED=0
FAILED=0

while IFS= read -r dev; do
  [ -z "$dev" ] && continue
  base=$(basename "$dev")
  case "$base" in
    sr*|cdrom*|fd*) log "SKIP $dev (optical/floppy)"; SKIPPED=$((SKIPPED+1)); continue ;;
  esac
  if [ -n "$LIVE_DISK" ] && [ "$dev" = "$LIVE_DISK" ]; then
    log "SKIP $dev (protected: $LIVE_REASON)"
    SKIPPED=$((SKIPPED+1))
    continue
  fi

  # Snapshot PARTUUIDs before wipe.
  while IFS= read -r pu; do
    [ -z "$pu" ] && continue
    PARTUUIDS="$PARTUUIDS $pu"
  done < <(lsblk -no PARTUUID "$dev" 2>/dev/null | tr -d ' ' | grep -v '^$' || true)

  log "--- wipe $dev ---"
  lsblk -o NAME,SIZE,TYPE,FSTYPE,PARTTYPE,PARTLABEL,MOUNTPOINT,PARTUUID "$dev" 2>/dev/null || true

  # Unmount anything under this disk (deepest mounts first).
  while IFS= read -r mp; do
    [ -z "$mp" ] && continue
    log "umount $mp"
    umount -f "$mp" 2>/dev/null || umount -l "$mp" 2>/dev/null || log "WARN: could not umount $mp"
  done < <(lsblk -nro MOUNTPOINT "$dev" 2>/dev/null | awk 'NF{print}' | awk '{print length, $0}' | sort -nr | cut -d" " -f2-)

  ok=0
  if [ -n "$SGDISK" ]; then
    if "$SGDISK" -Z "$dev" >/tmp/copper-wipe-sgdisk.out 2>&1; then
      ok=1
      log "sgdisk -Z ok"
    else
      log "WARN: sgdisk -Z failed:"; cat /tmp/copper-wipe-sgdisk.out 2>/dev/null || true
    fi
  fi
  if [ -n "$WIPEFS" ]; then
    if "$WIPEFS" -a -f "$dev" >/tmp/copper-wipe-wipefs.out 2>&1; then
      ok=1
      log "wipefs -a ok"
    else
      log "WARN: wipefs -a failed:"; cat /tmp/copper-wipe-wipefs.out 2>/dev/null || true
    fi
  fi
  # Fallback: zap primary + backup GPT / MBR areas (not a full disk zero).
  if [ "$ok" -eq 0 ]; then
    log "fallback: dd first+last 1MiB"
    bytes=$(blockdev --getsize64 "$dev" 2>/dev/null || echo 0)
    mi=$((bytes / 1048576))
    dd_ok=0
    if dd if=/dev/zero of="$dev" bs=1M count=1 conv=fsync status=none 2>/tmp/copper-wipe-dd.err; then
      dd_ok=1
    fi
    if [ "$mi" -gt 2 ] && dd if=/dev/zero of="$dev" bs=1M count=1 seek=$((mi - 1)) conv=fsync status=none 2>>/tmp/copper-wipe-dd.err; then
      dd_ok=1
    fi
    if [ "$dd_ok" -eq 1 ]; then
      ok=1
      log "dd zap ok"
    else
      log "ERROR: dd zap failed:"; cat /tmp/copper-wipe-dd.err 2>/dev/null || true
    fi
  fi

  if [ "$ok" -eq 1 ]; then
    WIPED=$((WIPED+1))
    log "WIPED $dev"
  else
    FAILED=$((FAILED+1))
    log "ERROR: failed to wipe $dev"
  fi
done < <(lsblk -dnpo PATH,TYPE 2>/dev/null | awk '$2=="disk"{print $1}')

# Drop stale UEFI Boot#### entries that referenced wiped PARTUUIDs.
# Must not fail the wipe: empty efibootmgr -v / no Boot#### lines makes grep
# exit 1, and with pipefail that used to mark a successful wipe as failed.
if [ -n "$EFIBOOTMGR" ] && [ -n "$(echo "$PARTUUIDS" | tr -d '[:space:]')" ]; then
  log "--- efibootmgr cleanup ---"
  EFI_OUT=$("$EFIBOOTMGR" -v 2>/dev/null || true)
  if [ -z "$(echo "$EFI_OUT" | tr -d '[:space:]')" ]; then
    log "efibootmgr -v empty (EFI vars unavailable?); NVRAM cleanup skipped"
  else
    echo "$EFI_OUT" | head -n 80 || true
    for pu in $PARTUUIDS; do
      [ -z "$pu" ] && continue
      pu_lc=$(echo "$pu" | tr 'A-F' 'a-f')
      while IFS= read -r line; do
        [ -z "$line" ] && continue
        line_lc=$(echo "$line" | tr 'A-F' 'a-f')
        case "$line_lc" in
          *"$pu_lc"*)
            bootid=$(echo "$line" | sed -n 's/^Boot\([0-9A-Fa-f]\{4\}\).*/\1/p')
            [ -z "$bootid" ] && continue
            log "efibootmgr -B -b $bootid (PARTUUID $pu)"
            "$EFIBOOTMGR" -B -b "$bootid" 2>&1 || log "WARN: delete Boot$bootid failed"
            ;;
        esac
      done < <(echo "$EFI_OUT" | grep -E '^Boot[0-9A-Fa-f]{4}' || true)
    done
  fi
else
  log "efibootmgr cleanup skipped (no tool or no PARTUUIDs)"
fi

partprobe 2>/dev/null || true
udevadm settle 2>/dev/null || true
log "summary wiped=$WIPED skipped=$SKIPPED failed=$FAILED"
if [ "$FAILED" -gt 0 ]; then
  log "WIPE_FAIL"
  exit 1
fi
log "WIPE_OK"
`
}
