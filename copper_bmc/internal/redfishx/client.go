package redfishx

import (
	"encoding/json"
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

// LogFunc receives redfish operation progress for the agent activity console.
// level is "info", "warn", or "error".
type LogFunc func(level, format string, args ...any)

// Client wraps gofish for southbound Redfish operations.
type Client struct {
	logfn LogFunc
}

func New() *Client { return &Client{} }

// SetLogger wires agent activity logging (optional).
func (c *Client) SetLogger(fn LogFunc) {
	c.logfn = fn
}

func (c *Client) logf(level, format string, args ...any) {
	if c == nil || c.logfn == nil {
		return
	}
	c.logfn(level, format, args...)
}

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
// Keys: system / cpu / ram / storage / network / gpu / pcie / psu / fans / bmc / chassis / counts.
type SystemInventory struct {
	System  SystemInfo      `json:"system"`
	CPU     []CPUInfo       `json:"cpu"`
	RAM     []RAMInfo       `json:"ram"`
	Storage []DriveInfo     `json:"storage"`
	Network []NICInfo       `json:"network"`
	GPU     []GPUInfo       `json:"gpu"`
	PCIe    []PCIeInfo      `json:"pcie"`
	PSU     []PSUInfo       `json:"psu"`
	Fans    []FanInfo       `json:"fans"`
	BMC     []BMCInfo       `json:"bmc"`
	Chassis []ChassisInfo   `json:"chassis"` // Redfish Chassis[].SerialNumber — NOT servers.chassis_id
	Counts  InventoryCounts `json:"counts"`
}

// InventoryCounts are convenience totals (same as len of each slice).
type InventoryCounts struct {
	CPU     int `json:"cpu"`
	RAM     int `json:"ram"`
	Storage int `json:"storage"`
	Network int `json:"network"`
	GPU     int `json:"gpu"`
	PCIe    int `json:"pcie"`
	PSU     int `json:"psu"`
	Fans    int `json:"fans"`
	BMC     int `json:"bmc"`
	Chassis int `json:"chassis"`
}

type PSUInfo struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name,omitempty"`
	Manufacturer       string   `json:"manufacturer,omitempty"`
	Model              string   `json:"model,omitempty"`
	SerialNumber       string   `json:"serial_number,omitempty"`
	PartNumber         string   `json:"part_number,omitempty"`
	FirmwareVersion    string   `json:"firmware_version,omitempty"`
	PowerSupplyType    string   `json:"power_supply_type,omitempty"`
	PowerCapacityWatts *float64 `json:"power_capacity_watts,omitempty"`
	PowerOutputWatts   *float64 `json:"power_output_watts,omitempty"`
	Status             string   `json:"status,omitempty"`
	StatusState        string   `json:"status_state,omitempty"`
	Chassis            string   `json:"chassis,omitempty"`
	Source             string   `json:"source,omitempty"` // power | power_subsystem | chassis
}

type FanInfo struct {
	ID              string   `json:"id"`
	Name            string   `json:"name,omitempty"`
	Manufacturer    string   `json:"manufacturer,omitempty"`
	Model           string   `json:"model,omitempty"`
	SerialNumber    string   `json:"serial_number,omitempty"`
	PartNumber      string   `json:"part_number,omitempty"`
	PhysicalContext string   `json:"physical_context,omitempty"`
	Reading         *float64 `json:"reading,omitempty"`
	ReadingUnits    string   `json:"reading_units,omitempty"` // RPM | Percent
	SpeedPercent    *float64 `json:"speed_percent,omitempty"`
	SpeedRPM        *float64 `json:"speed_rpm,omitempty"`
	Status          string   `json:"status,omitempty"`
	StatusState     string   `json:"status_state,omitempty"`
	Chassis         string   `json:"chassis,omitempty"`
	Source          string   `json:"source,omitempty"` // thermal | thermal_subsystem | chassis
}

