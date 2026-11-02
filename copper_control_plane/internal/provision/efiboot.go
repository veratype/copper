package provision

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"copper_control_plane/internal/storage"
)

// sanitizeEFIBootLabel keeps efibootmgr -L short and NVRAM-safe.
func sanitizeEFIBootLabel(hostname string) string {
	label := strings.TrimSpace(hostname)
	if label == "" {
		label = "Copper-OS"
	}
	label = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == ' ':
			return r
		default:
			return '-'
		}
	}, label)
	if len(label) > 40 {
		label = label[:40]
	}
	return label
}

// efiLoaderPath converts a slash path on the ESP (e.g. EFI/ubuntu/shimx64.efi)
// into the single-backslash UEFI path efibootmgr -l expects: \EFI\ubuntu\shimx64.efi
func efiLoaderPath(rel string) string {
	rel = strings.TrimSpace(rel)
	rel = strings.TrimPrefix(rel, "/")
	rel = strings.ReplaceAll(rel, "/", `\`)
	if rel == "" {
		return `\EFI\ubuntu\shimx64.efi`
	}
	if !strings.HasPrefix(rel, `\`) {
		rel = `\` + rel
	}
	for strings.HasPrefix(rel, `\\`) {
		rel = strings.TrimPrefix(rel, `\`)
	}
	return rel
}

// parseBootIDs returns sorted unique Boot#### hex ids from efibootmgr output.
func parseBootIDs(efibootmgrOut string) []string {
	seen := map[string]struct{}{}
	var ids []string
	for _, line := range strings.Split(efibootmgrOut, "\n") {
		line = strings.TrimSpace(line)
		if len(line) < 8 || !strings.HasPrefix(line, "Boot") {
			continue
		}
		rest := line[4:]
		hex := make([]rune, 0, 4)
		for _, r := range rest {
			if len(hex) == 4 {
				break
			}
			if unicode.Is(unicode.ASCII_Hex_Digit, r) {
				hex = append(hex, unicode.ToUpper(r))
			} else {
				break
			}
		}
		if len(hex) != 4 {
			continue
		}
		id := string(hex)
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// newBootIDFromDiff returns the Boot#### id that appears in after but not before.
func newBootIDFromDiff(beforeOut, afterOut string) (string, error) {
	before := map[string]struct{}{}
	for _, id := range parseBootIDs(beforeOut) {
		before[id] = struct{}{}
	}
	var added []string
	for _, id := range parseBootIDs(afterOut) {
		if _, ok := before[id]; !ok {
			added = append(added, id)
		}
	}
	if len(added) == 0 {
		return "", fmt.Errorf("no new Boot#### id after efibootmgr -c")
	}
	if len(added) > 1 {
		sort.Strings(added)
		return added[len(added)-1], nil
	}
	return added[0], nil
}

// bootIDForLabel returns the last Boot#### whose entry line contains label.
func bootIDForLabel(efibootmgrOut, label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return ""
	}
	var last string
	for _, line := range strings.Split(efibootmgrOut, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Boot") || !strings.Contains(line, label) {
			continue
		}
		ids := parseBootIDs(line)
		if len(ids) == 1 {
			last = ids[0]
		}
	}
	return last
}

// entryMatchesLabel reports whether Boot<id> in the listing carries label.
func entryMatchesLabel(efibootmgrOut, id, label string) bool {
	id = strings.ToUpper(strings.TrimSpace(id))
	label = strings.TrimSpace(label)
	if id == "" || label == "" {
		return false
	}
	prefix := "Boot" + id
	for _, line := range strings.Split(efibootmgrOut, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		rest := line[len(prefix):]
		if rest != "" {
			r := rest[0]
			if r != '*' && r != ' ' && r != '\t' {
				continue
			}
		}
		return strings.Contains(line, label)
	}
	return false
}

// buildBootOrder puts newID first and keeps the remaining BootOrder entries.
func buildBootOrder(newID, bootOrderLine string) string {
	newID = strings.ToUpper(strings.TrimSpace(newID))
	order := strings.TrimSpace(bootOrderLine)
	order = strings.TrimPrefix(order, "BootOrder:")
	order = strings.TrimSpace(order)

	var rest []string
	seen := map[string]struct{}{newID: {}}
	for _, part := range strings.Split(order, ",") {
		id := strings.ToUpper(strings.TrimSpace(part))
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		rest = append(rest, id)
	}
	if len(rest) == 0 {
		return newID
	}
	return newID + "," + strings.Join(rest, ",")
}

// parseBootOrderLine extracts the CSV from a "BootOrder: A,B,C" line.
func parseBootOrderLine(efibootmgrOut string) string {
	for _, line := range strings.Split(efibootmgrOut, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "BootOrder:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "BootOrder:"))
		}
	}
	return ""
}

// bootOrderStartsWith reports whether BootOrder CSV begins with id.
func bootOrderStartsWith(orderCSV, id string) bool {
	orderCSV = strings.ToUpper(strings.TrimSpace(orderCSV))
	id = strings.ToUpper(strings.TrimSpace(id))
	if id == "" || orderCSV == "" {
		return false
	}
	return orderCSV == id || strings.HasPrefix(orderCSV, id+",")
}

// staleBootIDs returns Boot#### ids to delete before creating a fresh entry:
// anything pointing at this ESP PARTUUID, plus any entry carrying our label.
func staleBootIDs(listing, espUUID, label string) []string {
	espUUID = strings.TrimSpace(espUUID)
	label = strings.TrimSpace(label)
	seen := map[string]struct{}{}
	var ids []string
	add := func(id string) {
		id = strings.ToUpper(strings.TrimSpace(id))
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	needle := ""
	if espUUID != "" {
		needle = "GPT," + espUUID
	}
	for _, line := range strings.Split(listing, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Boot") {
			continue
		}
		parsed := parseBootIDs(line)
		if len(parsed) != 1 {
			continue
		}
		id := parsed[0]
		if needle != "" && strings.Contains(line, needle) {
			add(id)
			continue
		}
		if label != "" && entryMatchesLabel(line+"\n", id, label) {
			add(id)
		}
	}
	sort.Strings(ids)
	return ids
}

// resolveNewBootID picks the created Boot#### via set-diff, with label fallback.
func resolveNewBootID(before, after, label string) (string, error) {
	id, err := newBootIDFromDiff(before, after)
	if err != nil {
		id = bootIDForLabel(after, label)
		if id == "" {
			return "", fmt.Errorf("%w; label %q not in listing", err, label)
		}
	}
	if !entryMatchesLabel(after, id, label) {
		return "", fmt.Errorf("Boot%s does not carry label %q", id, label)
	}
	return id, nil
}

func extractMarkedBlock(out, begin, end string) string {
	i := strings.Index(out, begin)
	if i < 0 {
		return ""
	}
	rest := out[i+len(begin):]
	j := strings.Index(rest, end)
	if j < 0 {
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(rest[:j])
}

func fieldValue(out, key string) string {
	prefix := key + "="
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

type efiProbe struct {
	ESPPart   string
	ESPNum    string
	ESPUUID   string
	LoaderRel string
	Listing   string
}

// runEFIBoot probes NVRAM on the live OS, decides deletions/order in Go, then
// applies efibootmgr -B / -c / -o with tiny remote scripts (no awk/comm parsing).
func runEFIBoot(ctx context.Context, h storage.Host, disk, hostname string, progress ProgressFunc) error {
	if progress == nil {
		progress = func(string, string) {}
	}
	label := sanitizeEFIBootLabel(hostname)
	progress("efiboot", "probe ESP + efibootmgr listing on "+disk)

	probeOut, err := storage.RunSSHRetry(ctx, h.IP, h.Creds, "sudo -n bash -lc "+storage.ShellQuote(buildEFIBootProbeScript(disk)), 3)
	logLines(progress, "efiboot", probeOut)
	if strings.Contains(probeOut, "EFIBOOT_SKIP") {
		progress("efiboot", "WARNING: efibootmgr missing on live OS — UEFI BootOrder not pinned; old OS on another disk may still win")
		return nil
	}
	if err != nil {
		return fmt.Errorf("UEFI boot probe failed: %v\n--- remote ---\n%s", err, remoteDiag(probeOut))
	}
	if !strings.Contains(probeOut, "EFIBOOT_PROBE_OK") {
		return fmt.Errorf("UEFI boot probe did not confirm success\n--- remote ---\n%s", remoteDiag(probeOut))
	}

	probe := efiProbe{
		ESPPart:   fieldValue(probeOut, "ESP_PART"),
		ESPNum:    fieldValue(probeOut, "ESP_NUM"),
		ESPUUID:   fieldValue(probeOut, "ESP_UUID"),
		LoaderRel: fieldValue(probeOut, "LOADER_REL"),
		Listing:   extractMarkedBlock(probeOut, "EFIBOOT_LISTING_BEGIN", "EFIBOOT_LISTING_END"),
	}
	if probe.ESPPart == "" || probe.ESPNum == "" {
		return fmt.Errorf("UEFI boot probe missing ESP_PART/ESP_NUM")
	}
	loader := efiLoaderPath(probe.LoaderRel)
	progress("efiboot", fmt.Sprintf("ESP=%s num=%s uuid=%s loader=%s", probe.ESPPart, probe.ESPNum, probe.ESPUUID, loader))

	stale := staleBootIDs(probe.Listing, probe.ESPUUID, label)
	if len(stale) > 0 {
		progress("efiboot", "delete stale Boot####: "+strings.Join(stale, ","))
	}

	progress("efiboot", "create boot entry label="+label)
	createOut, err := storage.RunSSHRetry(ctx, h.IP, h.Creds,
		"sudo -n bash -lc "+storage.ShellQuote(buildEFIBootCreateScript(disk, probe.ESPNum, label, loader, stale)), 3)
	logLines(progress, "efiboot", createOut)
	if err != nil {
		return fmt.Errorf("UEFI boot create failed: %v\n--- remote ---\n%s", err, remoteDiag(createOut))
	}
	if !strings.Contains(createOut, "EFIBOOT_CREATE_OK") {
		return fmt.Errorf("UEFI boot create did not confirm success\n--- remote ---\n%s", remoteDiag(createOut))
	}

	before := extractMarkedBlock(createOut, "EFIBOOT_BEFORE_BEGIN", "EFIBOOT_BEFORE_END")
	after := extractMarkedBlock(createOut, "EFIBOOT_AFTER_BEGIN", "EFIBOOT_AFTER_END")
	newID, err := resolveNewBootID(before, after, label)
	if err != nil {
		return fmt.Errorf("UEFI boot id resolve: %w", err)
	}
	order := buildBootOrder(newID, parseBootOrderLine(after))
	progress("efiboot", fmt.Sprintf("NEW_BOOT=%s SET BootOrder=%s", newID, order))

	orderOut, err := storage.RunSSHRetry(ctx, h.IP, h.Creds,
		"sudo -n bash -lc "+storage.ShellQuote(buildEFIBootOrderScript(order)), 3)
	logLines(progress, "efiboot", orderOut)
	if err != nil {
		return fmt.Errorf("UEFI boot order failed: %v\n--- remote ---\n%s", err, remoteDiag(orderOut))
	}
	if !strings.Contains(orderOut, "EFIBOOT_ORDER_OK") {
		return fmt.Errorf("UEFI boot order did not confirm success\n--- remote ---\n%s", remoteDiag(orderOut))
	}
	finalListing := extractMarkedBlock(orderOut, "EFIBOOT_FINAL_BEGIN", "EFIBOOT_FINAL_END")
	finalOrder := parseBootOrderLine(finalListing)
	if finalOrder == "" {
		finalOrder = parseBootOrderLine(orderOut)
	}
	if !bootOrderStartsWith(finalOrder, newID) {
		return fmt.Errorf("BootOrder after -o does not start with %s (got %s)", newID, finalOrder)
	}
	if !entryMatchesLabel(finalListing, newID, label) && !entryMatchesLabel(after, newID, label) {
		return fmt.Errorf("Boot%s lost label %q after -o", newID, label)
	}
	progress("efiboot", fmt.Sprintf("EFIBOOT_OK disk=%s esp=%s boot=%s order=%s", disk, probe.ESPPart, newID, finalOrder))
	return nil
}

// buildEFIBootProbeScript finds ESP/loader and dumps efibootmgr -v. No decisions.
func buildEFIBootProbeScript(disk string) string {
	return remotePrelude() + fmt.Sprintf(`
set -u
DEV=%s
log() { printf '%%s\n' "$1"; }

EFIBOOTMGR_BIN=$(copper_bin efibootmgr || true)
log "EFIBOOT_PROBE_BEGIN dev=$DEV"
log "efibootmgr=${EFIBOOTMGR_BIN:-MISSING}"
if [ -z "$EFIBOOTMGR_BIN" ]; then
  log "WARN: efibootmgr not found on live OS"
  log "EFIBOOT_SKIP reason=no-efibootmgr"
  exit 0
fi

partprobe "$DEV" 2>/dev/null || true
sleep 1
log "--- partitions on $DEV ---"
lsblk -ln -o NAME,TYPE,SIZE,FSTYPE,PARTTYPE,LABEL "$DEV" 2>&1 || true

ESP_PART=""
ESP_GUID="c12a7328-f81f-11d2-ba4b-00a0c93ec93b"
while read -r name typ fstype parttype; do
  [ "$typ" = "part" ] || continue
  path="/dev/$name"
  case "$path" in
    "$DEV"*) ;;
    *) continue ;;
  esac
  pt=$(printf '%%s' "$parttype" | tr 'A-Z' 'a-z')
  if [ "$pt" = "$ESP_GUID" ]; then
    ESP_PART="$path"
    break
  fi
