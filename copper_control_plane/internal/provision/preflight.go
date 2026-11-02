package provision

import (
	"context"
	"fmt"
	"strings"

	"copper_control_plane/internal/storage"
)

// Preflight checks live-OS reachability, target disk, image URL, and required tools
// without writing anything. Used as a batch gate before any host starts downloading.
func Preflight(ctx context.Context, h storage.Host, req Request, progress ProgressFunc) error {
	if progress == nil {
		progress = func(string, string) {}
	}
	req.ImageURL = strings.TrimSpace(req.ImageURL)
	req.TargetDisk = strings.TrimSpace(req.TargetDisk)
	if err := req.Validate(); err != nil {
		return err
	}

	progress("validate", fmt.Sprintf("host=%s disk=%s image=%s", h.IP, req.TargetDisk, req.ImageURL))

	check := remotePrelude() + fmt.Sprintf(`
set -u
echo "CHECK_BEGIN"
echo "PATH=$PATH"
echo "whoami=$(whoami) id=$(id)"
echo "sudo_n=$(sudo -n true 2>&1 && echo ok || echo FAIL)"
echo "curl=$(copper_bin curl || echo MISSING)"
echo "dd=$(copper_bin dd || echo MISSING)"
echo "qemu-img=$(copper_bin qemu-img || echo MISSING)"
echo "sgdisk=$(copper_bin sgdisk || echo MISSING)"
echo "gdisk=$(copper_bin gdisk || echo MISSING)"
echo "efibootmgr=$(copper_bin efibootmgr || echo MISSING)"
echo "mdadm=$(copper_bin mdadm || echo MISSING)"
lsblk -dn -o NAME,SIZE,TYPE,TRAN,MODEL %s 2>&1 || true
test -b %s && echo "BLOCK_OK" || echo "BLOCK_MISSING"
DEV=%s
case "$DEV" in
  /dev/md*)
    if ! copper_bin mdadm >/dev/null; then
      echo "ERROR: mdadm required on live OS when target is a software RAID device"
      exit 1
    fi
    MD_BASE=$(basename "$DEV")
    if ! grep -E "(^$MD_BASE | $MD_BASE :)" /proc/mdstat >/dev/null 2>&1; then
      echo "WARN: $DEV not listed in /proc/mdstat — assemble Soft RAID before Deploy"
    fi
    ;;
esac
if ! sudo -n true 2>/dev/null; then
  echo "ERROR: passwordless sudo required on live OS"
  exit 1
fi
if ! copper_bin curl >/dev/null; then
  echo "ERROR: curl missing on live OS"
  exit 1
fi
if ! copper_bin dd >/dev/null; then
  echo "ERROR: dd missing on live OS"
  exit 1
fi
if ! copper_bin sgdisk >/dev/null; then
  echo "ERROR: sgdisk required (GPT relocate -e and re-identify -G)"
  echo "HINT: package is usually 'gdisk'; binary is /usr/sbin/sgdisk"
  exit 1
fi
if ! copper_bin efibootmgr >/dev/null; then
  echo "WARN: efibootmgr missing — UEFI BootOrder will not be pinned (old OS on another disk may win)"
fi
echo "CHECK_OK"
`, storage.ShellQuote(req.TargetDisk), storage.ShellQuote(req.TargetDisk), storage.ShellQuote(req.TargetDisk))
	out, err := storage.RunSSHRetry(ctx, h.IP, h.Creds, "bash -lc "+storage.ShellQuote(check), 3)
	logLines(progress, "validate", out)
	if err != nil {
		return fmt.Errorf("target disk check failed: %w", err)
	}
	if !strings.Contains(out, "BLOCK_OK") {
		if strings.TrimSpace(out) == "" {
			return fmt.Errorf("target disk check returned empty SSH output (not a confirmed missing disk)")
		}
		if strings.Contains(out, "BLOCK_MISSING") {
			return fmt.Errorf("target disk %s is not a block device", req.TargetDisk)
		}
		return fmt.Errorf("target disk %s check incomplete (no BLOCK_OK)\n--- remote ---\n%s", req.TargetDisk, trimTail(out, 40))
	}
	if !strings.Contains(out, "CHECK_OK") {
		return fmt.Errorf("live OS pre-check failed\n--- remote ---\n%s", trimTail(out, 40))
	}

	progress("preflight", "HTTP HEAD/GET probe of image URL from live host")
	preflight := remotePrelude() + fmt.Sprintf(`
set -u
URL=%s
CURL_BIN=$(copper_bin curl || true)
QEMU_BIN=$(copper_bin qemu-img || true)
echo "PREFLIGHT_BEGIN url=$URL"
echo "curl=$CURL_BIN qemu-img=$QEMU_BIN dd=$(copper_bin dd || true)"
if [ -z "$CURL_BIN" ]; then
  echo "ERROR: curl missing on live OS"
  exit 1
fi
echo "--- curl -I ---"
"$CURL_BIN" -sS -I -L --max-time 30 -o /tmp/copper-preflight.hdr -w "http_code=%%{http_code} size=%%{size_download} redirect=%%{url_effective}\n" "$URL" 2>&1 || echo "curl_I_exit=$?"
head -n 40 /tmp/copper-preflight.hdr 2>/dev/null || true
echo "--- curl first 16 bytes ---"
"$CURL_BIN" -sS -L --max-time 60 -r 0-15 -o /tmp/copper-preflight.bin -w "http_code=%%{http_code} bytes=%%{size_download}\n" "$URL" 2>&1 || echo "curl_range_exit=$?"
ls -la /tmp/copper-preflight.bin 2>&1 || true
MAGIC=$(od -An -tx1 -N 4 /tmp/copper-preflight.bin 2>/dev/null | tr -d ' \n')
echo "IMG_MAGIC_HEX=$MAGIC"
case "$MAGIC" in
  514649fb|514649FB)
    echo "IMG_FORMAT=qcow2"
    if [ -z "$QEMU_BIN" ]; then
      echo "ERROR: image is qcow2 but qemu-img is missing on live OS"
      exit 1
    fi
    ;;
  "")
    echo "ERROR: could not read image magic (URL unreachable or empty)"
    exit 1
    ;;
  *)
    echo "IMG_FORMAT=raw-or-unknown"
    ;;
esac
echo "PREFLIGHT_OK"
echo "PREFLIGHT_END"
`, storage.ShellQuote(req.ImageURL))
	out, err = storage.RunSSHRetry(ctx, h.IP, h.Creds, "bash -lc "+storage.ShellQuote(preflight), 3)
	logLines(progress, "preflight", out)
	if err != nil {
		return fmt.Errorf("image preflight failed: %w", err)
	}
	if !strings.Contains(out, "PREFLIGHT_OK") {
		tail := trimTail(out, 40)
		if strings.TrimSpace(tail) == "" {
			tail = "(empty remote output — SSH flake)"
		}
		return fmt.Errorf("image preflight did not confirm success\n--- remote ---\n%s", tail)
	}
	return nil
}
