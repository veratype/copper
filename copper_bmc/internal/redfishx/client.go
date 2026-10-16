package redfishx

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/stmcginnis/gofish"
	"github.com/stmcginnis/gofish/schemas"
)

// Target identifies one hardware BMC. Control plane sends this per request
// so the agent can operate on a collection of BMCs without storing credentials.
type Target struct {
	Endpoint string `json:"endpoint"`
	Username string `json:"username"`
	Password string `json:"password"`
	Insecure bool   `json:"insecure"`
}

// Client wraps gofish for southbound Redfish operations.
type Client struct{}

func New() *Client { return &Client{} }

func (c *Client) connect(t Target) (*gofish.APIClient, error) {
	if strings.TrimSpace(t.Endpoint) == "" {
		return nil, fmt.Errorf("bmc endpoint is required")
	}
	if strings.TrimSpace(t.Username) == "" {
		return nil, fmt.Errorf("bmc username is required")
	}
	endpoint := strings.TrimRight(t.Endpoint, "/")
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "https://" + endpoint
	}
	return gofish.Connect(gofish.ClientConfig{
		Endpoint: endpoint,
		Username: t.Username,
		Password: t.Password,
		Insecure: t.Insecure,
	})
}

// Component inventory ---------------------------------------------------------

// SystemInventory is the combined hardware snapshot returned by GET /v1/system.
// Keys: system / cpu / ram / storage / network / gpu / bmc.
type SystemInventory struct {
	System  SystemInfo  `json:"system"`
	CPU     []CPUInfo   `json:"cpu"`
	RAM     []RAMInfo   `json:"ram"`
	Storage []DriveInfo `json:"storage"`
	Network []NICInfo   `json:"network"`
	GPU     []GPUInfo   `json:"gpu"`
	BMC     []BMCInfo   `json:"bmc"`
}

type SystemInfo struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	SerialNumber string `json:"serial_number"`
	UUID         string `json:"uuid"`
	PowerState   string `json:"power_state"`
	BIOSVersion  string `json:"bios_version"`
}

type CPUInfo struct {
	ID            string `json:"id"`
	Manufacturer  string `json:"manufacturer"`
	Model         string `json:"model"`
	TotalCores    int    `json:"total_cores"`
	TotalThreads  int    `json:"total_threads"`
	MaxSpeedMHz   int    `json:"max_speed_mhz"`
	ProcessorType string `json:"processor_type,omitempty"`
}

type RAMInfo struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	CapacityMiB      int    `json:"capacity_mib"`
	OperatingMHz     int    `json:"operating_mhz"`
	Manufacturer     string `json:"manufacturer"`
	SerialNumber     string `json:"serial_number"`
	MemoryDeviceType string `json:"device_type"`
}

type NICInfo struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	MACAddress   string   `json:"mac_address"`
	SpeedMbps    int      `json:"speed_mbps"`
	PermanentMAC string   `json:"permanent_mac,omitempty"`
	IPv4         []string `json:"ipv4,omitempty"`
}

type GPUInfo struct {
	ID            string `json:"id"`
	Name          string `json:"name,omitempty"`
	Manufacturer  string `json:"manufacturer"`
	Model         string `json:"model"`
	SerialNumber  string `json:"serial_number,omitempty"`
	PartNumber    string `json:"part_number,omitempty"`
	ProcessorType string `json:"processor_type,omitempty"`
	Source        string `json:"source"` // processor | graphics_controller | pcie
	MemoryMiB     int    `json:"memory_mib,omitempty"`
}

// BMCInfo is the out-of-band management controller (iLO / iDRAC / …).
type BMCInfo struct {
	ID                    string    `json:"id"`
	Name                  string    `json:"name"`
	ManagerType           string    `json:"manager_type"`
	Manufacturer          string    `json:"manufacturer,omitempty"`
	Model                 string    `json:"model,omitempty"`
	SerialNumber          string    `json:"serial_number,omitempty"`
	UUID                  string    `json:"uuid,omitempty"`
	ServiceEntryPointUUID string    `json:"service_entry_point_uuid,omitempty"`
	FirmwareVersion       string    `json:"firmware_version,omitempty"`
	MACAddresses          []string  `json:"mac_addresses,omitempty"`
	Network               []NICInfo `json:"network,omitempty"`
}

