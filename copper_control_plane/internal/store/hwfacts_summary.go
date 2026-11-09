package store

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// inventory shapes used only for summarization (subset of Redfish inventory JSON).
type invRoot struct {
	CPU     []invCPU   `json:"cpu"`
	RAM     []invRAM   `json:"ram"`
	Storage []invDrive `json:"storage"`
	Network []invNIC   `json:"network"`
	GPU     []invGPU   `json:"gpu"`
	PSU     []invPSU   `json:"psu"`
	BMC     []invBMC   `json:"bmc"`
}

type invCPU struct {
	ID           string `json:"id"`
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	TotalCores   int    `json:"total_cores"`
	TotalThreads int    `json:"total_threads"`
}

type invRAM struct {
	ID          string `json:"id"`
	CapacityMiB int    `json:"capacity_mib"`
}

type invDrive struct {
	ID            string `json:"id"`
	SerialNumber  string `json:"serial_number"`
	MediaType     string `json:"media_type"`
	Protocol      string `json:"protocol"`
	CapacityGiB   int64  `json:"capacity_gib"`
	CapacityBytes int64  `json:"capacity_bytes"`
}

type invNIC struct {
	ID           string `json:"id"`
	MACAddress   string `json:"mac_address"`
	PermanentMAC string `json:"permanent_mac"`
	Source       string `json:"source"`
}

type invGPU struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	SerialNumber string `json:"serial_number"`
}

type invPSU struct {
	ID           string `json:"id"`
	SerialNumber string `json:"serial_number"`
	Model        string `json:"model"`
}

type invBMC struct {
	MACAddresses []string `json:"mac_addresses"`
	Network      []invNIC `json:"network"`
}

// SummarizeInventory builds deduped HardwareFacts from a Redfish inventory JSON payload.
func SummarizeInventory(inventoryJSON string) (HardwareFacts, error) {
	var facts HardwareFacts
	raw := strings.TrimSpace(inventoryJSON)
	if raw == "" || raw == "null" || raw == "{}" {
		return facts, nil
	}
	var root invRoot
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return facts, fmt.Errorf("parse inventory: %w", err)
	}

	facts.CPUCount, facts.CPUModels, facts.CPUCores, facts.CPUThreads = summarizeCPUs(root.CPU)
	facts.CPUSummary = formatCountModels(facts.CPUCount, facts.CPUModels)

	facts.RAMModules, facts.RAMGiB = summarizeRAM(root.RAM)
	if facts.RAMGiB > 0 {
		facts.RAMSummary = fmt.Sprintf("%d GiB", facts.RAMGiB)
	} else if facts.RAMModules > 0 {
		facts.RAMSummary = fmt.Sprintf("%d modules", facts.RAMModules)
	}

	facts.GPUCount, facts.GPUModels = summarizeGPUs(root.GPU)
	facts.HasGPU = facts.GPUCount > 0
	facts.GPUSummary = formatCountModels(facts.GPUCount, facts.GPUModels)

	bmcMACs := bmcMACSet(root.BMC)
	facts.NICCount = summarizeNICs(root.Network, bmcMACs)
	if facts.NICCount > 0 {
		facts.NICSummary = fmt.Sprintf("NIC×%d", facts.NICCount)
	}

	facts.PSUCount = summarizePSUs(root.PSU)
	if facts.PSUCount > 0 {
		facts.PSUSummary = fmt.Sprintf("PSU×%d", facts.PSUCount)
	}

	facts.Storage, facts.DriveCount = summarizeStorage(root.Storage)
	facts.StorageSummary = formatStorageSummary(facts.Storage)
	return facts, nil
}

func summarizeCPUs(cpus []invCPU) (count int, models []string, cores, threads int) {
	seen := map[string]struct{}{}
	modelCount := map[string]int{}
	modelOrder := []string{}
	for _, c := range cpus {
		key := dedupeKey(c.ID, c.Model, fmt.Sprintf("%d", c.TotalCores))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		count++
		cores += c.TotalCores
		threads += c.TotalThreads
		model := strings.TrimSpace(c.Model)
		if model == "" {
			model = strings.TrimSpace(c.Manufacturer)
		}
		if model == "" {
			model = "CPU"
		}
		if _, ok := modelCount[model]; !ok {
			modelOrder = append(modelOrder, model)
		}
		modelCount[model]++
	}
	models = make([]string, 0, len(modelOrder))
	for _, m := range modelOrder {
		models = append(models, m)
	}
	return count, models, cores, threads
}

