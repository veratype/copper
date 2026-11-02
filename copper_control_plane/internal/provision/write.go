package provision

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"copper_control_plane/internal/storage"
)

// Request is one OS provision against a live host.
type Request struct {
	ImageURL      string
	TargetDisk    string // /dev/sdX or /dev/nvme0n1
	Cidata        Cidata
	SkipPreflight bool // set after a successful batch preflight gate
	// ImageSHA256 / ImageSize come from the data-media server (file content hash).
	// Stamped onto the target after a successful write so a later retry can match them.
	ImageSHA256 string
	ImageSize   int64
	// ReuseImage enables checksum skip-write (retry only). First provision always rewrites.
	ReuseImage bool
}

func (r Request) Validate() error {
	r.ImageURL = strings.TrimSpace(r.ImageURL)
	r.TargetDisk = strings.TrimSpace(r.TargetDisk)
	if r.ImageURL == "" {
		return fmt.Errorf("image_url is required")
	}
	if !strings.HasPrefix(r.ImageURL, "http://") && !strings.HasPrefix(r.ImageURL, "https://") {
		return fmt.Errorf("image_url must be http(s)")
	}
	if r.TargetDisk == "" || !strings.HasPrefix(r.TargetDisk, "/dev/") {
		return fmt.Errorf("target_disk must be a /dev/ path")
	}
	// Reject partitions as whole-disk write targets (sdXN / nvmeXnYpZ).
	base := strings.TrimPrefix(r.TargetDisk, "/dev/")
	if looksLikePartition(base) {
		return fmt.Errorf("target_disk must be a whole disk, not a partition: %s", r.TargetDisk)
	}
	return r.Cidata.Validate()
}

func looksLikePartition(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	// md0 / md127 are whole arrays; md0p1 is a partition on the array.
	if strings.HasPrefix(name, "md") {
		i := strings.LastIndex(name, "p")
		if i <= 0 || i == len(name)-1 {
			return false
		}
		// require digits after p (md0p1), and a digit before p (md0p…)
		for _, ch := range name[i+1:] {
			if ch < '0' || ch > '9' {
				return false
			}
		}
		for _, ch := range name[:i] {
			if ch >= '0' && ch <= '9' {
				return true
			}
		}
		return false
	}
	// nvme0n1p1, mmcblk0p2
	if strings.Contains(name, "nvme") || strings.HasPrefix(name, "mmcblk") {
		i := strings.LastIndex(name, "p")
		if i <= 0 || i == len(name)-1 {
			return false
		}
		for _, ch := range name[i+1:] {
			if ch < '0' || ch > '9' {
				return false
			}
		}
		return true
	}
	// sda1, vda2, xvda3 — trailing digits after a letter
	i := len(name) - 1
	if i < 1 || name[i] < '0' || name[i] > '9' {
		return false
	}
	for i >= 0 && name[i] >= '0' && name[i] <= '9' {
		i--
	}
	return i >= 0 && name[i] >= 'a' && name[i] <= 'z'
}

// ProgressFunc receives phase / log lines during provision.
type ProgressFunc func(phase, line string)

func logLines(progress ProgressFunc, phase, blob string) {
	blob = strings.ReplaceAll(blob, "\r\n", "\n")
	blob = strings.ReplaceAll(blob, "\r", "\n")
	for _, line := range strings.Split(blob, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		progress(phase, line)
	}
}