// DriveLocation captures bay/box/slot placement for admins.
type DriveLocation struct {
	Info                string `json:"info,omitempty"` // human string some BMCs still send
	InfoFormat          string `json:"info_format,omitempty"`
	LocationType        string `json:"location_type,omitempty"` // Bay, Slot, Connector, …
	Ordinal             *int   `json:"ordinal,omitempty"`       // e.g. bay index
	ServiceLabel        string `json:"service_label,omitempty"`
	Reference           string `json:"reference,omitempty"` // Front, Rear, …
	Orientation         string `json:"orientation,omitempty"`
	PartLocationContext string `json:"part_location_context,omitempty"` // box / enclosure hierarchy
	Rack                string `json:"rack,omitempty"`
	RackOffset          *int   `json:"rack_offset,omitempty"`
	AdditionalInfo      string `json:"additional_info,omitempty"`
}

type DriveInfo struct {
	ID                        string         `json:"id"`
	Name                      string         `json:"name"`
	Description               string         `json:"description,omitempty"`
	Manufacturer              string         `json:"manufacturer"`
	Model                     string         `json:"model"`
	SerialNumber              string         `json:"serial_number"`
	PartNumber                string         `json:"part_number,omitempty"`
	SKU                       string         `json:"sku,omitempty"`
	AssetTag                  string         `json:"asset_tag,omitempty"`
	Revision                  string         `json:"revision,omitempty"`
	FirmwareVersion           string         `json:"firmware_version,omitempty"`
	HardwareVersion           string         `json:"hardware_version,omitempty"`
	MediaType                 string         `json:"media_type"` // HDD, SSD, …
	Protocol                  string         `json:"protocol"`   // SATA, SAS, NVMe, …
	FormFactor                string         `json:"form_factor,omitempty"`
	CapacityGiB               int64          `json:"capacity_gib"`
	CapacityBytes             int64          `json:"capacity_bytes"`
	BlockSizeBytes            int            `json:"block_size_bytes,omitempty"`
	CapableSpeedGbs           *float64       `json:"capable_speed_gbs,omitempty"`
	NegotiatedSpeedGbs        *float64       `json:"negotiated_speed_gbs,omitempty"`
	RotationSpeedRPM          *float64       `json:"rotation_speed_rpm,omitempty"`
	PredictedMediaLifeLeftPct *float64       `json:"predicted_media_life_left_percent,omitempty"`
	FailurePredicted          bool           `json:"failure_predicted"`
	HotspareType              string         `json:"hotspare_type,omitempty"`
	EncryptionAbility         string         `json:"encryption_ability,omitempty"`
	EncryptionStatus          string         `json:"encryption_status,omitempty"`
	Status                    string         `json:"status,omitempty"`
	StatusState               string         `json:"status_state,omitempty"`
	StatusIndicator           string         `json:"status_indicator,omitempty"`
	Controller                string         `json:"controller,omitempty"`
	Chassis                   string         `json:"chassis,omitempty"`
	Source                    string         `json:"source,omitempty"` // system_storage | chassis_drives | …
	Location                  *DriveLocation `json:"location,omitempty"`
}

// StorageInventory remains for GET /v1/system/storage.
type StorageInventory struct {
	Drives []DriveInfo `json:"drives"`
}