func summarizeRAM(rams []invRAM) (modules, gib int) {
	seen := map[string]struct{}{}
	totalMiB := 0
	for i, r := range rams {
		key := strings.TrimSpace(r.ID)
		if key == "" {
			key = fmt.Sprintf("ram-%d-%d", i, r.CapacityMiB)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		modules++
		totalMiB += r.CapacityMiB
	}
	gib = totalMiB / 1024
	return modules, gib
}

func summarizeGPUs(gpus []invGPU) (count int, models []string) {
	seen := map[string]struct{}{}
	modelCount := map[string]int{}
	modelOrder := []string{}
	for _, g := range gpus {
		key := dedupeKey(g.SerialNumber, g.ID, g.Model, g.Name)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		count++
		model := strings.TrimSpace(g.Model)
		if model == "" {
			model = strings.TrimSpace(g.Name)
		}
		if model == "" {
			model = strings.TrimSpace(g.Manufacturer)
		}
		if model == "" {
			model = "GPU"
		}
		if _, ok := modelCount[model]; !ok {
			modelOrder = append(modelOrder, model)
		}
		modelCount[model]++
	}
	models = append([]string{}, modelOrder...)
	return count, models
}

func summarizeNICs(nics []invNIC, bmcMACs map[string]struct{}) int {
	seen := map[string]struct{}{}
	count := 0
	for i, n := range nics {
		mac := normalizeMAC(n.PermanentMAC)
		if mac == "" {
			mac = normalizeMAC(n.MACAddress)
		}
		if mac != "" {
			if _, isBMC := bmcMACs[mac]; isBMC {
				continue
			}
			if _, ok := seen[mac]; ok {
				continue
			}
			seen[mac] = struct{}{}
			count++
			continue
		}
		key := strings.TrimSpace(n.ID)
		if key == "" {
			key = fmt.Sprintf("nic-%d", i)
		}
		key = "id:" + strings.ToLower(key)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		count++
	}
	return count
}

func summarizePSUs(psus []invPSU) int {
	seen := map[string]struct{}{}
	count := 0
	for _, p := range psus {
		key := dedupeKey(p.SerialNumber, p.ID, p.Model)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		count++
	}
	return count
}

func summarizeStorage(drives []invDrive) ([]StorageFact, int) {
	seen := map[string]struct{}{}
	type agg struct {
		media, proto string
		count        int
		capGiB       int64
	}
	buckets := map[string]*agg{}
	order := []string{}
	total := 0
	for i, d := range drives {
		key := dedupeKey(d.SerialNumber, d.ID)
		if key == "" {
			key = fmt.Sprintf("drive-%d-%s-%s-%d", i, d.MediaType, d.Protocol, d.CapacityGiB)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		total++
		bucket := storageKey(d.MediaType, d.Protocol)
		a, ok := buckets[bucket]
		if !ok {
			a = &agg{media: strings.TrimSpace(d.MediaType), proto: strings.TrimSpace(d.Protocol)}
			buckets[bucket] = a
			order = append(order, bucket)
		}
		a.count++
		gib := d.CapacityGiB
		if gib <= 0 && d.CapacityBytes > 0 {
			gib = d.CapacityBytes / (1024 * 1024 * 1024)
		}
		a.capGiB += gib
	}
	sort.SliceStable(order, func(i, j int) bool {
		return storageKeyRank(order[i]) < storageKeyRank(order[j])
	})
	out := make([]StorageFact, 0, len(order))
	for _, k := range order {
		a := buckets[k]
		out = append(out, StorageFact{
			Key:         k,
			MediaType:   a.media,
			Protocol:    a.proto,
			Count:       a.count,
			CapacityGiB: a.capGiB,
		})
	}
	return out, total
}

func storageKey(mediaType, protocol string) string {
	media := strings.ToUpper(strings.TrimSpace(mediaType))
	proto := strings.ToUpper(strings.TrimSpace(protocol))
	proto = strings.ReplaceAll(proto, " ", "")
	if strings.Contains(proto, "NVME") || media == "NVME" {
		return "NVMe"
	}
	switch media {
	case "HDD":
		return "HDD"
	case "SSD":
		switch {
		case strings.Contains(proto, "SATA"):
			return "SATA-SSD"
		case strings.Contains(proto, "SAS"):
			return "SAS-SSD"
		default:
			return "SSD"
		}
	case "":
		if proto != "" {
			return proto
		}
		return "Other"
	default:
		return media
	}
}

func storageKeyRank(key string) int {
	switch strings.ToUpper(key) {
	case "HDD":
		return 1
	case "SAS-SSD":
		return 2
	case "SATA-SSD":
		return 3
	case "SSD":
		return 4
	case "NVME":
		return 5
	case "OTHER":
		return 90
	default:
		return 50
	}
}

func formatStorageSummary(items []StorageFact) string {
	if len(items) == 0 {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, s := range items {
		parts = append(parts, fmt.Sprintf("%s×%d", s.Key, s.Count))
	}
	return strings.Join(parts, " · ")
}

func formatCountModels(count int, models []string) string {
	if count <= 0 {
		return ""
	}
	if len(models) == 0 {
		return fmt.Sprintf("×%d", count)
	}
	if len(models) == 1 {
		if count == 1 {
			return models[0]
		}
		return fmt.Sprintf("%d× %s", count, models[0])
	}
	// Multiple distinct models — show count then short join.
	return fmt.Sprintf("%d× (%s)", count, strings.Join(models, ", "))
}

func dedupeKey(parts ...string) string {
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			return strings.ToLower(p)
		}
	}
	return ""
}

func normalizeMAC(mac string) string {
	mac = strings.ToLower(strings.TrimSpace(mac))
	mac = strings.ReplaceAll(mac, "-", ":")
	mac = strings.ReplaceAll(mac, ".", "")
	if mac == "" || mac == "00:00:00:00:00:00" {
		return ""
	}
	return mac
}

func bmcMACSet(bmcs []invBMC) map[string]struct{} {
	out := map[string]struct{}{}
	for _, b := range bmcs {
		for _, m := range b.MACAddresses {
			if mac := normalizeMAC(m); mac != "" {
				out[mac] = struct{}{}
			}
		}
		for _, n := range b.Network {
			if mac := normalizeMAC(n.PermanentMAC); mac != "" {
				out[mac] = struct{}{}
			}
			if mac := normalizeMAC(n.MACAddress); mac != "" {
				out[mac] = struct{}{}
			}
		}
	}
	return out
}
