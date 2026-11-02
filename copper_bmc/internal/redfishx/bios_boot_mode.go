package redfishx

import (
	"fmt"
	"strings"

	"github.com/stmcginnis/gofish/schemas"
)

// ConvertToUEFIRequest stages persistent BIOS boot mode = UEFI and optionally reboots.
// Reboot defaults to true when nil (pending BIOS attrs apply on reset).
type ConvertToUEFIRequest struct {
	Reboot    *bool  `json:"reboot,omitempty"`
	ResetType string `json:"reset_type,omitempty"` // default ForceRestart
}

func (r ConvertToUEFIRequest) rebootOrDefault() bool {
	if r.Reboot == nil {
		return true
	}
	return *r.Reboot
}

// ConvertToUEFIResult is returned by ConvertToUEFI.
type ConvertToUEFIResult struct {
	Manufacturer  string `json:"manufacturer,omitempty"`
	Attribute     string `json:"attribute"`
	PreviousValue string `json:"previous_value,omitempty"`
	PendingValue  string `json:"pending_value"`
	AlreadyUEFI   bool   `json:"already_uefi"`
	Patched       bool   `json:"patched"`
	SettingsURI   string `json:"settings_uri,omitempty"`
	Rebooted      bool   `json:"rebooted"`
	ResetType     string `json:"reset_type,omitempty"`
	Note          string `json:"note,omitempty"`
}

// bootModeAttr describes a vendor BIOS attribute that selects UEFI vs Legacy.
type bootModeAttr struct {
	Key      string
	UEFIVal  string
	IsUEFI   func(cur string) bool
}

var knownBootModeAttrs = []bootModeAttr{
	// Supermicro X11/X12/X13: BootModeSelect = Legacy | UEFI | Dual
	{
		Key:     "BootModeSelect",
		UEFIVal: "UEFI",
		IsUEFI: func(cur string) bool {
			c := strings.ToLower(strings.TrimSpace(cur))
			return c == "uefi"
		},
	},
	// HPE iLO: BootMode = Uefi | LegacyBios
	{
		Key:     "BootMode",
		UEFIVal: "Uefi",
		IsUEFI: func(cur string) bool {
			c := strings.ToLower(strings.TrimSpace(cur))
			return c == "uefi"
		},
	},
}

// resolveBootModeAttr picks the boot-mode attribute present on this Bios resource.
func resolveBootModeAttr(attrs schemas.SettingsAttributes) (bootModeAttr, string, error) {
	if attrs == nil {
		return bootModeAttr{}, "", fmt.Errorf("BIOS Attributes empty — BMC may not expose BIOS config over Redfish")
	}
	for _, cand := range knownBootModeAttrs {
		raw, ok := attrs[cand.Key]
		if !ok {
			continue
		}
		return cand, fmt.Sprint(raw), nil
	}
	// Soft fallback: any attribute whose name looks like boot mode.
	for key, raw := range attrs {
		lk := strings.ToLower(key)
		if strings.Contains(lk, "bootmode") || strings.Contains(lk, "boot_mode") {
			val := fmt.Sprint(raw)
			return bootModeAttr{
				Key:     key,
				UEFIVal: guessUEFIValue(val),
				IsUEFI: func(cur string) bool {
					c := strings.ToLower(strings.TrimSpace(cur))
					return c == "uefi"
				},
			}, val, nil
		}
	}
	return bootModeAttr{}, "", fmt.Errorf(
		"no known BIOS boot-mode attribute (tried BootModeSelect, BootMode) — set UEFI in BIOS UI or check Redfish license")
}

func guessUEFIValue(current string) string {
	c := strings.ToLower(strings.TrimSpace(current))
	switch {
	case strings.Contains(c, "legacy"):
		if strings.Contains(current, "Bios") || strings.HasPrefix(current, "L") {
			return "Uefi" // HPE-style casing nearby
		}
		return "UEFI"
	case c == "dual":
		return "UEFI"
	default:
		return "UEFI"
	}
}

