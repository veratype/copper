package storage

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// LayoutSnapshotKind is the store.snapshots kind for persisted HDD layout used by deploy.
const LayoutSnapshotKind = "storage_layout"

// InstallTarget is a block device suggested for OS install from the saved layout.
type InstallTarget struct {
	Device   string `json:"device"`                     // e.g. /dev/sda (volatile — prefer DeviceID)
	DeviceID string `json:"device_id,omitempty"`        // stable WWN / Unique Identifier / NAA
	Key      string `json:"key,omitempty"`              // stable role: array:A:1 / drive:1I:1:3 / disk:name
	Label    string `json:"label,omitempty"`            // human: "array A LD 1" / "bay 3"
	Source   string `json:"source,omitempty"`           // array | drive | disk
	RAID     string `json:"raid,omitempty"`
	Size     string `json:"size,omitempty"`
}

// Layout is the persisted post-RAID (or non-RAID) picture for provisioning.
type Layout struct {
	OK          bool            `json:"ok"`
	Mode        string          `json:"mode"` // raid | direct
	Family      Family          `json:"family,omitempty"`
	Tool        string          `json:"tool,omitempty"`
	Model       string          `json:"model,omitempty"`
	Slot        int             `json:"slot,omitempty"`
	Fingerprint string          `json:"fingerprint"` // per-host identity (WWN / md UUID / …)
	// BatchFingerprint is the cross-host match key for Deploy batch.
	// Soft RAID sets a structural value; hardware/direct usually leave it empty
	// and LayoutsMatch falls back to Fingerprint.
	BatchFingerprint string          `json:"batch_fingerprint,omitempty"`
	Controller       *Controller     `json:"controller,omitempty"`
	Targets          []InstallTarget `json:"targets"`
	DataIP           string          `json:"data_ip,omitempty"`
	Error            string          `json:"error,omitempty"`
	SavedAt          time.Time       `json:"saved_at"`
}

// LayoutFromController builds a Layout from a live controller config.
func LayoutFromController(c Controller, dataIP string) Layout {
	c.Normalize()
	targets := SuggestTargets(c)
	return Layout{
		OK:          true,
		Mode:        "raid",
		Family:      c.Family,
		Tool:        c.Tool,
		Model:       c.Model,
		Slot:        c.Slot,
		Fingerprint: LayoutFingerprint(c),
		Controller:  &c,
		Targets:     targets,
		DataIP:      dataIP,
		SavedAt:     time.Now().UTC(),
	}
}

// LayoutFromDisks builds a direct (non-RAID) layout from lsblk-style disks.
func LayoutFromDisks(disks []BlockDisk, dataIP string) Layout {
	targets := make([]InstallTarget, 0, len(disks))
	parts := make([]string, 0, len(disks))
	for _, d := range disks {
		if d.Name == "" || d.Type != "disk" {
			continue
		}
		dev := d.Path
		if dev == "" {
			dev = "/dev/" + d.Name
		}
		targets = append(targets, InstallTarget{
			Device:   dev,
			DeviceID: strings.TrimSpace(d.WWN),
			Key:      fmt.Sprintf("disk:%s", d.Name),
			Label:    d.Name + " " + d.Size,
			Source:   "disk",
			Size:     d.Size,
		})
		parts = append(parts, fmt.Sprintf("%s:%s:%s", d.Name, d.Size, d.Tran))
	}
	sort.Strings(parts)
	return Layout{
		OK:          len(targets) > 0,
		Mode:        "direct",
		Family:      FamilyNone,
		Fingerprint: "direct|" + strings.Join(parts, ";"),
		Targets:     targets,
		DataIP:      dataIP,
		SavedAt:     time.Now().UTC(),
		Error: func() string {
			if len(targets) == 0 {
				return "no disks found"
			}
			return ""
		}(),
	}
}

// SuggestTargets picks install candidates from controller arrays / unassigned drives.
// Labels match the Storage console (Array A · LD 1 … / 1I:1:3 · bay 3 …).
func SuggestTargets(c Controller) []InstallTarget {
	inArray := map[string]bool{}
	for _, a := range c.Arrays {
		for _, addr := range a.Drives {
			inArray[addr] = true
		}
	}
	var out []InstallTarget
	// Arrays first — preferred OS install targets after RAID.
	for _, a := range c.Arrays {
		dev := ""
		if a.LD.Disk != nil {
			dev = strings.TrimSpace(*a.LD.Disk)
		}
		label := fmt.Sprintf("Array %s · LD %d · RAID %s · %s", a.Name, a.LD.ID, a.LD.RAID, a.LD.Size)
		if len(a.Drives) > 0 {
			label += " · " + strings.Join(a.Drives, ",")
		}
		if dev != "" {
			label += " · " + dev
		} else {
			label += " · (no /dev yet — rescan)"
		}
		out = append(out, InstallTarget{
			Device:   dev,
			DeviceID: strings.TrimSpace(a.LD.WWN),
			Key:      fmt.Sprintf("array:%s:%d", a.Name, a.LD.ID),
			Label:    label,
			Source:   "array",
			RAID:     a.LD.RAID,
			Size:     a.LD.Size,
		})
	}
	// Unassigned / raw drives only (members of arrays are not install targets).
	for _, d := range c.Drives {
		if inArray[d.Addr] {
			continue
		}
		dev := ""
		if d.Disk != nil {
			dev = strings.TrimSpace(*d.Disk)
		}
		label := fmt.Sprintf("%s · bay %d · %s", d.Addr, d.Bay, d.Size)
		if d.Type != "" {
			label += " · " + d.Type
		}
		if dev != "" {
			label += " · " + dev
		}
		out = append(out, InstallTarget{
			Device:   dev,
			DeviceID: strings.TrimSpace(d.WWN),
			Key:      fmt.Sprintf("drive:%s", d.Addr),
			Label:    label,
			Source:   "drive",
			Size:     d.Size,
		})
	}
	return out
}

// LayoutFingerprint matches the storage console batch fingerprint (family+slot+drives+arrays).
func LayoutFingerprint(c Controller) string {
	drives := append([]Drive(nil), c.Drives...)
	sort.Slice(drives, func(i, j int) bool {
		return drives[i].Addr < drives[j].Addr
	})
	dparts := make([]string, 0, len(drives))
	for _, d := range drives {
		dparts = append(dparts, fmt.Sprintf("%s:%d:%s:%d:%s",
			d.Addr, d.Bay, d.Size, int(d.GB+0.5), d.Type))
	}
	arrays := append([]Array(nil), c.Arrays...)
	sort.Slice(arrays, func(i, j int) bool {
		return arrays[i].Name < arrays[j].Name
	})
	aparts := make([]string, 0, len(arrays))
	for _, a := range arrays {
		ds := append([]string(nil), a.Drives...)
		sort.Strings(ds)
		aparts = append(aparts, fmt.Sprintf("%s/%s/%s", a.Name, a.LD.RAID, strings.Join(ds, ",")))
	}
	return fmt.Sprintf("%s|slot=%d|%s|%s", c.Family, c.Slot, strings.Join(dparts, ";"), strings.Join(aparts, ";"))
}

// batchKey picks the fingerprint used for cross-host Deploy batch matching.
func batchKey(l Layout) string {
	if strings.TrimSpace(l.BatchFingerprint) != "" {
		return l.BatchFingerprint
	}
	return l.Fingerprint
}

// LayoutsMatch reports whether two layouts may be batch-provisioned together.
func LayoutsMatch(a, b Layout) bool {
	afp, bfp := batchKey(a), batchKey(b)
	if afp == "" || bfp == "" {
		return false
	}
	return afp == bfp
}