// Run writes the cloud image and injects NoCloud seed on the live OS over SSH.
func Run(ctx context.Context, h storage.Host, req Request, progress ProgressFunc) error {
	if progress == nil {
		progress = func(string, string) {}
	}
	req.ImageURL = strings.TrimSpace(req.ImageURL)
	req.TargetDisk = strings.TrimSpace(req.TargetDisk)
	req.Cidata = req.Cidata.Normalize()
	if err := req.Validate(); err != nil {
		return err
	}

	if !req.SkipPreflight {
		if err := Preflight(ctx, h, req, progress); err != nil {
			return err
		}
	} else {
		progress("preflight", "skipped (already passed batch gate)")
	}

	// Stamp digest is always recorded after cidata; checksum skip-write is retry-only.
	expectSHA := strings.ToLower(strings.TrimSpace(req.ImageSHA256))
	expectSize := req.ImageSize
	checkSHA := ""
	checkSize := int64(0)
	if req.ReuseImage {
		checkSHA = expectSHA
		checkSize = expectSize
	}
	if req.ReuseImage && checkSHA != "" {
		progress("write", fmt.Sprintf("retry: checking target %s against media sha256=%s…", req.TargetDisk, shortSHA(checkSHA)))
	} else {
		progress("write", "download image to /tmp, then write raw to "+req.TargetDisk)
	}
	// Ubuntu *.img cloud images are often qcow2 (magic QFI\\xfb). Never pipe qcow2 into dd.
	// On retry only: if media SHA256 matches a stamp on the target (or raw prefix hash), skip rewrite.
	writeScript := remotePrelude() + fmt.Sprintf(`
set -u
DEV=%s
URL=%s
EXPECT_SHA=%s
EXPECT_SIZE=%s
DOWNLOAD_TOTAL=%s
IMG=/tmp/copper-cloud.img
STAMP_REL=etc/copper-image.sha256
log() { printf '%%s\n' "$1"; }

log "WRITE_BEGIN dev=$DEV url=$URL"
log "EXPECT_SHA=$EXPECT_SHA EXPECT_SIZE=$EXPECT_SIZE DOWNLOAD_TOTAL=$DOWNLOAD_TOTAL"
log "date=$(date -Is 2>/dev/null || date)"
df -h /tmp 2>&1 || true

CURL_BIN=$(copper_bin curl || true)
QEMU_BIN=$(copper_bin qemu-img || true)
DD_BIN=$(copper_bin dd || true)
SHA_BIN=$(copper_bin sha256sum || true)
log "tools curl=$CURL_BIN qemu-img=$QEMU_BIN dd=$DD_BIN sha256sum=$SHA_BIN"

# --- Skip rewrite only when caller asked for retry reuse (EXPECT_SHA set) ---
skip_write=0
if [ -n "$EXPECT_SHA" ] && [ -n "$SHA_BIN" ]; then
  # 1) Stamp left by a prior Copper provision (survives GPT relocate).
  partprobe "$DEV" 2>/dev/null || true
  sleep 1
  STAMP_TMP=$(mktemp -d)
  while read -r name typ; do
    [ "$typ" = "part" ] || continue
    path="/dev/$name"
    case "$path" in
      "$DEV"*) ;;
      *) continue ;;
    esac
    if mount -o ro "$path" "$STAMP_TMP" 2>/dev/null; then
      if [ -f "$STAMP_TMP/$STAMP_REL" ]; then
        got=$(tr -d '[:space:]' < "$STAMP_TMP/$STAMP_REL" | tr 'A-F' 'a-f')
        log "STAMP_FOUND path=$path value=$got"
        if [ "$got" = "$EXPECT_SHA" ]; then
          skip_write=1
          log "STAMP_MATCH — image already on target"
        else
          log "STAMP_MISMATCH want=$EXPECT_SHA got=$got"
        fi
      fi
      umount "$STAMP_TMP" 2>/dev/null || true
      [ "$skip_write" = "1" ] && break
    fi
  done < <(lsblk -ln -o NAME,TYPE "$DEV" 2>/dev/null)
  rm -rf "$STAMP_TMP"

  # 2) Fallback: hash first EXPECT_SIZE bytes of the disk (works if write finished
  #    but GPT/cidata never ran — GPT relocate changes early sectors so stamp is preferred).
  if [ "$skip_write" != "1" ] && [ "${EXPECT_SIZE:-0}" -gt 1048576 ] 2>/dev/null; then
    log "PREFIX_HASH_BEGIN bytes=$EXPECT_SIZE"
    set +e
    got=$("$DD_BIN" if="$DEV" bs=16M count=$(( (EXPECT_SIZE + 16777215) / 16777216 )) status=none 2>/dev/null \
      | head -c "$EXPECT_SIZE" | "$SHA_BIN" | awk '{print $1}')
    st_hash=$?
    set -e
    got=$(printf '%%s' "$got" | tr 'A-F' 'a-f' | tr -d '[:space:]')
    log "PREFIX_HASH_EXIT=$st_hash got=$got"
    if [ "$st_hash" = "0" ] && [ -n "$got" ] && [ "$got" = "$EXPECT_SHA" ]; then
      skip_write=1
      log "PREFIX_HASH_MATCH — image already on target"
    fi
  fi
fi

if [ "$skip_write" = "1" ]; then
  log "WRITE_SKIP sha256=$EXPECT_SHA"
  exit 0
fi

# Unmount anything on the target (best effort).
lsblk -ln -o NAME,TYPE "$DEV" 2>/dev/null | while read -r name typ; do
  [ "$typ" = "part" ] || continue
  log "umount /dev/$name"
  umount -f "/dev/$name" 2>&1 || true
done
umount -f "$DEV" 2>&1 || true

if [ -z "$CURL_BIN" ]; then
  log "ERROR: curl not found on live OS"
  exit 13
fi
if [ -z "$DD_BIN" ]; then
  log "ERROR: dd not found on live OS"
  exit 12
fi

rm -f "$IMG"
: > /tmp/copper-curl.err
log "DOWNLOAD_BEGIN -> $IMG total=$DOWNLOAD_TOTAL"
# Background curl; poll size every 2s and emit DOWNLOAD_PROGRESS for the job UI
# (streamed over SSH — works in batch, one stream per host).
human() {
  _b=$1
  if [ "${_b:-0}" -ge 1073741824 ] 2>/dev/null; then
    awk -v b="$_b" 'BEGIN{printf "%%.1fG", b/1073741824}'
  elif [ "${_b:-0}" -ge 1048576 ] 2>/dev/null; then
    awk -v b="$_b" 'BEGIN{printf "%%.1fM", b/1048576}'
  else
    printf '%%sB' "${_b:-0}"
  fi
}
set +e
"$CURL_BIN" -fL --retry 3 --retry-delay 2 --connect-timeout 30 \
  -o "$IMG" "$URL" >/tmp/copper-curl.err 2>&1 &
curl_pid=$!
while kill -0 "$curl_pid" 2>/dev/null; do
  sleep 2
  bytes=$(wc -c < "$IMG" 2>/dev/null | tr -d ' ' || echo 0)
  if [ "${DOWNLOAD_TOTAL:-0}" -gt 0 ] 2>/dev/null; then
    pct=$(( bytes * 100 / DOWNLOAD_TOTAL ))
    [ "$pct" -gt 100 ] && pct=100
    log "DOWNLOAD_PROGRESS pct=$pct bytes=$bytes total=$DOWNLOAD_TOTAL ($(human "$bytes") / $(human "$DOWNLOAD_TOTAL"))"
  else
    log "DOWNLOAD_PROGRESS bytes=$bytes ($(human "$bytes"))"
  fi
done
wait "$curl_pid"
st_curl=$?
set -e
log "DOWNLOAD_EXIT=$st_curl"
if [ -s /tmp/copper-curl.err ]; then
  log "--- curl stderr (tail) ---"
  tail -n 40 /tmp/copper-curl.err || true
fi
if [ "$st_curl" != "0" ]; then
  log "ERROR: download failed (exit $st_curl)"
  exit 14
fi
IMG_BYTES=$(wc -c < "$IMG" 2>/dev/null || echo 0)
log "DOWNLOAD_BYTES=$IMG_BYTES"
if [ "${DOWNLOAD_TOTAL:-0}" -gt 0 ] 2>/dev/null; then
  log "DOWNLOAD_PROGRESS pct=100 bytes=$IMG_BYTES total=$DOWNLOAD_TOTAL ($(human "$IMG_BYTES") / $(human "$DOWNLOAD_TOTAL"))"
fi
if [ "${IMG_BYTES:-0}" -lt 1048576 ]; then
  log "ERROR: downloaded file too small ($IMG_BYTES bytes)"
  exit 16
fi

# Detect format from magic: QFI\\xfb = qcow2; otherwise treat as raw.
HEAD=$(od -An -tx1 -N 4 "$IMG" 2>/dev/null | tr -d ' \n')
log "IMG_MAGIC_HEX=$HEAD"
FMT=raw
case "$HEAD" in
  514649fb|514649FB) FMT=qcow2 ;;
esac
log "IMG_FORMAT=$FMT"

set +e
if [ "$FMT" = "qcow2" ]; then
  if [ -z "$QEMU_BIN" ]; then
    log "ERROR: image is qcow2 but qemu-img is not installed on the live OS."
    log "ERROR: install qemu-utils, or serve a RAW cloud image instead."
    exit 17
  fi
  log "CONVERT_BEGIN qemu-img convert -O raw -> $DEV"
  "$QEMU_BIN" info "$IMG" 2>&1 | head -n 20 || true
  "$QEMU_BIN" convert -p -O raw "$IMG" "$DEV" 2>/tmp/copper-qemu.err
  st_write=$?
  log "CONVERT_EXIT=$st_write"
  if [ -s /tmp/copper-qemu.err ]; then
    log "--- qemu-img stderr (tail) ---"
    tail -n 40 /tmp/copper-qemu.err || true
  fi
else
  log "DD_BEGIN dd if=$IMG of=$DEV"
  "$DD_BIN" if="$IMG" of="$DEV" bs=16M conv=fsync 2>/tmp/copper-dd.err
  st_write=$?
  log "DD_EXIT=$st_write"
  if [ -s /tmp/copper-dd.err ]; then
    log "--- dd stderr (tail) ---"
    tail -n 40 /tmp/copper-dd.err || true
  fi
fi
set -e

rm -f "$IMG"
log "temp image removed"

lsblk -dn -o NAME,SIZE,TYPE "$DEV" 2>&1 || true
if [ "$st_write" != "0" ]; then
  log "ERROR: write/convert failed (exit $st_write)"
  exit 15
fi

sync
log "WRITE_OK format=$FMT"
exit 0
`, storage.ShellQuote(req.TargetDisk), storage.ShellQuote(req.ImageURL),
		storage.ShellQuote(checkSHA), storage.ShellQuote(fmt.Sprintf("%d", checkSize)),
		storage.ShellQuote(fmt.Sprintf("%d", expectSize)))
	// Stream remote lines live so DOWNLOAD_PROGRESS reaches the job UI during curl
	// (batch: each host job gets its own stream). Do not logLines afterward — that
	// would duplicate every line already forwarded via onLine.
	out, err := storage.RunSSHRetryLines(ctx, h.IP, h.Creds, storage.SudoBashLC(writeScript), 3, func(line string) {
		progress("write", line)
	})
	if err != nil {
		tail := trimTail(out, 80)
		if tail != "" {
			return fmt.Errorf("image write failed: %v\n--- remote ---\n%s", err, tail)
		}
		return fmt.Errorf("image write failed: %w", err)
	}
	skippedWrite := strings.Contains(out, "WRITE_SKIP")
	if !skippedWrite && !strings.Contains(out, "WRITE_OK") {
		return fmt.Errorf("image write did not confirm success\n--- remote ---\n%s", remoteDiag(out))
	}
	if skippedWrite {
		progress("write", "image already present (checksum match) — skipping download/dd")
	}

	// Cloud images are ~few GB; dd/qemu-img leave the backup GPT near the end of the
	// *image*, not the end of a large RAID LD / physical disk. Linux often still mounts
	// the primary header; UEFI firmware frequently rejects the table and offers no boot
	// device. Relocate the backup header before cidata mounts (growpart cannot run until
	// firmware boots the disk). Always re-run (idempotent) even after WRITE_SKIP.
	progress("gpt", "relocate backup GPT header to end of "+req.TargetDisk)
	gptScript := buildGPTFixScript(req.TargetDisk)
	out, err = storage.RunSSHRetry(ctx, h.IP, h.Creds, "sudo -n bash -lc "+storage.ShellQuote(gptScript), 3)
	logLines(progress, "gpt", out)
	if err != nil {
		return fmt.Errorf("GPT relocate failed: %v\n--- remote ---\n%s", err, remoteDiag(out))
	}
	if !strings.Contains(out, "GPT_OK") {
		return fmt.Errorf("GPT relocate did not confirm success\n--- remote ---\n%s", remoteDiag(out))
	}

	// Cloud images reuse fixed GPT PARTUUIDs. On second-hand / multi-disk hosts
	// that leaves twin HD(…,GPT,…) NVRAM nodes and identical grub/fstab refs.
	// sgdisk -G + PARTUUID rewrite before cidata/efiboot (no e2fsck/tune2fs).
	progress("reidentify", "sgdisk -G + rewrite PARTUUID refs on "+req.TargetDisk)
	reScript := buildReidentifyScript(req.TargetDisk)
	out, err = storage.RunSSHRetry(ctx, h.IP, h.Creds, "sudo -n bash -lc "+storage.ShellQuote(reScript), 3)
	logLines(progress, "reidentify", out)
	if err != nil {
		return fmt.Errorf("disk reidentify failed: %v\n--- remote ---\n%s", err, remoteDiag(out))
	}
	if !strings.Contains(out, "REIDENTIFY_OK") {
		return fmt.Errorf("disk reidentify did not confirm success\n--- remote ---\n%s", remoteDiag(out))
	}

	// Wipe prior seed/cloud-init/netplan state, then inject fresh cidata + image stamp.
	progress("cidata", "clean prior seed/cloud-init state, then inject NoCloud + netplan")
	userData := req.Cidata.UserData()
	metaData := req.Cidata.MetaData()
	netCfg := req.Cidata.NetworkConfig()
	netplanCfg := req.Cidata.NetplanConfig()
	progress("cidata", "seed paths: /var/lib/cloud/seed/nocloud/{user-data,meta-data,network-config} + /etc/netplan/99-copper.yaml")
	for _, line := range strings.Split(strings.TrimSpace(netCfg), "\n") {
		progress("cidata", "network-config| "+line)
	}
	inject := buildInjectScript(req.TargetDisk, userData, metaData, netCfg, netplanCfg, expectSHA, expectSize)
	out, err = storage.RunSSHRetry(ctx, h.IP, h.Creds, "sudo -n bash -lc "+storage.ShellQuote(inject), 3)
	logLines(progress, "cidata", out)
	if err != nil {
		return fmt.Errorf("cidata inject failed: %v\n--- remote ---\n%s", err, remoteDiag(out))
	}
	if !strings.Contains(out, "CIDATA_OK") {
		return fmt.Errorf("cidata inject did not confirm success\n--- remote ---\n%s", remoteDiag(out))
	}

	// Second-hand / multi-disk hosts: Redfish "Hdd" only means "boot some hard disk".
	// Firmware may still prefer an older OS on another drive. Probe efibootmgr on
	// the live OS; Go decides deletions + BootOrder; tiny scripts apply -B/-c/-o.
	progress("efiboot", "register UEFI boot entry on "+req.TargetDisk+" and put it first")
	if err := runEFIBoot(ctx, h, req.TargetDisk, req.Cidata.Hostname, progress); err != nil {
		return err
	}

	if skippedWrite {
		progress("done", "image reused (checksum match), cidata cleaned+injected, boot handled for "+req.TargetDisk)
	} else {
		progress("done", "image written, cidata injected, boot handled for "+req.TargetDisk)
	}
	return nil
}