done < <(lsblk -ln -o NAME,TYPE,FSTYPE,PARTTYPE "$DEV" 2>/dev/null)

if [ -z "$ESP_PART" ]; then
  TMP=$(mktemp -d)
  while read -r name typ fstype; do
    [ "$typ" = "part" ] || continue
    [ "$fstype" = "vfat" ] || [ "$fstype" = "fat32" ] || continue
    path="/dev/$name"
    case "$path" in
      "$DEV"*) ;;
      *) continue ;;
    esac
    if mount -o ro "$path" "$TMP" 2>/dev/null; then
      if [ -d "$TMP/EFI" ] || [ -d "$TMP/efi" ]; then
        ESP_PART="$path"
        umount "$TMP" 2>/dev/null || true
        break
      fi
      umount "$TMP" 2>/dev/null || true
    fi
  done < <(lsblk -ln -o NAME,TYPE,FSTYPE "$DEV" 2>/dev/null)
  rm -rf "$TMP"
fi

if [ -z "$ESP_PART" ]; then
  log "ERROR: no EFI System Partition found on $DEV"
  exit 21
fi

base=$(basename "$ESP_PART")
case "$base" in
  nvme*p[0-9]*|mmcblk*p[0-9]*) ESP_NUM=${base##*p} ;;
  *) ESP_NUM=$(printf '%%s' "$base" | sed -E 's/.*[^0-9]//') ;;