func (c *Client) Inventory(t Target) (*SystemInventory, error) {
	api, err := c.connect(t)
	if err != nil {
		return nil, fmt.Errorf("redfish connect: %w", err)
	}
	defer api.Logout()

	systems, err := api.Service.Systems()
	if err != nil {
		return nil, fmt.Errorf("list systems: %w", err)
	}
	if len(systems) == 0 {
		return nil, fmt.Errorf("no ComputerSystem found on BMC")
	}
	sys := systems[0]

	inv := &SystemInventory{
		System: SystemInfo{
			ID:           sys.ID,
			Name:         sys.Name,
			Manufacturer: sys.Manufacturer,
			Model:        sys.Model,
			SerialNumber: sys.SerialNumber,
			UUID:         sys.UUID,
			PowerState:   string(sys.PowerState),
			BIOSVersion:  sys.BiosVersion,
		},
		CPU:     []CPUInfo{},
		RAM:     []RAMInfo{},
		Storage: []DriveInfo{},
		Network: []NICInfo{},
		GPU:     []GPUInfo{},
		BMC:     []BMCInfo{},
	}

	gpuSeen := map[string]struct{}{}

	if procs, err := sys.Processors(); err == nil {
		for _, p := range procs {
			pt := string(p.ProcessorType)
			info := CPUInfo{
				ID:            p.ID,
				Manufacturer:  p.Manufacturer,
				Model:         p.Model,
				TotalCores:    derefInt(p.TotalCores),
				TotalThreads:  derefInt(p.TotalThreads),
				MaxSpeedMHz:   derefInt(p.MaxSpeedMHz),
				ProcessorType: pt,
			}
			switch schemas.ProcessorType(pt) {
			case schemas.GPUProcessorType, schemas.AcceleratorProcessorType:
				key := "proc:" + p.ID
				gpuSeen[key] = struct{}{}
				inv.GPU = append(inv.GPU, GPUInfo{
					ID:            p.ID,
					Name:          p.Name,
					Manufacturer:  p.Manufacturer,
					Model:         p.Model,
					SerialNumber:  p.SerialNumber,
					ProcessorType: pt,
					Source:        "processor",
					MemoryMiB:     derefInt(p.MemorySummary.TotalMemorySizeMiB),
				})
			default:
				// Empty / CPU / Core / OEM → treat as CPU inventory.
				inv.CPU = append(inv.CPU, info)
			}
		}
	}

	if dims, err := sys.Memory(); err == nil {
		for _, m := range dims {
			inv.RAM = append(inv.RAM, RAMInfo{
				ID:               m.ID,
				Name:             m.Name,
				CapacityMiB:      derefInt(m.CapacityMiB),
				OperatingMHz:     derefInt(m.OperatingSpeedMhz),
				Manufacturer:     m.Manufacturer,
				SerialNumber:     m.SerialNumber,
				MemoryDeviceType: string(m.MemoryDeviceType),
			})
		}
	}

	if nics, err := sys.EthernetInterfaces(); err == nil {
		for _, n := range nics {
			info := NICInfo{
				ID:           n.ID,
				Name:         n.Name,
				MACAddress:   n.MACAddress,
				SpeedMbps:    derefInt(n.SpeedMbps),
				PermanentMAC: n.PermanentMACAddress,
			}
			for _, a := range n.IPv4Addresses {
				if a.Address != "" {
					info.IPv4 = append(info.IPv4, a.Address)
				}
			}
			inv.Network = append(inv.Network, info)
		}
	}

	if gcs, err := sys.GraphicsControllers(); err == nil {
		for _, g := range gcs {
			key := "gfx:" + g.ID
			if _, ok := gpuSeen[key]; ok {
				continue
			}
			gpuSeen[key] = struct{}{}
			inv.GPU = append(inv.GPU, GPUInfo{
				ID:           g.ID,
				Name:         g.Name,
				Manufacturer: g.Manufacturer,
				Model:        g.Model,
				SerialNumber: g.SerialNumber,
				PartNumber:   g.PartNumber,
				Source:       "graphics_controller",
			})
		}
	}

	if devices, err := sys.PCIeDevices(); err == nil {
		for _, d := range devices {
			if !looksLikeGPU(d.Name, d.Model, d.Description) {
				continue
			}
			key := "pcie:" + d.ID
			if _, ok := gpuSeen[key]; ok {
				continue
			}
			// Skip if we already have the same model from processors/gfx.
			dup := false
			for _, g := range inv.GPU {
				if g.Model != "" && g.Model == d.Model {
					dup = true
					break
				}
			}
			if dup {
				continue
			}
			gpuSeen[key] = struct{}{}
			inv.GPU = append(inv.GPU, GPUInfo{
				ID:           d.ID,
				Name:         d.Name,
				Manufacturer: d.Manufacturer,
				Model:        d.Model,
				SerialNumber: d.SerialNumber,
				PartNumber:   d.PartNumber,
				Source:       "pcie",
			})
		}
	}

	inv.Storage = collectDrives(api, systems)
	inv.BMC = collectBMC(api)

	return inv, nil
}

