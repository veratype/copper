// Package storage is the shared live-OS storage surface: SSH helpers, SoftRAID-
// friendly layout types, and the Driver interface.
//
// Hardware RAID (ssacli / storcli) lives in copper_control_plane/internal/hwraid
// and registers itself at init. Enterprise licensing is copper_control_plane/internal/license.
package storage

import "time"

// Family identifies which CLI / adapter to use after Detect.
type Family string

const (
	FamilySmartArray Family = "smartarray" // HPE ssacli
	FamilyMegaRAID   Family = "megaraid"   // Broadcom/LSI storcli (and Dell PERC when storcli works)
	FamilyNone       Family = "none"
	FamilyUnknown    Family = "unknown"
)

// Drive is one physical disk in the shared console model.
type Drive struct {
	Addr   string  `json:"addr"`
	Bay    int     `json:"bay"`
	Type   string  `json:"type"`
	Size   string  `json:"size"`
	GB     float64 `json:"gb"`
	Status string  `json:"status"`
	Disk   *string `json:"disk"` // /dev/sdX when passed through; nil when in an array
	// WWN is a stable OS identity (ssacli Unique Identifier / WWN, storcli NAA, …).
	// /dev/sdX letters reshuffle; bind paths from live lsblk using this id.
	WWN string `json:"wwn,omitempty"`
	// DG is the MegaRAID drive-group id ("0", "1", …). Empty for free/JBOD/Smart Array.
	// Used to list Onln members under Arrays and logical drives.
	DG string `json:"dg,omitempty"`
}

// LogicalDrive is a volume inside an array.
// JSON field stays "ld" for both vendors; UI copy should say Smart Array "logical
// drive" / MegaRAID "virtual drive" (VD) as appropriate.
type LogicalDrive struct {
	ID     int     `json:"id"`
	RAID   string  `json:"raid"`
	Size   string  `json:"size"`
	Status string  `json:"status"`
	Disk   *string `json:"disk"`
	// WWN is the volume's stable identity:
	// Smart Array LD Unique Identifier, MegaRAID VD SCSI NAA, …
	WWN string `json:"wwn,omitempty"`
}

// Array groups physical drives under one volume (Smart Array LD / MegaRAID VD).
type Array struct {
	Name   string       `json:"name"`
	LD     LogicalDrive `json:"ld"`
	Drives []string     `json:"drives"`
	// DG is the MegaRAID drive-group id from "DG/VD" (may differ from LD.ID).
	DG string `json:"dg,omitempty"`
}

// Port is a controller port with bay numbers for the chassis map.
type Port struct {
	Name string `json:"name"`
	Bays []int  `json:"bays"`
}

// Controller is the JSON shape the storage console expects (vendor-neutral).
type Controller struct {
	Family Family  `json:"family"`
	Tool   string  `json:"tool"`
	Slot   int     `json:"slot"`
	Model  string  `json:"model"`
	Serial string  `json:"serial"`
	Mode   string  `json:"mode"`
	Status string  `json:"status"`
	Ports  []Port  `json:"ports"`
	Drives []Drive `json:"drives"`
	Arrays []Array `json:"arrays"`
}

// Normalize ensures list fields are non-nil so JSON encodes [] not null.
func (c *Controller) Normalize() {
	if c.Ports == nil {
		c.Ports = []Port{}
	}
	if c.Drives == nil {
		c.Drives = []Drive{}
	}
	if c.Arrays == nil {
		c.Arrays = []Array{}
	}
	for i := range c.Ports {
		if c.Ports[i].Bays == nil {
			c.Ports[i].Bays = []int{}
		}
	}
	for i := range c.Arrays {
		if c.Arrays[i].Drives == nil {
			c.Arrays[i].Drives = []string{}
		}
	}
}

// DetectResult is persisted after a successful (or failed) live-OS probe.
type DetectResult struct {
	OK         bool      `json:"ok"`
	Family     Family    `json:"family"`
	Tool       string    `json:"tool,omitempty"`
	Model      string    `json:"model,omitempty"`
	Slot       int       `json:"slot,omitempty"`
	DataIP     string    `json:"data_ip,omitempty"`
	Error      string    `json:"error,omitempty"`
	Detail     string    `json:"detail,omitempty"`
	DetectedAt time.Time `json:"detected_at"`
}

// SnapshotKind is the store.snapshots kind for DetectResult JSON.
const SnapshotKind = "raid_detect"