esac
if [ -z "$ESP_NUM" ]; then
  log "ERROR: could not parse partition number from $ESP_PART"
  exit 22
fi
ESP_UUID=$(blkid -s PARTUUID -o value "$ESP_PART" 2>/dev/null || true)

LOADER_REL=""
TMP=$(mktemp -d)
if mount -o ro "$ESP_PART" "$TMP" 2>/dev/null; then
  for cand in \
    EFI/ubuntu/shimx64.efi \
    EFI/ubuntu/grubx64.efi \
    EFI/BOOT/BOOTX64.EFI \
    EFI/boot/bootx64.efi
  do
    if [ -f "$TMP/$cand" ]; then
      LOADER_REL="$cand"
      break
    fi
  done
  umount "$TMP" 2>/dev/null || true
fi
rm -rf "$TMP"
if [ -z "$LOADER_REL" ]; then
  LOADER_REL="EFI/ubuntu/shimx64.efi"
  log "WARN: no loader file found on ESP; trying default $LOADER_REL"
fi

log "ESP_PART=$ESP_PART"
log "ESP_NUM=$ESP_NUM"
log "ESP_UUID=${ESP_UUID:-}"
log "LOADER_REL=$LOADER_REL"
log "EFIBOOT_LISTING_BEGIN"
"$EFIBOOTMGR_BIN" -v 2>&1 || true
log "EFIBOOT_LISTING_END"
log "EFIBOOT_PROBE_OK"
exit 0
`, storage.ShellQuote(disk))
}

// buildEFIBootCreateScript deletes stale Boot#### ids, then efibootmgr -c.
// Dumps listings before/after create for Go to resolve the new id.
func buildEFIBootCreateScript(disk, espNum, label, loader string, deleteIDs []string) string {
	var delLines strings.Builder
	for _, id := range deleteIDs {
		id = strings.ToUpper(strings.TrimSpace(id))
		if id == "" {
			continue
		}
		delLines.WriteString(fmt.Sprintf("log \"removing Boot%s\"\n\"$EFIBOOTMGR_BIN\" -b %s -B 2>&1 || true\n", id, storage.ShellQuote(id)))
	}
	return remotePrelude() + fmt.Sprintf(`
