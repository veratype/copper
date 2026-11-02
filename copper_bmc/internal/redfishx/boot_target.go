package redfishx

import "strings"

// Preferred media boot targets. Supermicro BMC firmware often lists / honors
// UsbCd (and sometimes UefiCd) for Virtual Media while accepting Cd on PATCH
// yet still booting the local disk. HPE iLO typically only allows Cd.
var (
	bootTargetPrefDefault = []string{"Cd", "UsbCd", "UefiCd", "Usb", "RemoteDrive"}
	bootTargetPrefSuper   = []string{"UsbCd", "UefiCd", "Cd", "Usb", "RemoteDrive"}
)

func isSupermicroManufacturer(manufacturer string) bool {
	return strings.Contains(strings.ToLower(manufacturer), "supermicro")
}

func normalizeBootTarget(s string) string {
	return strings.TrimSpace(s)
}

func bootTargetAllowedSet(allowed []string) map[string]string {
	out := map[string]string{}
	for _, a := range allowed {
		a = normalizeBootTarget(a)
		if a == "" {
			continue
		}
		out[strings.ToLower(a)] = a
	}
	return out
}

// mediaBootTargetCandidates returns ordered BootSourceOverrideTarget values to
// try for virtual-media boot. requested (if set) is tried first. When the BMC
// advertises allowable targets, candidates are filtered to that set. Empty
// allowable lists keep the full preference (flaky BMCs).
func mediaBootTargetCandidates(requested string, allowed []string, manufacturer string) []string {
	pref := bootTargetPrefDefault
	if isSupermicroManufacturer(manufacturer) {
		pref = bootTargetPrefSuper
	}

	allow := bootTargetAllowedSet(allowed)
	filter := len(allow) > 0

	var out []string
	seen := map[string]bool{}
	add := func(t string) {
		t = normalizeBootTarget(t)
		if t == "" {
			return
		}
		key := strings.ToLower(t)
		if seen[key] {
			return
		}
		if filter {
			canon, ok := allow[key]
			if !ok {
				return
			}
			t = canon
		}
		seen[key] = true
		out = append(out, t)
	}

	add(requested)
	for _, t := range pref {
		add(t)
	}

	// Caller asked for a target the BMC did not list — still try it first so
	// an explicit boot_target is never silently dropped, then fall back.
	if req := normalizeBootTarget(requested); req != "" && !seen[strings.ToLower(req)] {
		out = append([]string{req}, out...)
	}

	// Allowable list filtered everything away (odd BMC) — use unfiltered pref.
	if len(out) == 0 {
		for _, t := range pref {
			t = normalizeBootTarget(t)
			if t == "" || seen[strings.ToLower(t)] {
				continue
			}
			seen[strings.ToLower(t)] = true
			out = append(out, t)
		}
	}
	return out
}
