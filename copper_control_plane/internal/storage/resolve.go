package storage

import (
	"context"
	"strconv"
	"strings"
)

// NormalizeDiskID canonicalizes WWN / Unique Identifier / SCSI NAA for comparison.
func NormalizeDiskID(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimPrefix(s, "0x")
	s = strings.ReplaceAll(s, ":", "")
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, " ", "")
	// lsblk NVMe often uses eui.XXXX — keep the hex body comparable.
	s = strings.TrimPrefix(s, "eui.")
	return s
}

func blockDiskPath(d BlockDisk) string {
	if p := strings.TrimSpace(d.Path); p != "" {
		return p
	}
	if d.Name != "" {
		return "/dev/" + d.Name
	}
	return ""
}

// PathForDiskID returns the live /dev path for a stable disk id (WWN/NAA/serial).
func PathForDiskID(disks []BlockDisk, id string) (string, bool) {
	want := NormalizeDiskID(id)
	if want == "" {
		return "", false
	}
	for _, d := range disks {
		if NormalizeDiskID(d.WWN) == want || NormalizeDiskID(d.Serial) == want {
			if p := blockDiskPath(d); p != "" {
				return p, true
			}
		}
	}
	return "", false
}

func livePathSet(disks []BlockDisk) map[string]bool {
	out := map[string]bool{}
	for _, d := range disks {
		if p := blockDiskPath(d); p != "" {
			out[p] = true
		}
	}
	return out
}

func isLogicalVolumeModel(model string) bool {
	m := strings.ToUpper(strings.TrimSpace(model))
	if m == "" {
		return false
	}
	// Smart Array LDs commonly show as "LOGICAL VOLUME".
	if strings.Contains(m, "LOGICAL") || strings.Contains(m, "VIRTUAL") || strings.Contains(m, "VOLUME") {
		return true
	}
	// MegaRAID / PERC VDs often stamp the controller model on the block device
	// (e.g. "MR9560-16i", "PERC H755") instead of "LOGICAL VOLUME".
	if strings.Contains(m, "MEGARAID") || strings.Contains(m, "PERC") || strings.Contains(m, "AVAGO") ||
		strings.Contains(m, "BROADCOM") || strings.Contains(m, "LSI") {
		return true
	}
	if strings.HasPrefix(m, "MR") || strings.Contains(m, "RAID") {
		return true
	}
	return false
}

// diskLooksNVMe reports an NVMe namespace (other controller / onboard), not a
// MegaRAID/Smart Array SCSI virtual drive.
func diskLooksNVMe(d BlockDisk) bool {
	if strings.EqualFold(strings.TrimSpace(d.Tran), "nvme") {
		return true
	}
	name := strings.ToLower(strings.TrimSpace(d.Name))
	path := strings.ToLower(blockDiskPath(d))
	if strings.HasPrefix(name, "nvme") || strings.Contains(path, "/nvme") {
		return true
	}
	w := strings.ToLower(strings.TrimSpace(d.WWN))
	return strings.HasPrefix(w, "eui.")
}