func (c *Client) Storage(t Target) (*StorageInventory, error) {
	api, err := c.connect(t)
	if err != nil {
		return nil, fmt.Errorf("redfish connect: %w", err)
	}
	defer api.Logout()

	systems, err := api.Service.Systems()
	if err != nil {
		return nil, fmt.Errorf("list systems: %w", err)
	}
	if len(systems) == 0 {
		return nil, fmt.Errorf("no ComputerSystem found on BMC")
	}
	return &StorageInventory{Drives: collectDrives(api, systems)}, nil
}

func collectBMC(api *gofish.APIClient) []BMCInfo {
	out := []BMCInfo{}
	mgrs, err := api.Service.Managers()
	if err != nil {
		return out
	}
	for _, m := range mgrs {
		info := BMCInfo{
			ID:                    m.ID,
			Name:                  m.Name,
			ManagerType:           string(m.ManagerType),
			Manufacturer:          m.Manufacturer,
			Model:                 m.Model,
			SerialNumber:          m.SerialNumber,
			UUID:                  m.UUID,
			ServiceEntryPointUUID: m.ServiceEntryPointUUID,
			FirmwareVersion:       m.FirmwareVersion,
			MACAddresses:          []string{},
			Network:               []NICInfo{},
		}
		if nics, err := m.EthernetInterfaces(); err == nil {
			for _, n := range nics {
				nic := NICInfo{
					ID:           n.ID,
					Name:         n.Name,
					MACAddress:   n.MACAddress,
					SpeedMbps:    derefInt(n.SpeedMbps),
					PermanentMAC: n.PermanentMACAddress,
				}
				for _, a := range n.IPv4Addresses {
					if a.Address != "" {
						nic.IPv4 = append(nic.IPv4, a.Address)
					}
				}
				info.Network = append(info.Network, nic)
				for _, mac := range []string{n.MACAddress, n.PermanentMACAddress} {
					mac = strings.TrimSpace(mac)
					if mac == "" || mac == "00:00:00:00:00:00" {
						continue
					}
					dup := false
					for _, existing := range info.MACAddresses {
						if strings.EqualFold(existing, mac) {
							dup = true
							break
						}
					}
					if !dup {
						info.MACAddresses = append(info.MACAddresses, mac)
					}
				}
			}
		}
		out = append(out, info)
	}
	return out
}

func collectDrives(api *gofish.APIClient, systems []*schemas.ComputerSystem) []DriveInfo {
	out := []DriveInfo{}
	seen := map[string]struct{}{}

	add := func(d *schemas.Drive, controller, chassis, source string) {
		key := d.ODataID
		if key == "" {
			key = d.ID + "|" + d.SerialNumber
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, mapDrive(d, controller, chassis, source))
	}

	for _, sys := range systems {
		if storage, err := sys.Storage(); err == nil {
			for _, st := range storage {
				drives, err := st.Drives()
				if err != nil {
					continue
				}
				for _, d := range drives {
					add(d, st.Name, "", "system_storage")
				}
			}
		}

		if simple, err := sys.SimpleStorage(); err == nil {
			for _, ss := range simple {
				for _, dev := range ss.Devices {
					key := "simple:" + ss.ID + "/" + dev.Name + "/" + dev.Model
					if _, ok := seen[key]; ok {
						continue
					}
					seen[key] = struct{}{}
					bytes := int64(derefUint(dev.CapacityBytes))
					out = append(out, DriveInfo{
						ID:            dev.Name,
						Name:          dev.Name,
						Manufacturer:  dev.Manufacturer,
						Model:         dev.Model,
						CapacityBytes: bytes,
						CapacityGiB:   bytes / (1024 * 1024 * 1024),
						Status:        string(dev.Status.Health),
						Controller:    ss.Name,
						MediaType:     "unknown",
						Protocol:      "SimpleStorage",
						Source:        "system_simple_storage",
					})
				}
			}
		}
	}

	// HP iLO / many BMCs expose bays under Chassis, not ComputerSystem.Storage.
	if chassisList, err := api.Service.Chassis(); err == nil {
		for _, ch := range chassisList {
			chassisLabel := ch.Name
			if chassisLabel == "" {
				chassisLabel = ch.ID
			}
			if drives, err := ch.Drives(); err == nil {
				for _, d := range drives {
					add(d, "", chassisLabel, "chassis_drives")
				}
			}
			if storage, err := ch.Storage(); err == nil {
				for _, st := range storage {
					drives, err := st.Drives()
					if err != nil {
						continue
					}
					for _, d := range drives {
						add(d, st.Name, chassisLabel, "chassis_storage")
					}
				}
			}
		}
	}

	return out
}