type PCIeInfo struct {
	ID              string `json:"id"`
	Name            string `json:"name,omitempty"`
	Description     string `json:"description,omitempty"`
	Manufacturer    string `json:"manufacturer,omitempty"`
	Model           string `json:"model,omitempty"`
	SerialNumber    string `json:"serial_number,omitempty"`
	PartNumber      string `json:"part_number,omitempty"`
	SKU             string `json:"sku,omitempty"`
	AssetTag        string `json:"asset_tag,omitempty"`
	FirmwareVersion string `json:"firmware_version,omitempty"`
	DeviceType      string `json:"device_type,omitempty"`
	Status          string `json:"status,omitempty"`
	StatusState     string `json:"status_state,omitempty"`
	// Slot / link details when the BMC exposes them.
	SlotID       string `json:"slot_id,omitempty"`
	SlotType     string `json:"slot_type,omitempty"`
	PCIeType     string `json:"pcie_type,omitempty"`
	MaxPCIeType  string `json:"max_pcie_type,omitempty"`
	LanesInUse   int    `json:"lanes_in_use,omitempty"`
	MaxLanes     int    `json:"max_lanes,omitempty"`
	IsGPU         bool     `json:"is_gpu"`
	Chassis       string   `json:"chassis,omitempty"`
	Source        string   `json:"source,omitempty"` // system | chassis
	MACAddresses  []string `json:"mac_addresses,omitempty"` // from linked PCIeFunctions when BMC exposes them
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

// ChassisInfo is one Redfish /redfish/v1/Chassis/{id} member.
// SerialNumber maps to live-OS /sys/class/dmi/id/chassis_serial (SMBIOS Type 3).
// BoardSerial (when present) maps to /sys/class/dmi/id/board_serial — Supermicro
// often puts a different FRU there under Oem.*.BoardSerialNumber.
// This is NOT Copper's servers.chassis_id / LLDP chassis id — never conflate them.
type ChassisInfo struct {
	ID           string `json:"id"`
	Name         string `json:"name,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model,omitempty"`
	SerialNumber string `json:"serial_number,omitempty"`
	BoardSerial  string `json:"board_serial,omitempty"`
	PartNumber   string `json:"part_number,omitempty"`
	ChassisType  string `json:"chassis_type,omitempty"`
	AssetTag     string `json:"asset_tag,omitempty"`
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
	Source       string   `json:"source,omitempty"` // system_ethernet | network_adapter | pcie_function | …
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
		PCIe:    []PCIeInfo{},
		PSU:     []PSUInfo{},
		Fans:    []FanInfo{},
		BMC:     []BMCInfo{},
		Chassis: []ChassisInfo{},
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

	inv.Network = collectHostNICs(api, sys)

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

	inv.PCIe = collectPCIe(api, sys)
	for _, d := range inv.PCIe {
		if !d.IsGPU {
			continue
		}
		key := "pcie:" + d.ID
		if _, ok := gpuSeen[key]; ok {
			continue
		}
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

	inv.Storage = collectDrives(api, systems)
	inv.BMC = collectBMC(api)
	inv.Chassis = collectChassis(api)
	inv.PSU = collectPSU(api)
	inv.Fans = collectFans(api)
	// Drop any host-network MAC that is really a BMC / manager link MAC
	// (defense in depth if a vendor names the virtual NIC oddly).
	inv.Network = filterBMCMACsFromHostNICs(inv.Network, inv.BMC)
	inv.Counts = InventoryCounts{
		CPU:     len(inv.CPU),
		RAM:     len(inv.RAM),
		Storage: len(inv.Storage),
		Network: len(inv.Network),
		GPU:     len(inv.GPU),
		PCIe:    len(inv.PCIe),
		PSU:     len(inv.PSU),
		Fans:    len(inv.Fans),
		BMC:     len(inv.BMC),
		Chassis: len(inv.Chassis),
	}

	return inv, nil
}

// collectChassis reads /redfish/v1/Chassis members. SerialNumber is the SMBIOS
// Type 3 / FRU value that Linux exposes as /sys/class/dmi/id/chassis_serial.
// BoardSerial is harvested from Oem.*.BoardSerialNumber when vendors stash it
// there (Supermicro X13, …) — maps to /sys/class/dmi/id/board_serial.
func collectChassis(api *gofish.APIClient) []ChassisInfo {
	out := []ChassisInfo{}
	if api == nil {
		return out
	}
	chassisList, err := api.Service.Chassis()
	if err != nil {
		return out
	}
	for _, ch := range chassisList {
		if ch == nil {
			continue
		}
		out = append(out, ChassisInfo{
			ID:           ch.ID,
			Name:         ch.Name,
			Manufacturer: ch.Manufacturer,
			Model:        ch.Model,
			SerialNumber: ch.SerialNumber,
			BoardSerial:  boardSerialFromOEM(ch.OEM),
			PartNumber:   ch.PartNumber,
			ChassisType:  string(ch.ChassisType),
			AssetTag:     ch.AssetTag,
		})
	}
	return out
}

// boardSerialFromOEM walks Oem JSON for BoardSerialNumber (case-insensitive key).
// Vendor-agnostic: Supermicro uses Oem.Supermicro.BoardSerialNumber; others may nest similarly.
func boardSerialFromOEM(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return ""
	}
	var found string
	var walk func(v any)
	walk = func(v any) {
		if found != "" {
			return
		}
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				if strings.EqualFold(strings.TrimSpace(k), "BoardSerialNumber") {
					if s, ok := child.(string); ok {
						s = strings.TrimSpace(s)
						if s != "" && !strings.EqualFold(s, "none") && !strings.EqualFold(s, "n/a") {
							found = s
							return
						}
					}
				}
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(root)
	return found
}

func filterBMCMACsFromHostNICs(nics []NICInfo, bmcs []BMCInfo) []NICInfo {
	bmcMACs := map[string]struct{}{}
	for _, b := range bmcs {
		for _, m := range b.MACAddresses {
			if u := usableMAC(m); u != "" {
				bmcMACs[strings.ToLower(u)] = struct{}{}
			}
		}
		for _, n := range b.Network {
			if u := usableMAC(n.MACAddress); u != "" {
				bmcMACs[strings.ToLower(u)] = struct{}{}
			}
			if u := usableMAC(n.PermanentMAC); u != "" {
				bmcMACs[strings.ToLower(u)] = struct{}{}
			}
		}
	}
	if len(bmcMACs) == 0 {
		return nics
	}
	out := make([]NICInfo, 0, len(nics))
	for _, n := range nics {
		mac := usableMAC(n.MACAddress)
		if mac == "" {
			mac = usableMAC(n.PermanentMAC)
		}
		if mac != "" {
			if _, ok := bmcMACs[strings.ToLower(mac)]; ok {
				continue
			}
		}
		out = append(out, n)
	}
	return out
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

func collectPCIe(api *gofish.APIClient, sys *schemas.ComputerSystem) []PCIeInfo {
	out := []PCIeInfo{}
	seen := map[string]struct{}{}
	add := func(d *schemas.PCIeDevice, chassis, source string) {
		if d == nil {
			return
		}
		key := d.ID
		if key == "" {
			key = d.Name + "|" + d.SerialNumber + "|" + d.Model
		}
		if key == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		info := PCIeInfo{
			ID:              d.ID,
			Name:            d.Name,
			Description:     d.Description,
			Manufacturer:    d.Manufacturer,
			Model:           d.Model,
			SerialNumber:    d.SerialNumber,
			PartNumber:      d.PartNumber,
			SKU:             d.SKU,
			AssetTag:        d.AssetTag,
			FirmwareVersion: d.FirmwareVersion,
			DeviceType:      string(d.DeviceType),
			Status:          string(d.Status.Health),
			StatusState:     string(d.Status.State),
			PCIeType:        string(d.PCIeInterface.PCIeType),
			MaxPCIeType:     string(d.PCIeInterface.MaxPCIeType),
			LanesInUse:      derefInt(d.PCIeInterface.LanesInUse),
			MaxLanes:        derefInt(d.PCIeInterface.MaxLanes),
			IsGPU:           looksLikeGPU(d.Name, d.Model, d.Description, d.Manufacturer),
			Chassis:         chassis,
			Source:          source,
		}
		if d.Slot.SlotType != "" || d.Slot.PCIeType != "" || d.Slot.Lanes != nil {
			info.SlotType = string(d.Slot.SlotType)
			if info.PCIeType == "" {
				info.PCIeType = string(d.Slot.PCIeType)
			}
			if info.MaxLanes == 0 {
				info.MaxLanes = derefInt(d.Slot.Lanes)
			}
			if sl := d.Slot.Location.PartLocation.ServiceLabel; sl != "" {
				info.SlotID = sl
			}
		}
		info.MACAddresses = macsFromPCIeDevice(d)
		out = append(out, info)
	}

	if devices, err := sys.PCIeDevices(); err == nil {
		for _, d := range devices {
			add(d, "", "system")
		}
	}
	if chassisList, err := api.Service.Chassis(); err == nil {
		for _, ch := range chassisList {
			label := ch.Name
			if label == "" {
				label = ch.ID
			}
			if devices, err := ch.PCIeDevices(); err == nil {
				for _, d := range devices {
					add(d, label, "chassis")
				}
			}
		}
	}
	return out
}

func collectPSU(api *gofish.APIClient) []PSUInfo {
	out := []PSUInfo{}
	seen := map[string]struct{}{}
	add := func(p schemas.PowerSupply, chassis, source string) {
		key := p.MemberID
		if key == "" {
			key = p.ID
		}
		if key == "" {
			key = p.Name + "|" + p.SerialNumber + "|" + p.Model
		}
		if key == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		id := p.ID
		if id == "" {
			id = p.MemberID
		}
		if id == "" {
			id = p.Name
		}
		info := PSUInfo{
			ID:              id,
			Name:            p.Name,
			Manufacturer:    p.Manufacturer,
			Model:           p.Model,
			SerialNumber:    p.SerialNumber,
			PartNumber:      p.PartNumber,
			FirmwareVersion: p.FirmwareVersion,
			PowerSupplyType: string(p.PowerSupplyType),
			Status:          string(p.Status.Health),
			StatusState:     string(p.Status.State),
			Chassis:         chassis,
			Source:          source,
		}
		if p.PowerCapacityWatts != nil {
			v := float64(*p.PowerCapacityWatts)
			info.PowerCapacityWatts = &v
		}
		if p.PowerOutputWatts != nil {
			v := float64(*p.PowerOutputWatts)
			info.PowerOutputWatts = &v
		} else if p.LastPowerOutputWatts != nil {
			v := float64(*p.LastPowerOutputWatts)
			info.PowerOutputWatts = &v
		}
		out = append(out, info)
	}

	chassisList, err := api.Service.Chassis()
	if err != nil {
		return out
	}
	for _, ch := range chassisList {
		label := ch.Name
		if label == "" {
			label = ch.ID
		}
		// Legacy Chassis.Power → PowerSupplies array (common on iLO / older iDRAC).
		if power, err := ch.Power(); err == nil && power != nil {
			for _, p := range power.PowerSupplies {
				add(p, label, "power")
			}
		}
		// Newer Chassis.PowerSubsystem → PowerSupply collection.
		if ps, err := ch.PowerSubsystem(); err == nil && ps != nil {
			if supplies, err := ps.PowerSupplies(); err == nil {
				for _, p := range supplies {
					if p != nil {
						add(*p, label, "power_subsystem")
					}
				}
			}
		}
		// Direct Chassis.PowerSupplies links (some OEMs).
		if supplies, err := ch.PowerSupplies(); err == nil {
			for _, p := range supplies {
				if p != nil {
					add(*p, label, "chassis")
				}
			}
		}
	}
	return out
}

func collectFans(api *gofish.APIClient) []FanInfo {
	out := []FanInfo{}
	seen := map[string]struct{}{}
	addThermal := func(f schemas.ThermalFan, chassis, source string) {
		key := f.MemberID
		if key == "" {
			key = f.ID
		}
		if key == "" {
			key = f.Name + "|" + f.SerialNumber + "|" + f.Model
		}
		if key == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		id := f.ID
		if id == "" {
			id = f.MemberID
		}
		if id == "" {
			id = f.Name
			if id == "" {
				id = f.FanName
			}
		}
		info := FanInfo{
			ID:              id,
			Name:            firstNonEmpty(f.Name, f.FanName),
			Manufacturer:    f.Manufacturer,
			Model:           f.Model,
			SerialNumber:    f.SerialNumber,
			PartNumber:      f.PartNumber,
			PhysicalContext: string(f.PhysicalContext),
			ReadingUnits:    string(f.ReadingUnits),
			Status:          string(f.Status.Health),
			StatusState:     string(f.Status.State),
			Chassis:         chassis,
			Source:          source,
		}
		if f.Reading != nil {
			v := float64(*f.Reading)
			info.Reading = &v
			switch f.ReadingUnits {
			case schemas.RPMReadingUnits:
				info.SpeedRPM = &v
			case schemas.PercentReadingUnits:
				info.SpeedPercent = &v
			}
		}
		out = append(out, info)
	}
	addFan := func(f *schemas.Fan, chassis, source string) {
		if f == nil {
			return
		}
		key := f.ID
		if key == "" {
			key = f.Name + "|" + f.SerialNumber + "|" + f.Model
		}
		if key == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		info := FanInfo{
			ID:              f.ID,
			Name:            f.Name,
			Manufacturer:    f.Manufacturer,
			Model:           f.Model,
			SerialNumber:    f.SerialNumber,
			PartNumber:      f.PartNumber,
			PhysicalContext: string(f.PhysicalContext),
			Status:          string(f.Status.Health),
			StatusState:     string(f.Status.State),
			Chassis:         chassis,
			Source:          source,
		}
		if f.SpeedPercent.Reading != nil {
			info.SpeedPercent = f.SpeedPercent.Reading
			info.Reading = f.SpeedPercent.Reading
			info.ReadingUnits = string(schemas.PercentReadingUnits)
		}
		if f.SpeedPercent.SpeedRPM != nil {
			info.SpeedRPM = f.SpeedPercent.SpeedRPM
			if info.Reading == nil {
				info.Reading = f.SpeedPercent.SpeedRPM
				info.ReadingUnits = string(schemas.RPMReadingUnits)
			}
		}
		out = append(out, info)
	}

	chassisList, err := api.Service.Chassis()
	if err != nil {
		return out
	}
	for _, ch := range chassisList {
		label := ch.Name
		if label == "" {
			label = ch.ID
		}
		// Legacy Chassis.Thermal → Fans array.
		if thermal, err := ch.Thermal(); err == nil && thermal != nil {
			for _, f := range thermal.Fans {
				addThermal(f, label, "thermal")
			}
		}
		// Newer Chassis.ThermalSubsystem → Fan collection (gofish types as ThermalFan).
		if ts, err := ch.ThermalSubsystem(); err == nil && ts != nil {
			if fans, err := ts.Fans(); err == nil {
				for _, f := range fans {
					if f != nil {
						addThermal(*f, label, "thermal_subsystem")
					}
				}
			}
		}
		// Direct Chassis.Fans links (Fan.v1_* resources).
		if fans, err := ch.Fans(); err == nil {
			for _, f := range fans {
				addFan(f, label, "chassis")
			}
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
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
	BootTarget string `json:"boot_target,omitempty"` // optional; else pick from BMC allowable (UsbCd preferred on Supermicro)
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
	enabled := req.Enabled
	if enabled == "" {
		enabled = "Once"
	}
	// Clearing the override (Disabled) does not need a boot target; requiring
	// one forced callers into Hdd Continuous, which fights efibootmgr BootOrder.
	if !strings.EqualFold(enabled, "Disabled") && req.Target == "" {
		return nil, fmt.Errorf("target is required unless enabled=Disabled")
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
		BootSourceOverrideEnabled: schemas.BootSourceOverrideEnabled(enabled),
	}
	if req.Target != "" && !strings.EqualFold(req.Target, "None") {
		boot.BootSourceOverrideTarget = schemas.BootSource(req.Target)
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

// EjectResult summarizes one VirtualMedia slot after a post-provision eject pass.
type EjectResult struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Source      string `json:"source"`
	Image       string `json:"image,omitempty"`
	WasInserted bool   `json:"was_inserted"`
	Ejected     bool   `json:"ejected"`
	Method      string `json:"method,omitempty"` // eject-action | patch-inserted-false | already-empty
	Error       string `json:"error,omitempty"`
}

// EjectAllVirtualMedia ejects every inserted VirtualMedia slot (manager + system).
// Used after successful OS provision so the next reboot does not re-enter the live ISO
// via iLO Virtual CD (BootCurrent often shows that device while provisioning).
func (c *Client) EjectAllVirtualMedia(t Target) (map[string]any, error) {
	c.logf("info", "virtual-media eject-all start bmc=%s", t.Endpoint)

	api, err := c.connect(t)
	if err != nil {
		return nil, fmt.Errorf("redfish connect: %w", err)
	}
	defer api.Logout()

	candidates, err := listVirtualMediaCandidates(api, "")
	if err != nil {
		c.logf("error", "virtual-media eject-all discovery failed: %v", err)
		return nil, err
	}

	results := make([]EjectResult, 0, len(candidates))
	var failN int
	for _, cand := range candidates {
		vm := cand.vm
		res := EjectResult{
			ID:          vm.ID,
			Name:        vm.Name,
			Source:      cand.source,
			Image:       vm.Image,
			WasInserted: derefBool(vm.Inserted),
		}
		if !res.WasInserted && strings.TrimSpace(vm.Image) == "" {
			res.Ejected = true
			res.Method = "already-empty"
			results = append(results, res)
			c.logf("info", "virtual-media eject skip id=%s (already empty)", vm.ID)
			continue
		}
		c.logf("info", "virtual-media eject slot id=%s name=%q source=%s image=%q inserted=%v",
			vm.ID, vm.Name, cand.source, vm.Image, res.WasInserted)

		vm.DisableEtagMatch(true)
		if _, ejectErr := vm.EjectMedia(); ejectErr == nil {
			res.Ejected = true
			res.Method = "eject-action"
			c.logf("info", "virtual-media eject ok method=eject-action id=%s", vm.ID)
		} else {
			c.logf("warn", "virtual-media eject-action soft-fail id=%s: %s — trying PATCH clear",
				vm.ID, shortRedfishErr(ejectErr.Error()))
			patched := false
			for _, body := range []map[string]any{
				{"Inserted": false, "Image": nil},
				{"Inserted": false},
				{"Image": nil},
			} {
				if patchErr := vm.Patch(vm.ODataID, body); patchErr != nil {
					c.logf("warn", "virtual-media eject PATCH id=%s body=%v: %s",
						vm.ID, body, shortRedfishErr(patchErr.Error()))
					continue
				}
				res.Ejected = true
				res.Method = "patch-clear"
				patched = true
				c.logf("info", "virtual-media eject ok method=patch-clear id=%s", vm.ID)
				break
			}
			if !patched {
				res.Error = fmt.Sprintf("could not eject: %s", shortRedfishErr(ejectErr.Error()))
				failN++
				c.logf("warn", "virtual-media eject incomplete id=%s — admin can retry or clear in BMC UI", vm.ID)
			}
		}
		results = append(results, res)
	}

	out := map[string]any{
		"ejected": results,
		"ok":      failN == 0,
	}
	if failN > 0 {
		c.logf("warn", "virtual-media eject-all partial bmc=%s cleared=%d failed=%d",
			t.Endpoint, len(results)-failN, failN)
		return out, fmt.Errorf(
			"ejected most slots, but %d still look busy — open BMC virtual media and eject manually if Mount keeps failing",
			failN)
	}
	c.logf("info", "virtual-media eject-all ok bmc=%s slots=%d", t.Endpoint, len(results))
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
	resetType := req.ResetType
	if resetType == "" {
		resetType = string(schemas.ForceRestartResetType)
	}

	c.logf("info", "virtual-media mount start bmc=%s image=%s boot_target_req=%q boot_once=%v reboot=%v device_id=%q",
		t.Endpoint, req.ImageURL, req.BootTarget, bootOnce, req.Reboot, req.DeviceID)

	api, err := c.connect(t)
	if err != nil {
		return nil, fmt.Errorf("redfish connect: %w", err)
	}
	defer api.Logout()

	candidates, err := listVirtualMediaCandidates(api, req.DeviceID)
	if err != nil {
		c.logf("error", "virtual-media device discovery failed: %v", err)
		return nil, err
	}
	c.logf("info", "virtual-media discovered %d candidate slot(s)", len(candidates))
	for i, cand := range candidates {
		c.logf("info", "virtual-media candidate[%d] id=%s name=%q source=%s inserted=%v types=%v supports_insert=%v",
			i, cand.vm.ID, cand.vm.Name, cand.source, derefBool(cand.vm.Inserted), vmMediaTypes(cand.vm), cand.vm.SupportsMediaInsert)
	}

	var (
		vm       *schemas.VirtualMedia
		source   string
		method   string
		attempts []insertAttempt
		lastErr  error
	)
	for i, cand := range candidates {
		c.logf("info", "virtual-media trying slot %d/%d id=%s name=%q source=%s",
			i+1, len(candidates), cand.vm.ID, cand.vm.Name, cand.source)
		m, atts, insertErr := c.insertMediaRobust(cand.vm, cand.source, req.ImageURL)
		attempts = append(attempts, atts...)
		if insertErr == nil {
			vm, source, method = cand.vm, cand.source, m
			lastErr = nil
			c.logf("info", "virtual-media slot %s accepted insert via %s", cand.vm.ID, m)
			break
		}
		lastErr = insertErr
		c.logf("warn", "virtual-media slot %s did not accept media — trying next if any (%s)",
			cand.vm.ID, shortRedfishErr(insertErr.Error()))
	}
	if vm == nil {
		c.logf("error", "virtual-media mount failed on all slots for bmc=%s slots=%d attempts=%d",
			t.Endpoint, len(candidates), len(attempts))
		return nil, fmt.Errorf("%s", politeMountFailure(len(candidates), len(attempts), lastErr))
	}

	sys, err := firstSystem(api)
	if err != nil {
		return nil, err
	}

	var allowed []string
	for _, at := range sys.Boot.AllowableBootSourceOverrideTargetValues {
		allowed = append(allowed, string(at))
	}
	bootTargets := mediaBootTargetCandidates(req.BootTarget, allowed, sys.Manufacturer)
	c.logf("info", "virtual-media insert settled method=%s device=%s manufacturer=%q allowed_targets=%v trying=%v once=%v",
		method, vmLabel(vm), sys.Manufacturer, allowed, bootTargets, bootOnce)

	enabled := schemas.OnceBootSourceOverrideEnabled
	if !bootOnce {
		enabled = schemas.ContinuousBootSourceOverrideEnabled
	}

	// Always request UEFI for the one-shot media boot so the live OS has
	// /sys/firmware/efi (needed for efibootmgr). Ignored if BIOS is pure Legacy
	// — use ConvertToUEFI for the persistent BootModeSelect/BootMode change.
	bootMode := schemas.UEFIBootSourceOverrideMode
	var bootTarget string
	var bootErr error
	var modeApplied bool
	for _, cand := range bootTargets {
		bootErr = sys.SetBoot(&schemas.Boot{
			BootSourceOverrideTarget:  schemas.BootSource(cand),
			BootSourceOverrideEnabled: enabled,
			BootSourceOverrideMode:    bootMode,
		})
		if bootErr == nil {
			bootTarget = cand
			modeApplied = true
			break
		}
		c.logf("warn", "virtual-media set boot target=%s mode=UEFI failed: %v", cand, bootErr)
	}
	if bootTarget == "" {
		// Some BMCs reject BootSourceOverrideMode; retry without so Mount still works.
		c.logf("warn", "virtual-media UEFI override mode rejected on all targets — retrying without mode")
		for _, cand := range bootTargets {
			bootErr = sys.SetBoot(&schemas.Boot{
				BootSourceOverrideTarget:  schemas.BootSource(cand),
				BootSourceOverrideEnabled: enabled,
			})
			if bootErr == nil {
				bootTarget = cand
				break
			}
			c.logf("warn", "virtual-media set boot target=%s failed: %v", cand, bootErr)
		}
	}
	if bootTarget == "" {
		if bootErr == nil {
			bootErr = fmt.Errorf("no boot target candidates")
		}
		c.logf("error", "virtual-media set boot failed on all targets %v: %v", bootTargets, bootErr)
		return nil, fmt.Errorf("set one-time boot to virtual media: %w", bootErr)
	}
	c.logf("info", "virtual-media boot override set ok target=%s enabled=%s mode_uefi=%v", bootTarget, enabled, modeApplied)

	result := map[string]any{
		"image_url":       req.ImageURL,
		"virtual_media":   mapVM(vm, source),
		"insert_method":   method,
		"insert_attempts": attempts,
		"boot_target":     bootTarget,
		"boot_targets":    bootTargets,
		"boot_once":       bootOnce,
		"boot_mode":       "UEFI",
		"boot_mode_set":   modeApplied,
		"rebooted":        false,
	}

	if req.Reboot {
		c.logf("info", "virtual-media issuing reset type=%s", resetType)
		if _, err := sys.Reset(schemas.ResetType(resetType)); err != nil {
			c.logf("error", "virtual-media reset failed after successful mount: %v", err)
			return nil, fmt.Errorf("media mounted and boot set, but reset failed: %w", err)
		}
		result["rebooted"] = true
		result["reset_type"] = resetType
		c.logf("info", "virtual-media reset accepted type=%s", resetType)
	}

	c.logf("info", "virtual-media mount complete bmc=%s method=%s rebooted=%v", t.Endpoint, method, req.Reboot)
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

type vmCandidate struct {
	vm     *schemas.VirtualMedia
	source string
}

func findVirtualMedia(api *gofish.APIClient, deviceID string) (*schemas.VirtualMedia, string, error) {
	cands, err := listVirtualMediaCandidates(api, deviceID)
	if err != nil {
		return nil, "", err
	}
	return cands[0].vm, cands[0].source, nil
}

// listVirtualMediaCandidates returns VirtualMedia slots in preference order
// (free CD/DVD first, then any CD/DVD, then remaining slots).
func listVirtualMediaCandidates(api *gofish.APIClient, deviceID string) ([]vmCandidate, error) {
	var all []vmCandidate

	if managers, err := api.Service.Managers(); err == nil {
		for _, m := range managers {
			vms, err := m.VirtualMedia()
			if err != nil {
				continue
			}
			for _, vm := range vms {
				all = append(all, vmCandidate{vm: vm, source: "manager:" + m.ID})
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
				all = append(all, vmCandidate{vm: vm, source: "system:" + s.ID})
			}
		}
	}

	if len(all) == 0 {
		return nil, fmt.Errorf("no VirtualMedia devices found on BMC")
	}

	if deviceID != "" {
		for _, cand := range all {
			if cand.vm.ID == deviceID || cand.vm.Name == deviceID {
				return []vmCandidate{cand}, nil
			}
		}
		return nil, fmt.Errorf("virtual media device %q not found", deviceID)
	}

	var freeCD, busyCD, other []vmCandidate
	for _, cand := range all {
		switch {
		case supportsCD(cand.vm) && !derefBool(cand.vm.Inserted):
			freeCD = append(freeCD, cand)
		case supportsCD(cand.vm):
			busyCD = append(busyCD, cand)
		default:
			other = append(other, cand)
		}
	}
	out := append(append(freeCD, busyCD...), other...)
	return out, nil
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
