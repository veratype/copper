package provision

import (
	"fmt"

	"copper_control_plane/internal/storage"
)

// buildReidentifyScript makes the written cloud image unique on this machine.
//
// Raw Ubuntu cloud images ship fixed GPT PARTUUIDs. Writing the same image to
// several disks leaves identical HD(…,GPT,<uuid>) NVRAM nodes and identical
// grub/fstab PARTUUID= lines, so firmware/grub can boot a twin. After GPT
// relocate and before cidata/efiboot we only need sgdisk -G (new disk +
// partition GUIDs) plus rewriting PARTUUID refs.
func buildReidentifyScript(disk string) string {
	return remotePrelude() + fmt.Sprintf(`
set -u
DEV=%s
log() { printf '%%s\n' "$1"; }

SGDISK_BIN=$(copper_bin sgdisk || true)
log "REIDENTIFY_BEGIN dev=$DEV"
log "sgdisk=${SGDISK_BIN:-MISSING}"

if [ -z "$SGDISK_BIN" ]; then
  log "ERROR: sgdisk required to randomize GPT disk/partition GUIDs (-G)"
  exit 30
fi

partprobe "$DEV" 2>/dev/null || true
sleep 1
log "--- partitions before ---"
lsblk -ln -o NAME,TYPE,SIZE,FSTYPE,PARTTYPE,LABEL,PARTUUID "$DEV" 2>&1 || true

# Derive ESP / root / boot from PARTTYPE + FSTYPE + LABEL (never hard-code p1/p15/p16).
ESP_GUID="c12a7328-f81f-11d2-ba4b-00a0c93ec93b"
BOOT_GUID="bc13c2ff-59e6-4262-a352-b275fd6f7172"
LINUX_GUID="0fc63daf-8483-4772-8e79-3d69d8477de4"
ESP_PART=""
ROOT_PART=""
BOOT_PART=""

while read -r name typ fstype parttype; do
  [ "$typ" = "part" ] || continue
  path="/dev/$name"
  case "$path" in
    "$DEV"*) ;;
    *) continue ;;
  esac
  pt=$(printf '%%s' "$parttype" | tr 'A-Z' 'a-z')
  lab=$(lsblk -ln -o LABEL "$path" 2>/dev/null | head -n1 | tr -d '\r')
  lab_lc=$(printf '%%s' "$lab" | tr 'A-Z' 'a-z')

  if [ "$pt" = "$ESP_GUID" ]; then
    ESP_PART="$path"
    continue
  fi
  if [ "$pt" = "$BOOT_GUID" ] || [ "$lab_lc" = "boot" ]; then
    BOOT_PART="$path"
    continue
  fi
  if [ "$lab_lc" = "cloudimg-rootfs" ] || [ "$pt" = "$LINUX_GUID" ]; then
    if [ "$fstype" = "ext4" ] || [ "$fstype" = "ext3" ] || [ "$fstype" = "ext2" ]; then
      ROOT_PART="$path"
      continue
    fi
  fi
done < <(lsblk -ln -o NAME,TYPE,FSTYPE,PARTTYPE "$DEV" 2>/dev/null)

# Fallback root: first ext* partition on $DEV that mounts with /etc.
if [ -z "$ROOT_PART" ]; then
  TMP_PROBE=$(mktemp -d)
  while read -r name typ fstype; do
    [ "$typ" = "part" ] || continue
    case "$fstype" in ext2|ext3|ext4) ;; *) continue ;; esac
    path="/dev/$name"
    case "$path" in
      "$DEV"*) ;;
      *) continue ;;
    esac
    [ "$path" = "$BOOT_PART" ] && continue
    if mount -o ro "$path" "$TMP_PROBE" 2>/dev/null; then
      if [ -d "$TMP_PROBE/etc" ]; then
        ROOT_PART="$path"
        umount "$TMP_PROBE" 2>/dev/null || true
        break
      fi
      umount "$TMP_PROBE" 2>/dev/null || true
    fi
  done < <(lsblk -ln -o NAME,TYPE,FSTYPE "$DEV" 2>/dev/null)
  rm -rf "$TMP_PROBE"
fi

# Fallback ESP: vfat with /EFI.
if [ -z "$ESP_PART" ]; then
  TMP_PROBE=$(mktemp -d)
  while read -r name typ fstype; do
    [ "$typ" = "part" ] || continue
    [ "$fstype" = "vfat" ] || [ "$fstype" = "fat32" ] || continue
    path="/dev/$name"
    case "$path" in
      "$DEV"*) ;;
      *) continue ;;
    esac
    if mount -o ro "$path" "$TMP_PROBE" 2>/dev/null; then
      if [ -d "$TMP_PROBE/EFI" ] || [ -d "$TMP_PROBE/efi" ]; then
        ESP_PART="$path"
        umount "$TMP_PROBE" 2>/dev/null || true
        break
      fi
      umount "$TMP_PROBE" 2>/dev/null || true
    fi
  done < <(lsblk -ln -o NAME,TYPE,FSTYPE "$DEV" 2>/dev/null)
  rm -rf "$TMP_PROBE"
fi

if [ -z "$ROOT_PART" ]; then
  log "ERROR: could not find root filesystem on $DEV"
  exit 32
fi
if [ -z "$ESP_PART" ]; then
  log "ERROR: could not find ESP on $DEV"
  exit 33
fi

log "ROOT_PART=$ROOT_PART BOOT_PART=${BOOT_PART:-none} ESP_PART=$ESP_PART"

# Capture PARTUUIDs *before* -G so we can rewrite grub/fstab.
OLD_ROOT_PU=$(blkid -s PARTUUID -o value "$ROOT_PART" 2>/dev/null || true)
OLD_BOOT_PU=""
OLD_ESP_PU=$(blkid -s PARTUUID -o value "$ESP_PART" 2>/dev/null || true)
if [ -n "$BOOT_PART" ]; then
  OLD_BOOT_PU=$(blkid -s PARTUUID -o value "$BOOT_PART" 2>/dev/null || true)
fi
log "OLD root_pu=$OLD_ROOT_PU boot_pu=$OLD_BOOT_PU esp_pu=$OLD_ESP_PU"

log "SGDISK_G $SGDISK_BIN -G $DEV"
set +e
"$SGDISK_BIN" -G "$DEV" 2>&1
st=$?
set -e
log "SGDISK_G_EXIT=$st"
if [ "$st" != "0" ]; then
  log "ERROR: sgdisk -G failed"
  exit 34
fi
partprobe "$DEV" 2>&1 || true
sleep 1

NEW_ROOT_PU=$(blkid -s PARTUUID -o value "$ROOT_PART" 2>/dev/null || true)
NEW_BOOT_PU=""
NEW_ESP_PU=$(blkid -s PARTUUID -o value "$ESP_PART" 2>/dev/null || true)
if [ -n "$BOOT_PART" ]; then
  NEW_BOOT_PU=$(blkid -s PARTUUID -o value "$BOOT_PART" 2>/dev/null || true)
fi
log "NEW root_pu=$NEW_ROOT_PU boot_pu=$NEW_BOOT_PU esp_pu=$NEW_ESP_PU"

if [ -z "$NEW_ROOT_PU" ] || [ -z "$NEW_ESP_PU" ]; then
  log "ERROR: missing new PARTUUID values after sgdisk -G"
  exit 36
fi
if [ -n "$OLD_ROOT_PU" ] && [ "$OLD_ROOT_PU" = "$NEW_ROOT_PU" ]; then
  log "ERROR: root PARTUUID unchanged after sgdisk -G"
  exit 37
fi

MNT=$(mktemp -d)
cleanup() { umount -R "$MNT" 2>/dev/null || true; rm -rf "$MNT"; }
trap cleanup EXIT

log "MOUNT root=$ROOT_PART -> $MNT"
mount "$ROOT_PART" "$MNT"
if [ -n "$BOOT_PART" ]; then
  mkdir -p "$MNT/boot"
  log "MOUNT boot=$BOOT_PART -> $MNT/boot"
  mount "$BOOT_PART" "$MNT/boot"
fi
mkdir -p "$MNT/boot/efi"
log "MOUNT esp=$ESP_PART -> $MNT/boot/efi"
mount "$ESP_PART" "$MNT/boot/efi"

# Replace old PARTUUIDs in grub/config snippets (only files that exist).
copper_subst() {
  _old="$1"; _new="$2"; _file="$3"
  [ -n "$_old" ] && [ -n "$_new" ] && [ -f "$_file" ] || return 0
  [ "$_old" = "$_new" ] && return 0
  sed -i "s/${_old}/${_new}/g" "$_file"
  log "PATCH $_file : ${_old} -> ${_new}"
}

for f in \
  "$MNT/boot/grub/grub.cfg" \
  "$MNT/boot/efi/EFI/ubuntu/grub.cfg" \
  "$MNT/etc/default/grub.d/40-force-partuuid.cfg"
do
  [ -f "$f" ] || continue
  log "--- patching $f ---"
  copper_subst "$OLD_ROOT_PU" "$NEW_ROOT_PU" "$f"
  copper_subst "$OLD_BOOT_PU" "$NEW_BOOT_PU" "$f"
  copper_subst "$OLD_ESP_PU" "$NEW_ESP_PU" "$f"
done

log "WRITE fstab (PARTUUID only)"
{
  printf 'PARTUUID=%%s / ext4 discard,errors=remount-ro 0 1\n' "$NEW_ROOT_PU"
  if [ -n "$BOOT_PART" ] && [ -n "$NEW_BOOT_PU" ]; then
    printf 'PARTUUID=%%s /boot ext4 defaults 0 2\n' "$NEW_BOOT_PU"
  fi
  printf 'PARTUUID=%%s /boot/efi vfat umask=0077 0 1\n' "$NEW_ESP_PU"
} > "$MNT/etc/fstab"
log "--- /etc/fstab ---"
cat "$MNT/etc/fstab"

sync
umount -R "$MNT" 2>/dev/null || true
trap - EXIT
rm -rf "$MNT"

log "--- partitions after ---"
lsblk -ln -o NAME,TYPE,SIZE,FSTYPE,PARTTYPE,LABEL,PARTUUID "$DEV" 2>&1 || true
log "REIDENTIFY_OK root=$ROOT_PART esp=$ESP_PART boot=${BOOT_PART:-none} root_pu=$NEW_ROOT_PU esp_pu=$NEW_ESP_PU"
exit 0
`, storage.ShellQuote(disk))
}
