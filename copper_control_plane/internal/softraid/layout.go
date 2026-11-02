package softraid

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"copper_control_plane/internal/storage"
)

// LayoutFromTopology builds a Deploy-ready layout from a Soft RAID scan.
// Arrays become preferred install targets (source=md).
func LayoutFromTopology(top Topology, dataIP string) storage.Layout {
	targets := SuggestTargets(top)
	return storage.Layout{
		OK:          top.OK && len(targets) > 0,
		Mode:        "soft_raid",
		Family:      storage.Family("mdadm"),
		Tool:        top.Tool,
		Model:       "mdadm",
		Fingerprint:      Fingerprint(top),      // per-host (WWN / md UUID)
		BatchFingerprint: BatchFingerprint(top), // cross-host Deploy / Soft RAID batch
		Targets:          targets,
		DataIP:      dataIP,
		SavedAt:     time.Now().UTC(),
		Error: func() string {
			if !top.OK {
				return top.Error
			}
			if len(targets) == 0 {
				return "no md arrays — create one before Deploy"
			}
			return ""
		}(),
	}
}

// SuggestTargets lists md arrays as OS install candidates.
func SuggestTargets(top Topology) []storage.InstallTarget {
	var out []storage.InstallTarget
	arrays := append([]Array(nil), top.Arrays...)
	sort.Slice(arrays, func(i, j int) bool { return arrays[i].Name < arrays[j].Name })
	for _, a := range arrays {
		label := fmt.Sprintf("%s · %s · %s", a.Path, a.Level, a.State)
		if a.Size != "" {
			label += " · " + a.Size
		}
		if len(a.Members) > 0 {
			var mem []string
			for _, m := range a.Members {
				mem = append(mem, m.Device)
			}
			label += " · " + strings.Join(mem, ",")
		}
		// Key is md:<name> so batch Deploy can pick the same role on every host.
		// UUID stays in DeviceID for resolve when names reshuffle.
		out = append(out, storage.InstallTarget{
			Device:   a.Path,
			DeviceID: a.UUID,
			Key:      "md:" + a.Name,
			Label:    label,
			Source:   "md",
			RAID:     a.Level,
			Size:     a.Size,
		})
	}
	return out
}

// Fingerprint is a per-host Deploy key (includes WWN/UUID — not for cross-host batch).
func Fingerprint(top Topology) string {
	arrays := append([]Array(nil), top.Arrays...)
	sort.Slice(arrays, func(i, j int) bool { return arrays[i].Name < arrays[j].Name })
	aparts := make([]string, 0, len(arrays))
	for _, a := range arrays {
		mem := make([]string, 0, len(a.Members))
		for _, m := range a.Members {
			mem = append(mem, m.Device+":"+m.Role)
		}
		sort.Strings(mem)
		aparts = append(aparts, fmt.Sprintf("%s/%s/%s/%s", a.Name, a.Level, a.UUID, strings.Join(mem, ",")))
	}
	disks := append([]Disk(nil), top.Disks...)
	sort.Slice(disks, func(i, j int) bool { return disks[i].Path < disks[j].Path })
	dparts := make([]string, 0, len(disks))
	for _, d := range disks {
		id := d.WWN
		if id == "" {
			id = d.Serial
		}
		if id == "" {
			id = d.Name
		}
		dparts = append(dparts, fmt.Sprintf("%s:%s:%s", id, d.Size, d.State))
	}
	return "soft_raid|" + strings.Join(dparts, ";") + "|" + strings.Join(aparts, ";")
}

// BatchFingerprint compares Soft RAID shape across hosts for batch create/exec.
// It ignores WWN/serial/UUID/path letters so identical chassis layouts match.
func BatchFingerprint(top Topology) string {
	disks := append([]Disk(nil), top.Disks...)
	sort.Slice(disks, func(i, j int) bool {
		if disks[i].Size != disks[j].Size {
			return disks[i].Size < disks[j].Size
		}
		if disks[i].State != disks[j].State {
			return disks[i].State < disks[j].State
		}
		return disks[i].Tran < disks[j].Tran
	})
	dparts := make([]string, 0, len(disks))
	for _, d := range disks {
		dparts = append(dparts, fmt.Sprintf("%s:%s:%s", d.Size, d.State, d.Tran))
	}
	arrays := append([]Array(nil), top.Arrays...)
	sort.Slice(arrays, func(i, j int) bool {
		if arrays[i].Level != arrays[j].Level {
			return arrays[i].Level < arrays[j].Level
		}
		if arrays[i].RaidDevices != arrays[j].RaidDevices {
			return arrays[i].RaidDevices < arrays[j].RaidDevices
		}
		return arrays[i].Name < arrays[j].Name
	})
	aparts := make([]string, 0, len(arrays))
	for _, a := range arrays {
		roles := make([]string, 0, len(a.Members))
		for _, m := range a.Members {
			roles = append(roles, m.Role)
		}
		sort.Strings(roles)
		aparts = append(aparts, fmt.Sprintf("%s/%d/%d/%s/%s",
			a.Level, a.RaidDevices, len(a.Members), a.Metadata, strings.Join(roles, ",")))
	}
	return "soft_raid_batch|" + strings.Join(dparts, ";") + "|" + strings.Join(aparts, ";")
}