func mapDrive(d *schemas.Drive, controller, chassis, source string) DriveInfo {
	bytes := int64(derefInt(d.CapacityBytes))
	info := DriveInfo{
		ID:                        d.ID,
		Name:                      d.Name,
		Description:               d.Description,
		Manufacturer:              d.Manufacturer,
		Model:                     d.Model,
		SerialNumber:              d.SerialNumber,
		PartNumber:                d.PartNumber,
		SKU:                       d.SKU,
		AssetTag:                  d.AssetTag,
		Revision:                  d.Revision,
		FirmwareVersion:           d.FirmwareVersion,
		HardwareVersion:           d.HardwareVersion,
		MediaType:                 string(d.MediaType),
		Protocol:                  string(d.Protocol),
		FormFactor:                string(d.DriveFormFactor),
		CapacityBytes:             bytes,
		CapacityGiB:               bytes / (1024 * 1024 * 1024),
		BlockSizeBytes:            derefInt(d.BlockSizeBytes),
		CapableSpeedGbs:           d.CapableSpeedGbs,
		NegotiatedSpeedGbs:        d.NegotiatedSpeedGbs,
		RotationSpeedRPM:          d.RotationSpeedRPM,
		PredictedMediaLifeLeftPct: d.PredictedMediaLifeLeftPercent,
		FailurePredicted:          d.FailurePredicted,
		HotspareType:              string(d.HotspareType),
		EncryptionAbility:         string(d.EncryptionAbility),
		EncryptionStatus:          string(d.EncryptionStatus),
		Status:                    string(d.Status.Health),
		StatusState:               string(d.Status.State),
		StatusIndicator:           string(d.StatusIndicator),
		Controller:                controller,
		Chassis:                   chassis,
		Source:                    source,
		Location:                  driveLocation(d),
	}
	return info
}

func driveLocation(d *schemas.Drive) *DriveLocation {
	loc := DriveLocation{}
	filled := false

	pl := d.PhysicalLocation
	if applyLocation(&loc, pl) {
		filled = true
	}
	// Older Redfish: Location[] (first useful entry).
	for _, l := range d.Location {
		if applyLocation(&loc, l) {
			filled = true
			break
		}
	}

	// Fallback: IDs like Disk.Bay.3 / HDDBay.1
	if loc.Ordinal == nil || loc.LocationType == "" {
		if typ, ord, ok := parseBaySlotFromID(d.ID, d.Name); ok {
			if loc.LocationType == "" {
				loc.LocationType = typ
			}
			if loc.Ordinal == nil {
				loc.Ordinal = &ord
			}
			filled = true
		}
	}

	if !filled {
		return nil
	}
	return &loc
}

func applyLocation(dst *DriveLocation, src schemas.Location) bool {
	filled := false
	if src.Info != "" {
		dst.Info = src.Info
		filled = true
	}
	if src.InfoFormat != "" {
		dst.InfoFormat = src.InfoFormat
		filled = true
	}
	if src.PartLocationContext != "" {
		dst.PartLocationContext = src.PartLocationContext
		filled = true
	}
	pl := src.PartLocation
	if pl.LocationType != "" {
		dst.LocationType = string(pl.LocationType)
		filled = true
	}
	if pl.LocationOrdinalValue != nil {
		dst.Ordinal = pl.LocationOrdinalValue
		filled = true
	}
	if pl.ServiceLabel != "" {
		dst.ServiceLabel = pl.ServiceLabel
		filled = true
	}
	if pl.Reference != "" {
		dst.Reference = string(pl.Reference)
		filled = true
	}
	if pl.Orientation != "" {
		dst.Orientation = string(pl.Orientation)
		filled = true
	}
	if src.Placement.Rack != "" {
		dst.Rack = src.Placement.Rack
		filled = true
	}
	if src.Placement.RackOffset != nil {
		dst.RackOffset = src.Placement.RackOffset
		filled = true
	}
	if src.Placement.AdditionalInfo != "" {
		dst.AdditionalInfo = src.Placement.AdditionalInfo
		filled = true
	}
	return filled
}