func shortSHA(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 12 {
		return s
	}
	return s[:12] + "…"
}

func remoteDiag(out string) string {
	tail := trimTail(out, 80)
	if strings.TrimSpace(tail) == "" {
		return "(empty remote output — SSH session likely dropped; Copper retries transient SSH flakes automatically)"
	}
	return tail
}

func buildGPTFixScript(disk string) string {
	return remotePrelude() + fmt.Sprintf(`
set -u
DEV=%s
log() { printf '%%s\n' "$1"; }

SGDISK_BIN=$(copper_bin sgdisk || true)
GDISK_BIN=$(copper_bin gdisk || true)
log "GPT_BEGIN dev=$DEV"
log "disk bytes=$(blockdev --getsize64 "$DEV" 2>/dev/null || echo unknown)"
log "sgdisk=${SGDISK_BIN:-MISSING} gdisk=${GDISK_BIN:-MISSING}"

if [ -z "$SGDISK_BIN" ] && [ -z "$GDISK_BIN" ]; then
  log "ERROR: need sgdisk or gdisk on the live OS to fix GPT for UEFI"
  log "HINT: usually /usr/sbin/gdisk (package gdisk); non-root PATH may omit sbin"
  exit 18
fi

# Show the classic "backup GPT ... wrong place" warning when present.
if [ -n "$SGDISK_BIN" ]; then
  log "--- sgdisk -v (before) ---"
  "$SGDISK_BIN" -v "$DEV" 2>&1 || true
elif [ -n "$GDISK_BIN" ]; then
  log "--- gdisk -l (before) ---"
  "$GDISK_BIN" -l "$DEV" 2>&1 | head -n 40 || true
fi

# Try sgdisk first when present; on failure (or if missing) retry with gdisk.
ok=0
tool=""
set +e
if [ -n "$SGDISK_BIN" ]; then
  log "RELOCATE_TRY $SGDISK_BIN -e"
  "$SGDISK_BIN" -e "$DEV" 2>&1
  st=$?
  log "RELOCATE_EXIT=$st tool=sgdisk"
  if [ "$st" = "0" ]; then
    ok=1
    tool=sgdisk
  else
    log "RELOCATE_RETRY sgdisk failed; trying gdisk if available"
  fi
fi
if [ "$ok" = "0" ] && [ -n "$GDISK_BIN" ]; then
  log "RELOCATE_TRY $GDISK_BIN x/e/w"
  printf 'x\ne\nw\nY\n' | "$GDISK_BIN" "$DEV" 2>&1
  st=$?
  log "RELOCATE_EXIT=$st tool=gdisk"
  if [ "$st" = "0" ]; then
    ok=1
    tool=gdisk
  fi
fi
set -e

if [ "$ok" != "1" ]; then
  log "ERROR: GPT relocate failed with every available tool (sgdisk/gdisk)"
  exit 19
fi
log "RELOCATE_OK tool=$tool"

partprobe "$DEV" 2>&1 || true
sleep 1

if [ -n "$SGDISK_BIN" ]; then
  log "--- sgdisk -v (after) ---"
  "$SGDISK_BIN" -v "$DEV" 2>&1 || true
elif [ -n "$GDISK_BIN" ]; then
  log "--- gdisk -l (after) ---"
  "$GDISK_BIN" -l "$DEV" 2>&1 | head -n 40 || true
fi
lsblk -ln -o NAME,TYPE,SIZE,FSTYPE "$DEV" 2>&1 || true
log "GPT_OK"
exit 0
`, storage.ShellQuote(disk))
}