func looksLikeLicenseError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, needle := range []string{
		"license",
		"sft-dcms",
		"sft-oob",
		"not licensed",
		"unlicensed",
		"feature not",
		"insufficientprivilege",
		"oem.supermicro",
	} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func wrapBIOSLicenseHint(err error) error {
	if err == nil {
		return nil
	}
	if looksLikeLicenseError(err) {
		return fmt.Errorf("%w — Supermicro often requires SFT-DCMS-SINGLE (or SFT-OOB-LIC) for BIOS Redfish; set Boot Mode to UEFI (or Dual) in the BIOS UI instead", err)
	}
	return err
}

// ConvertToUEFI PATCHes pending BIOS attributes so the next reset boots in UEFI mode.
func (c *Client) ConvertToUEFI(t Target, req ConvertToUEFIRequest) (*ConvertToUEFIResult, error) {
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

	bios, err := sys.Bios()
	if err != nil || bios == nil {
		if err == nil {
			err = fmt.Errorf("Bios resource missing")
		}
		return nil, wrapBIOSLicenseHint(fmt.Errorf("get Bios: %w", err))
	}

	attr, prev, err := resolveBootModeAttr(bios.Attributes)
	if err != nil {
		return nil, err
	}

	out := &ConvertToUEFIResult{
		Manufacturer:  sys.Manufacturer,
		Attribute:     attr.Key,
		PreviousValue: prev,
		PendingValue:  attr.UEFIVal,
		SettingsURI:   bios.ODataID,
		AlreadyUEFI:   attr.IsUEFI(prev),
	}

	doReboot := req.rebootOrDefault()
	c.logf("info", "bios convert-uefi bmc=%s mfr=%q attr=%s current=%q target=%q already=%v reboot=%v",
		t.Endpoint, sys.Manufacturer, attr.Key, prev, attr.UEFIVal, out.AlreadyUEFI, doReboot)

	if out.AlreadyUEFI {
		out.Note = "BIOS already reports UEFI; no attribute PATCH needed"
		if doReboot {
			if _, err := sys.Reset(schemas.ResetType(resetType)); err != nil {
				return nil, fmt.Errorf("already UEFI but reset failed: %w", err)
			}
			out.Rebooted = true
			out.ResetType = resetType
		}
		return out, nil
	}

	applyTimes := bios.AllowedAttributeUpdateApplyTimes()
	apply := schemas.OnResetSettingsApplyTime
	hasOnReset := false
	for _, t := range applyTimes {
		if t == schemas.OnResetSettingsApplyTime {
			hasOnReset = true
			break
		}
	}
	if !hasOnReset && len(applyTimes) > 0 {
		apply = applyTimes[0]
	}

	err = bios.UpdateBiosAttributesApplyAt(
		schemas.SettingsAttributes{attr.Key: attr.UEFIVal},
		apply,
	)
	if err != nil {
		c.logf("error", "bios convert-uefi PATCH failed bmc=%s: %v", t.Endpoint, err)
		return nil, wrapBIOSLicenseHint(fmt.Errorf("PATCH BIOS %s=%s: %w", attr.Key, attr.UEFIVal, err))
	}
	out.Patched = true
	out.Note = fmt.Sprintf("staged %s=%s (ApplyTime=%s); takes effect on next reset", attr.Key, attr.UEFIVal, apply)
	c.logf("info", "bios convert-uefi PATCH ok bmc=%s %s", t.Endpoint, out.Note)

	if doReboot {
		if _, err := sys.Reset(schemas.ResetType(resetType)); err != nil {
			return out, fmt.Errorf("BIOS UEFI staged, but reset failed: %w", err)
		}
		out.Rebooted = true
		out.ResetType = resetType
		c.logf("info", "bios convert-uefi reset accepted type=%s", resetType)
	}
	return out, nil
}