set -u
DEV=%s
ESP_NUM=%s
LABEL=%s
LOADER=%s
log() { printf '%%s\n' "$1"; }

EFIBOOTMGR_BIN=$(copper_bin efibootmgr || true)
if [ -z "$EFIBOOTMGR_BIN" ]; then
  log "ERROR: efibootmgr missing"
  exit 23
fi

%s
log "EFIBOOT_BEFORE_BEGIN"
"$EFIBOOTMGR_BIN" -v 2>&1 || true
log "EFIBOOT_BEFORE_END"

log "CREATE efibootmgr -c -d $DEV -p $ESP_NUM -L $LABEL -l $LOADER"
set +e
CREATE_OUT=$("$EFIBOOTMGR_BIN" -c -d "$DEV" -p "$ESP_NUM" -L "$LABEL" -l "$LOADER" 2>&1)
st=$?
set -e
log "$CREATE_OUT"
log "CREATE_EXIT=$st"
if [ "$st" != "0" ]; then
  log "ERROR: efibootmgr -c failed"
  exit 23
fi

log "EFIBOOT_AFTER_BEGIN"
"$EFIBOOTMGR_BIN" -v 2>&1 || true
log "EFIBOOT_AFTER_END"
log "EFIBOOT_CREATE_OK"
exit 0
`, storage.ShellQuote(disk), storage.ShellQuote(espNum), storage.ShellQuote(label), storage.ShellQuote(loader), delLines.String())
}

// buildEFIBootOrderScript sets BootOrder; Go already computed the CSV.
func buildEFIBootOrderScript(order string) string {
	return remotePrelude() + fmt.Sprintf(`
set -u
ORDER=%s
log() { printf '%%s\n' "$1"; }

EFIBOOTMGR_BIN=$(copper_bin efibootmgr || true)
if [ -z "$EFIBOOTMGR_BIN" ]; then
  log "ERROR: efibootmgr missing"
  exit 26
fi

log "SET BootOrder=$ORDER"
set +e
ORDER_OUT=$("$EFIBOOTMGR_BIN" -o "$ORDER" 2>&1)
ost=$?
set -e
log "$ORDER_OUT"
if [ "$ost" != "0" ]; then
  log "ERROR: efibootmgr -o failed"
  exit 26
fi

log "EFIBOOT_FINAL_BEGIN"
"$EFIBOOTMGR_BIN" -v 2>&1 || true
log "EFIBOOT_FINAL_END"
log "EFIBOOT_ORDER_OK"
exit 0
`, storage.ShellQuote(order))
}
