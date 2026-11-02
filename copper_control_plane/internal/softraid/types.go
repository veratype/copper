// Package softraid manages Linux software RAID (mdadm) on a live OS over SSH.
package softraid

import "time"

// SnapshotKind is the store.snapshots kind for the last Soft RAID scan.
const SnapshotKind = "softraid_topology"

// Disk is an OS-visible whole disk that can join an md array.
type Disk struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Size   string `json:"size"`
	Tran   string `json:"tran,omitempty"`
	Model  string `json:"model,omitempty"`
	Serial string `json:"serial,omitempty"`
	WWN    string `json:"wwn,omitempty"`
	// State: free | member | spare | faulty | foreign
	State string `json:"state"`
	Array string `json:"array,omitempty"` // md name when member/spare
}

// Member is one device in an md array.
type Member struct {
	Device string `json:"device"`
	Role   string `json:"role"` // active | spare | faulty | journal | write-mostly | …
	Slot   int    `json:"slot,omitempty"`
}

// Array is one assembled (or known) md device.
type Array struct {
	Name         string   `json:"name"` // md0
	Path         string   `json:"path"` // /dev/md0
	UUID         string   `json:"uuid,omitempty"`
	Level        string   `json:"level"` // raid0, raid1, raid5, …
	State        string   `json:"state"` // clean, active, degraded, recovering, …
	Size         string   `json:"size,omitempty"`
	RaidDevices  int      `json:"raid_devices,omitempty"`
	TotalDevices int      `json:"total_devices,omitempty"`
	SpareDevices int      `json:"spare_devices,omitempty"`
	Metadata     string   `json:"metadata,omitempty"`
	Members      []Member `json:"members,omitempty"`
}

// Topology is the Soft RAID picture on one live host.
type Topology struct {
	OK        bool      `json:"ok"`
	Tool      string    `json:"tool,omitempty"`
	DataIP    string    `json:"data_ip,omitempty"`
	Arrays    []Array   `json:"arrays"`
	Disks     []Disk    `json:"disks"`
	Error     string    `json:"error,omitempty"`
	Detail    string    `json:"detail,omitempty"`
	ScannedAt time.Time `json:"scanned_at"`
	// BatchFingerprint is set by Scan for cross-host Soft RAID batch matching.
	BatchFingerprint string `json:"batch_fingerprint,omitempty"`
}

// Action is one allowlisted mdadm lifecycle operation.
type Action struct {
	Op string `json:"op"` // create | stop | remove | fail | remove-device | add | add-spare | assemble | assemble-scan | zero-superblock | grow

	// create / assemble
	Name         string   `json:"name,omitempty"`          // md0 or /dev/md0
	Level        string   `json:"level,omitempty"`         // 0|1|4|5|6|10|linear
	Devices      []string `json:"devices,omitempty"`       // member disks
	Metadata     string   `json:"metadata,omitempty"`      // 1.0 (default) | 1.1 | 1.2 | 0.90
	RaidDevices  int      `json:"raid_devices,omitempty"`  // defaults to len(Devices)
	SpareDevices []string `json:"spare_devices,omitempty"` // optional spares at create
	Force        bool     `json:"force,omitempty"`
	Layout       string   `json:"layout,omitempty"` // RAID10 layout e.g. n2

	// single-device ops (fail / remove-device / add / add-spare / zero-superblock)
	Device string `json:"device,omitempty"`

	// grow
	GrowRaidDevices int `json:"grow_raid_devices,omitempty"`
}