func buildInjectScript(disk, userData, metaData, networkConfig, netplanConfig, imageSHA string, imageSize int64) string {
	// Write seed files via base64 to avoid shell quoting hell, then mount root FS.
	// Always wipe prior Copper/cloud-init state before writing fresh seed (retry-safe).
	return fmt.Sprintf(`
set -euo pipefail
DEV=%s
EXPECT_SHA=%s
EXPECT_SIZE=%s
TMP=$(mktemp -d)
trap 'umount -f "$TMP/mnt" 2>/dev/null || true; rm -rf "$TMP"' EXIT
mkdir -p "$TMP/mnt" "$TMP/seed"
echo "CIDATA_BEGIN dev=$DEV tmp=$TMP expect_sha=$EXPECT_SHA"
printf '%%s' %s | base64 -d > "$TMP/seed/user-data"
printf '%%s' %s | base64 -d > "$TMP/seed/meta-data"
printf '%%s' %s | base64 -d > "$TMP/seed/network-config"
printf '%%s' %s | base64 -d > "$TMP/seed/99-copper.yaml"
for f in user-data meta-data network-config 99-copper.yaml; do
  bytes=$(wc -c < "$TMP/seed/$f" | tr -d ' ')
  echo "seed/$f bytes=$bytes"
  if [ "${bytes:-0}" -lt 1 ]; then
    echo "ERROR: decoded $f is empty" >&2
    exit 1
  fi
done
echo "--- network-config (NoCloud seed) ---"
cat "$TMP/seed/network-config"
echo "--- 99-copper.yaml (netplan) ---"
cat "$TMP/seed/99-copper.yaml"
partprobe "$DEV" 2>/dev/null || true
sleep 1
echo "partitions:"
lsblk -ln -o NAME,TYPE,SIZE,FSTYPE "$DEV" 2>&1 || true
ROOT=""
while read -r name typ; do
  [ "$typ" = "part" ] || continue
  echo "try mount /dev/$name"
  if mount "/dev/$name" "$TMP/mnt" 2>/tmp/copper-mount.err; then
    if [ -d "$TMP/mnt/etc" ] || [ -d "$TMP/mnt/var" ]; then
      ROOT="/dev/$name"
      echo "accepted root=$ROOT"
      break
    fi
    echo "skip /dev/$name (no /etc or /var)"
    umount "$TMP/mnt" 2>/dev/null || true
  else
    echo "mount failed /dev/$name: $(cat /tmp/copper-mount.err 2>/dev/null)"
  fi
done < <(lsblk -ln -o NAME,TYPE "$DEV")
if [ -z "$ROOT" ]; then
  echo "could not mount a root filesystem on $DEV" >&2
  exit 1
fi

echo "CIDATA_CLEAN prior seed / cloud-init / copper netplan"
# Remove ruined or stale seed completely — do not merge onto leftover files.
rm -rf "$TMP/mnt/var/lib/cloud/seed/nocloud" "$TMP/mnt/var/lib/cloud/seed/nocloud-net" 2>/dev/null || true
rm -rf "$TMP/mnt/var/lib/cloud/instance" "$TMP/mnt/var/lib/cloud/instances" \
  "$TMP/mnt/var/lib/cloud/data" "$TMP/mnt/var/lib/cloud/sem" \
  "$TMP/mnt/var/lib/cloud/handlers" "$TMP/mnt/var/lib/cloud/scripts" 2>/dev/null || true
rm -f "$TMP/mnt/etc/netplan/50-copper.yaml" "$TMP/mnt/etc/netplan/99-copper.yaml" 2>/dev/null || true
rm -f "$TMP/mnt/etc/copper-provisioned" "$TMP/mnt/etc/copper-image.sha256" 2>/dev/null || true
rm -f "$TMP/mnt/etc/cloud/cloud.cfg.d/90_copper_datasource.cfg" 2>/dev/null || true
rm -f "$TMP/mnt/etc/cloud/cloud.cfg.d/"*disable-network* 2>/dev/null || true
rm -f "$TMP/mnt/etc/cloud/cloud.cfg.d/99_copper_network_enable.cfg" 2>/dev/null || true
# Force cloud-init to treat next boot as first boot for networking/users.
rm -f "$TMP/mnt/etc/machine-id" 2>/dev/null || true
: > "$TMP/mnt/etc/machine-id" 2>/dev/null || true
rm -f "$TMP/mnt/var/lib/dbus/machine-id" 2>/dev/null || true

SEED="$TMP/mnt/var/lib/cloud/seed/nocloud"
SEED_NET="$TMP/mnt/var/lib/cloud/seed/nocloud-net"
mkdir -p "$SEED" "$SEED_NET" "$TMP/mnt/etc/cloud/cloud.cfg.d" "$TMP/mnt/etc/netplan" "$TMP/mnt/var/lib/cloud"

# NoCloud seed — cloud-init reads these on first boot.
cp -f "$TMP/seed/user-data" "$SEED/user-data"
cp -f "$TMP/seed/meta-data" "$SEED/meta-data"
cp -f "$TMP/seed/network-config" "$SEED/network-config"
# Some images also probe nocloud-net.
cp -f "$TMP/seed/user-data" "$SEED_NET/user-data"
cp -f "$TMP/seed/meta-data" "$SEED_NET/meta-data"
cp -f "$TMP/seed/network-config" "$SEED_NET/network-config"

printf 'datasource_list: [ NoCloud, None ]\n' > "$TMP/mnt/etc/cloud/cloud.cfg.d/90_copper_datasource.cfg"

# Netplan drop-in (applies even when cloud-init network stage is skipped).
cp -f "$TMP/seed/99-copper.yaml" "$TMP/mnt/etc/netplan/99-copper.yaml"
chmod 600 "$TMP/mnt/etc/netplan/99-copper.yaml"

# Stamp image identity for retry skip-write (matches data-media X-Copper-SHA256).
if [ -n "$EXPECT_SHA" ]; then
  printf '%%s\n' "$EXPECT_SHA" > "$TMP/mnt/etc/copper-image.sha256"
  echo "STAMP_WRITTEN /etc/copper-image.sha256 sha=$EXPECT_SHA size=$EXPECT_SIZE"
fi

# Hard verify — refuse CIDATA_OK unless network-config is on the written root.
for path in \
  "$SEED/user-data" \
  "$SEED/meta-data" \
  "$SEED/network-config" \
  "$TMP/mnt/etc/netplan/99-copper.yaml"
do
  if [ ! -s "$path" ]; then
    echo "ERROR: missing or empty after write: $path" >&2
    ls -la "$SEED" "$TMP/mnt/etc/netplan" 2>&1 || true
    exit 1
  fi
done

echo "--- verify on-disk network-config ---"
wc -c "$SEED/network-config" "$TMP/mnt/etc/netplan/99-copper.yaml"
echo "--- $SEED ---"
ls -la "$SEED"
echo "--- contents $SEED/network-config ---"
cat "$SEED/network-config"
echo "--- contents /etc/netplan/99-copper.yaml ---"
cat "$TMP/mnt/etc/netplan/99-copper.yaml"

{
  echo "copper provisioned"
  echo "seed=$SEED"
  echo "netplan=/etc/netplan/99-copper.yaml"
  [ -n "$EXPECT_SHA" ] && echo "image_sha256=$EXPECT_SHA"
  [ -n "$EXPECT_SIZE" ] && [ "$EXPECT_SIZE" != "0" ] && echo "image_size=$EXPECT_SIZE"
} > "$TMP/mnt/etc/copper-provisioned"
sync
umount "$TMP/mnt"
echo "CIDATA_OK root=$ROOT network-config=ok cleaned=1"
`, storage.ShellQuote(disk),
		storage.ShellQuote(strings.ToLower(strings.TrimSpace(imageSHA))),
		storage.ShellQuote(fmt.Sprintf("%d", imageSize)),
		storage.ShellQuote(b64(userData)), storage.ShellQuote(b64(metaData)),
		storage.ShellQuote(b64(networkConfig)), storage.ShellQuote(b64(netplanConfig)))
}

func b64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func trimTail(s string, maxLines int) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.TrimSpace(s)
	if s == "" || maxLines <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	return strings.Join(lines[len(lines)-maxLines:], "\n")
}