func parseBaySlotFromID(parts ...string) (locationType string, ordinal int, ok bool) {
	joined := strings.Join(parts, " ")
	lower := strings.ToLower(joined)
	for _, kind := range []string{"bay", "slot", "box"} {
		idx := strings.Index(lower, kind)
		if idx < 0 {
			continue
		}
		rest := joined[idx+len(kind):]
		rest = strings.TrimLeft(rest, " .-_#:")
		num := ""
		for _, r := range rest {
			if r >= '0' && r <= '9' {
				num += string(r)
			} else if num != "" {
				break
			}
		}
		if num == "" {
			continue
		}
		n, err := strconv.Atoi(num)
		if err != nil {
			continue
		}
		switch kind {
		case "bay":
			return "Bay", n, true
		case "slot":
			return "Slot", n, true
		case "box":
			return "Box", n, true
		}
	}
	return "", 0, false
}

func looksLikeGPU(parts ...string) bool {
	joined := strings.ToLower(strings.Join(parts, " "))
	for _, tip := range []string{"gpu", "nvidia", "amd ", "radeon", "quadro", "tesla", "a100", "h100", "l40", "geforce", "instinct", "graphics"} {
		if strings.Contains(joined, tip) {
			return true
		}
	}
	return false
}

// Boot ------------------------------------------------------------------------

type BootInfo struct {
	Target         string   `json:"target"`
	Enabled        string   `json:"enabled"`
	Mode           string   `json:"mode,omitempty"`
	AllowedTargets []string `json:"allowed_targets,omitempty"`
	AllowedEnabled []string `json:"allowed_enabled,omitempty"`
}

type SetBootRequest struct {
	Target  string `json:"target"`  // Cd, Usb, Hdd, Pxe, RemoteDrive, …
	Enabled string `json:"enabled"` // Once (default), Continuous, Disabled
	Mode    string `json:"mode,omitempty"`
}

type VirtualMediaInfo struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Inserted     bool     `json:"inserted"`
	Image        string   `json:"image,omitempty"`
	MediaTypes   []string `json:"media_types,omitempty"`
	ConnectedVia string   `json:"connected_via,omitempty"`
	Source       string   `json:"source"` // manager or system
}

type MountMediaRequest struct {
	ImageURL   string `json:"image_url"`           // must be reachable from BMC OOB NIC
	BootOnce   *bool  `json:"boot_once,omitempty"` // default true
	Reboot     bool   `json:"reboot,omitempty"`
	BootTarget string `json:"boot_target,omitempty"` // default Cd
	ResetType  string `json:"reset_type,omitempty"`  // default ForceRestart
	DeviceID   string `json:"device_id,omitempty"`   // optional virtual media slot id
}

type ResetRequest struct {
	ResetType string `json:"reset_type,omitempty"` // ForceRestart, GracefulRestart, …
}

func (c *Client) Boot(t Target) (*BootInfo, error) {
	api, err := c.connect(t)
	if err != nil {
		return nil, fmt.Errorf("redfish connect: %w", err)
	}
	defer api.Logout()

	sys, err := firstSystem(api)
	if err != nil {
		return nil, err
	}

	info := &BootInfo{
		Target:  string(sys.Boot.BootSourceOverrideTarget),
		Enabled: string(sys.Boot.BootSourceOverrideEnabled),
		Mode:    string(sys.Boot.BootSourceOverrideMode),
	}
	for _, t := range sys.Boot.AllowableBootSourceOverrideTargetValues {
		info.AllowedTargets = append(info.AllowedTargets, string(t))
	}
	info.AllowedEnabled = []string{"Disabled", "Once", "Continuous"}
	return info, nil
}