// BindControllerDisks rewrites volume/PD Disk pointers from live block devices.
//
// MegaRAID VDs bind via SCSI NAA (and optional OS Drive Name); Smart Array LDs
// bind via Unique Identifier (and Disk Name). Prefer those stable ids; keep a
// vendor path only when it still exists and is not claimed. Array-member PDs
// are cleared (not OS install targets; stale /dev names poison mapping).
func BindControllerDisks(c *Controller, disks []BlockDisk) {
	if c == nil {
		return
	}
	live := livePathSet(disks)
	claimed := map[string]bool{} // paths owned by WWN-resolved LDs

	inArray := map[string]bool{}
	for _, a := range c.Arrays {
		for _, addr := range a.Drives {
			inArray[addr] = true
		}
	}

	byPath := map[string]BlockDisk{}
	for _, d := range disks {
		if p := blockDiskPath(d); p != "" {
			byPath[p] = d
		}
	}

	rejectNVMeGuess := c.Family == FamilyMegaRAID || c.Family == FamilySmartArray

	// 1) Volumes (MegaRAID VD / Smart Array LD): stable id → live path wins.
	for i := range c.Arrays {
		vol := &c.Arrays[i].LD
		if path, ok := PathForDiskID(disks, vol.WWN); ok {
			// NAA / Unique Identifier match is authoritative.
			p := path
			vol.Disk = &p
			claimed[path] = true
			continue
		}
		if vol.Disk != nil {
			p := strings.TrimSpace(*vol.Disk)
			if p == "" || !live[p] || claimed[p] {
				vol.Disk = nil
				continue
			}
			// Never keep a stale NVMe path on a MegaRAID VD / Smart Array LD without id proof.
			if rejectNVMeGuess {
				if bd, ok := byPath[p]; ok && diskLooksNVMe(bd) {
					vol.Disk = nil
					continue
				}
			}
			// Stale vendor Disk/OS Drive Name after create often points at a leftover
			// physical /dev letter while the real volume is elsewhere in lsblk.
			if bd, ok := byPath[p]; ok && !isLogicalVolumeModel(bd.Model) {
				for _, d := range disks {
					dp := blockDiskPath(d)
					if dp == "" || claimed[dp] || dp == p {
						continue
					}
					if isLogicalVolumeModel(d.Model) {
						vol.Disk = nil
						break
					}
				}
			}
		}
	}

	// 2) Physical drives in arrays: drop OS paths (members are not block targets).
	for i := range c.Drives {
		if inArray[c.Drives[i].Addr] {
			c.Drives[i].Disk = nil
			continue
		}
		d := &c.Drives[i]
		if path, ok := PathForDiskID(disks, d.WWN); ok {
			if claimed[path] {
				d.Disk = nil
				continue
			}
			p := path
			d.Disk = &p
			claimed[path] = true
			continue
		}
		if d.Disk != nil {
			p := strings.TrimSpace(*d.Disk)
			if p == "" || !live[p] || claimed[p] {
				d.Disk = nil
			} else {
				claimed[p] = true
			}
		}
	}

	// 3) Last resort for volumes still missing a path (no NAA/UID / not visible yet).
	guessMissingLDDisks(c, disks, claimed)
}

// sizeBytesApprox converts a controller/lsblk size string to approximate bytes
// (decimal units, good enough for matching).
//
// Accepts both vendor forms ("400.000 MB", "838.33 GB") and lsblk forms
// ("400.0M", "838.4G"). A bare integer ≥ 1 MiB is treated as raw bytes — the
// old SizeStr path emitted those for sub-GiB disks and sizeGBLoose wrongly
// read them as gigabytes, breaking MegaRAID tiny-VD and Smart Array LD binds.
func sizeBytesApprox(s string) uint64 {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", ""))
	if s == "" {
		return 0
	}
	if looksRawByteCount(s) {
		v, err := strconv.ParseUint(s, 10, 64)
		if err == nil {
			return v
		}
	}
	gb := SizeGBLoose(s)
	if gb <= 0 {
		return 0
	}
	return uint64(gb * 1e9)
}

func looksRawByteCount(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	// ≥ 1 MiB: avoids treating a bare "900" (meant as GB elsewhere) as bytes.
	v, err := strconv.ParseUint(s, 10, 64)
	return err == nil && v >= (1<<20)
}

// sizesCompatible reports whether two sizes are close enough to be the same volume.
func sizesCompatible(want, got uint64) bool {
	if want == 0 || got == 0 {
		return false
	}
	var delta uint64
	if want > got {
		delta = want - got
	} else {
		delta = got - want
	}
	// 10% relative, but at least 64 MiB so tiny VDs (100–400 MB) still match lsblk.
	tol := want / 10
	const minTol = 64 << 20
	if tol < minTol {
		tol = minTol
	}
	return delta <= tol
}

