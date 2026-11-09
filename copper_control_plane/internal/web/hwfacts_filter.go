package web

import (
	"net/url"
	"strconv"
	"strings"

	"copper_control_plane/internal/store"
)

// hardwareListFilter is AND-combined structured inventory query criteria.
type hardwareListFilter struct {
	CPUType   string // substring match against CPU model(s)
	CPUMin    int    // min socket count; 0 = ignore
	RAMGiBMin int    // 0 = ignore
	RAMGiBMax int    // 0 = ignore
	GPU       string // "", "any"/"yes", "none"/"no", or model substring
	GPUType   string // model substring (also set when GPU is a free model string)
	NICMin    int
	PSUMin    int
	HDDMin    int
	NVMeMin   int
	SSDMin    int // any SSD family (NVMe + SATA-SSD + SAS-SSD + SSD)
}

func parseHardwareListFilter(q url.Values) hardwareListFilter {
	f := hardwareListFilter{
		CPUType: strings.TrimSpace(q.Get("cpu")),
		GPU:     strings.TrimSpace(q.Get("gpu")),
		GPUType: strings.TrimSpace(q.Get("gpu_type")),
	}
	f.CPUMin = atoiOrZero(q.Get("cpu_min"))
	f.RAMGiBMin = atoiOrZero(q.Get("ram_min"))
	f.RAMGiBMax = atoiOrZero(q.Get("ram_max"))
	f.NICMin = atoiOrZero(q.Get("nic_min"))
	f.PSUMin = atoiOrZero(q.Get("psu_min"))
	f.HDDMin = atoiOrZero(q.Get("hdd_min"))
	f.NVMeMin = atoiOrZero(q.Get("nvme_min"))
	f.SSDMin = atoiOrZero(q.Get("ssd_min"))

	// gpu=A100 means model filter; gpu=any/none are presence filters.
	g := strings.ToLower(f.GPU)
	switch g {
	case "", "any", "yes", "1", "true", "none", "no", "0", "false":
		// keep as presence / empty
	default:
		if f.GPUType == "" {
			f.GPUType = f.GPU
		}
		f.GPU = "any"
	}
	return f
}

func (f hardwareListFilter) Active() bool {
	return f.CPUType != "" || f.CPUMin > 0 || f.RAMGiBMin > 0 || f.RAMGiBMax > 0 ||
		f.GPU != "" || f.GPUType != "" || f.NICMin > 0 || f.PSUMin > 0 ||
		f.HDDMin > 0 || f.NVMeMin > 0 || f.SSDMin > 0
}

func (f hardwareListFilter) Match(hw *store.HardwareFacts) bool {
	if !f.Active() {
		return true
	}
	// No inventory yet → fail closed when any hardware criterion is set.
	if hw == nil {
		return false
	}
	if f.CPUType != "" && !modelsContain(hw.CPUModels, hw.CPUSummary, f.CPUType) {
		return false
	}
	if f.CPUMin > 0 && hw.CPUCount < f.CPUMin {
		return false
	}
	if f.RAMGiBMin > 0 && hw.RAMGiB < f.RAMGiBMin {
		return false
	}
	if f.RAMGiBMax > 0 && hw.RAMGiB > f.RAMGiBMax {
		return false
	}
	if f.GPUType != "" && !modelsContain(hw.GPUModels, hw.GPUSummary, f.GPUType) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(f.GPU)) {
	case "any", "yes", "1", "true":
		if !hw.HasGPU {
			return false
		}
	case "none", "no", "0", "false":
		if hw.HasGPU {
			return false
		}
	}
	if f.NICMin > 0 && hw.NICCount < f.NICMin {
		return false
	}
	if f.PSUMin > 0 && hw.PSUCount < f.PSUMin {
		return false
	}
	if f.HDDMin > 0 && hw.StorageCount("HDD") < f.HDDMin {
		return false
	}
	if f.NVMeMin > 0 && hw.StorageCount("NVMe") < f.NVMeMin {
		return false
	}
	if f.SSDMin > 0 && hw.SSDCount() < f.SSDMin {
		return false
	}
	return true
}

func modelsContain(models []string, summary, needle string) bool {
	needle = strings.ToLower(strings.TrimSpace(needle))
	if needle == "" {
		return true
	}
	if strings.Contains(strings.ToLower(summary), needle) {
		return true
	}
	for _, m := range models {
		if strings.Contains(strings.ToLower(m), needle) {
			return true
		}
	}
	return false
}

func atoiOrZero(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