func (c *Client) SetBoot(t Target, req SetBootRequest) (*BootInfo, error) {
	if req.Target == "" {
		return nil, fmt.Errorf("target is required")
	}
	enabled := req.Enabled
	if enabled == "" {
		enabled = "Once"
	}

	api, err := c.connect(t)
	if err != nil {
		return nil, fmt.Errorf("redfish connect: %w", err)
	}
	defer api.Logout()

	sys, err := firstSystem(api)
	if err != nil {
		return nil, err
	}

	boot := &schemas.Boot{
		BootSourceOverrideTarget:  schemas.BootSource(req.Target),
		BootSourceOverrideEnabled: schemas.BootSourceOverrideEnabled(enabled),
	}
	if req.Mode != "" {
		boot.BootSourceOverrideMode = schemas.BootSourceOverrideMode(req.Mode)
	}
	if err := sys.SetBoot(boot); err != nil {
		return nil, fmt.Errorf("set boot: %w", err)
	}
	return c.Boot(t)
}

func (c *Client) ListVirtualMedia(t Target) ([]VirtualMediaInfo, error) {
	api, err := c.connect(t)
	if err != nil {
		return nil, fmt.Errorf("redfish connect: %w", err)
	}
	defer api.Logout()

	var out []VirtualMediaInfo

	managers, err := api.Service.Managers()
	if err == nil {
		for _, m := range managers {
			vms, err := m.VirtualMedia()
			if err != nil {
				continue
			}
			for _, vm := range vms {
				out = append(out, mapVM(vm, "manager:"+m.ID))
			}
		}
	}

	systems, err := api.Service.Systems()
	if err == nil {
		for _, s := range systems {
			vms, err := s.VirtualMedia()
			if err != nil {
				continue
			}
			for _, vm := range vms {
				out = append(out, mapVM(vm, "system:"+s.ID))
			}
		}
	}

	return out, nil
}

func (c *Client) MountBootMedia(t Target, req MountMediaRequest) (map[string]any, error) {
	if strings.TrimSpace(req.ImageURL) == "" {
		return nil, fmt.Errorf("image_url is required")
	}
	bootOnce := true
	if req.BootOnce != nil {
		bootOnce = *req.BootOnce
	}
	bootTarget := req.BootTarget
	if bootTarget == "" {
		bootTarget = "Cd"
	}
	resetType := req.ResetType
	if resetType == "" {
		resetType = string(schemas.ForceRestartResetType)
	}

	api, err := c.connect(t)
	if err != nil {
		return nil, fmt.Errorf("redfish connect: %w", err)
	}
	defer api.Logout()

	vm, source, err := findVirtualMedia(api, req.DeviceID)
	if err != nil {
		return nil, err
	}

	if derefBool(vm.Inserted) {
		if _, err := vm.EjectMedia(); err != nil {
			return nil, fmt.Errorf("eject existing media: %w", err)
		}
	}

	inserted := true
	writeProtected := true
	proto := schemas.HTTPTransferProtocolType
	if strings.HasPrefix(strings.ToLower(req.ImageURL), "https://") {
		proto = schemas.HTTPSTransferProtocolType
	}
	params := &schemas.VirtualMediaInsertMediaParameters{
		Image:                req.ImageURL,
		Inserted:             &inserted,
		WriteProtected:       &writeProtected,
		TransferProtocolType: &proto,
		MediaType:            schemas.CDVirtualMediaType,
	}
	if _, err := vm.InsertMedia(params); err != nil {
		return nil, fmt.Errorf("insert media: %w", err)
	}

	sys, err := firstSystem(api)
	if err != nil {
		return nil, err
	}

	enabled := schemas.OnceBootSourceOverrideEnabled
	if !bootOnce {
		enabled = schemas.ContinuousBootSourceOverrideEnabled
	}
	if err := sys.SetBoot(&schemas.Boot{
		BootSourceOverrideTarget:  schemas.BootSource(bootTarget),
		BootSourceOverrideEnabled: enabled,
	}); err != nil {
		return nil, fmt.Errorf("set one-time boot to %s: %w", bootTarget, err)
	}

	result := map[string]any{
		"image_url":     req.ImageURL,
		"virtual_media": mapVM(vm, source),
		"boot_target":   bootTarget,
		"boot_once":     bootOnce,
		"rebooted":      false,
	}

	if req.Reboot {
		if _, err := sys.Reset(schemas.ResetType(resetType)); err != nil {
			return nil, fmt.Errorf("media mounted and boot set, but reset failed: %w", err)
		}
		result["rebooted"] = true
		result["reset_type"] = resetType
	}

	return result, nil
}