func guessMissingLDDisks(c *Controller, disks []BlockDisk, claimed map[string]bool) {
	rejectNVMe := c.Family == FamilyMegaRAID || c.Family == FamilySmartArray

	var free []BlockDisk
	for _, d := range disks {
		p := blockDiskPath(d)
		if p == "" || claimed[p] {
			continue
		}
		if rejectNVMe && diskLooksNVMe(d) {
			continue
		}
		free = append(free, d)
	}
	if len(free) == 0 {
		return
	}
	for i := range c.Arrays {
		ld := &c.Arrays[i].LD
		if ld.Disk != nil && strings.TrimSpace(*ld.Disk) != "" {
			continue
		}
		wantBytes := sizeBytesApprox(ld.Size)
		if wantBytes == 0 {
			// Without a usable size (and no WWN above), do not invent a path —
			// guessing across multi-controller hosts maps MegaRAID VDs onto NVMe.
			continue
		}
		best := -1
		bestDelta := uint64(1 << 62)
		bestLogical := false
		for j, d := range free {
			p := blockDiskPath(d)
			if p == "" || claimed[p] {
				continue
			}
			gotBytes := sizeBytesApprox(d.Size)
			if !sizesCompatible(wantBytes, gotBytes) {
				continue
			}
			var delta uint64
			if wantBytes > gotBytes {
				delta = wantBytes - gotBytes
			} else {
				delta = gotBytes - wantBytes
			}
			logical := isLogicalVolumeModel(d.Model)
			switch {
			case best < 0:
				best, bestDelta, bestLogical = j, delta, logical
			case logical && !bestLogical:
				best, bestDelta, bestLogical = j, delta, true
			case logical == bestLogical && delta < bestDelta:
				best, bestDelta = j, delta
			}
		}
		if best < 0 {
			continue
		}
		path := blockDiskPath(free[best])
		p := path
		ld.Disk = &p
		claimed[path] = true
	}
}

// ResolveControllerOSDisks lists live disks and binds controller OS paths.
func ResolveControllerOSDisks(ctx context.Context, h Host, c *Controller) error {
	disks, err := ListBlockDisks(ctx, h)
	if err != nil {
		return err
	}
	BindControllerDisks(c, disks)
	return nil
}

// RefreshLayoutOSDisks rebinds a persisted RAID layout against live lsblk and
// rebuilds install targets. No-op for direct layouts or missing controller.
func RefreshLayoutOSDisks(lay *Layout, disks []BlockDisk) bool {
	if lay == nil || lay.Mode != "raid" || lay.Controller == nil {
		return false
	}
	before := SuggestTargets(*lay.Controller)
	BindControllerDisks(lay.Controller, disks)
	lay.Targets = SuggestTargets(*lay.Controller)
	lay.Fingerprint = LayoutFingerprint(*lay.Controller)
	lay.OK = true
	after := lay.Targets
	if len(before) != len(after) {
		return true
	}
	for i := range before {
		if before[i].Device != after[i].Device || before[i].DeviceID != after[i].DeviceID {
			return true
		}
	}
	return false
}

// FindTargetByDevice returns the install target whose Device matches path.
func FindTargetByDevice(lay Layout, device string) (InstallTarget, bool) {
	device = strings.TrimSpace(device)
	if device == "" {
		return InstallTarget{}, false
	}
	for _, t := range lay.Targets {
		if strings.TrimSpace(t.Device) == device {
			return t, true
		}
	}
	return InstallTarget{}, false
}

// FindTargetByKey returns the install target with the given stable key.
func FindTargetByKey(lay Layout, key string) (InstallTarget, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return InstallTarget{}, false
	}
	for _, t := range lay.Targets {
		if strings.TrimSpace(t.Key) == key {
			return t, true
		}
	}
	return InstallTarget{}, false
}

// ResolveInstallPath picks the live /dev path for an install target on this host.
// Prefers DeviceID → lsblk WWN; falls back to the persisted Device path only when
// that path still exists and is not an obviously foreign NVMe for a RAID layout.
func ResolveInstallPath(disks []BlockDisk, t InstallTarget) string {
	if path, ok := PathForDiskID(disks, t.DeviceID); ok {
		return path
	}
	dev := strings.TrimSpace(t.Device)
	if dev == "" || !livePathSet(disks)[dev] {
		return ""
	}
	for _, d := range disks {
		if blockDiskPath(d) != dev {
			continue
		}
		// Stale layout may still say /dev/nvme… for a MegaRAID VD — refuse it
		// when DeviceID did not confirm the match.
		if diskLooksNVMe(d) && t.DeviceID != "" {
			return ""
		}
		return dev
	}
	return dev
}