func (c *Client) Reset(t Target, req ResetRequest) (map[string]any, error) {
	resetType := req.ResetType
	if resetType == "" {
		resetType = string(schemas.ForceRestartResetType)
	}

	api, err := c.connect(t)
	if err != nil {
		return nil, fmt.Errorf("redfish connect: %w", err)
	}
	defer api.Logout()

	sys, err := firstSystem(api)
	if err != nil {
		return nil, err
	}

	if _, err := sys.Reset(schemas.ResetType(resetType)); err != nil {
		return nil, fmt.Errorf("reset: %w", err)
	}
	return map[string]any{
		"status":     "accepted",
		"reset_type": resetType,
		"system_id":  sys.ID,
	}, nil
}

// helpers ---------------------------------------------------------------------

func firstSystem(api *gofish.APIClient) (*schemas.ComputerSystem, error) {
	systems, err := api.Service.Systems()
	if err != nil {
		return nil, fmt.Errorf("list systems: %w", err)
	}
	if len(systems) == 0 {
		return nil, fmt.Errorf("no ComputerSystem found on BMC")
	}
	return systems[0], nil
}

func mapVM(vm *schemas.VirtualMedia, source string) VirtualMediaInfo {
	info := VirtualMediaInfo{
		ID:           vm.ID,
		Name:         vm.Name,
		Inserted:     derefBool(vm.Inserted),
		Image:        vm.Image,
		ConnectedVia: string(vm.ConnectedVia),
		Source:       source,
	}
	for _, t := range vm.MediaTypes {
		info.MediaTypes = append(info.MediaTypes, string(t))
	}
	return info
}

func findVirtualMedia(api *gofish.APIClient, deviceID string) (*schemas.VirtualMedia, string, error) {
	var candidates []*schemas.VirtualMedia
	var sources []string

	if managers, err := api.Service.Managers(); err == nil {
		for _, m := range managers {
			vms, err := m.VirtualMedia()
			if err != nil {
				continue
			}
			for _, vm := range vms {
				candidates = append(candidates, vm)
				sources = append(sources, "manager:"+m.ID)
			}
		}
	}
	if systems, err := api.Service.Systems(); err == nil {
		for _, s := range systems {
			vms, err := s.VirtualMedia()
			if err != nil {
				continue
			}
			for _, vm := range vms {
				candidates = append(candidates, vm)
				sources = append(sources, "system:"+s.ID)
			}
		}
	}

	if len(candidates) == 0 {
		return nil, "", fmt.Errorf("no VirtualMedia devices found on BMC")
	}

	if deviceID != "" {
		for i, vm := range candidates {
			if vm.ID == deviceID || vm.Name == deviceID {
				return vm, sources[i], nil
			}
		}
		return nil, "", fmt.Errorf("virtual media device %q not found", deviceID)
	}

	// Prefer a CD-capable slot that is free or any CD-capable slot.
	for i, vm := range candidates {
		if supportsCD(vm) && !derefBool(vm.Inserted) {
			return vm, sources[i], nil
		}
	}
	for i, vm := range candidates {
		if supportsCD(vm) {
			return vm, sources[i], nil
		}
	}
	return candidates[0], sources[0], nil
}

func supportsCD(vm *schemas.VirtualMedia) bool {
	if len(vm.MediaTypes) == 0 {
		return true
	}
	for _, t := range vm.MediaTypes {
		if t == schemas.CDVirtualMediaType || t == schemas.DVDVirtualMediaType {
			return true
		}
	}
	return false
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func derefUint(p *uint) uint {
	if p == nil {
		return 0
	}
	return *p
}

func derefBool(p *bool) bool {
	return p != nil && *p
}
