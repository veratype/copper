package redfishx

import (
	"errors"
	"testing"

	"github.com/stmcginnis/gofish/schemas"
)

func TestResolveBootModeAttrSupermicro(t *testing.T) {
	attr, prev, err := resolveBootModeAttr(schemas.SettingsAttributes{
		"BootModeSelect": "Legacy",
		"QuietBoot":      "Enabled",
	})
	if err != nil {
		t.Fatal(err)
	}
	if attr.Key != "BootModeSelect" || attr.UEFIVal != "UEFI" || prev != "Legacy" {
		t.Fatalf("got key=%s uefi=%s prev=%s", attr.Key, attr.UEFIVal, prev)
	}
	if attr.IsUEFI(prev) {
		t.Fatal("Legacy should not count as UEFI")
	}
	if !attr.IsUEFI("UEFI") {
		t.Fatal("UEFI should count as UEFI")
	}
}

func TestResolveBootModeAttrHPE(t *testing.T) {
	attr, prev, err := resolveBootModeAttr(schemas.SettingsAttributes{
		"BootMode": "LegacyBios",
	})
	if err != nil {
		t.Fatal(err)
	}
	if attr.Key != "BootMode" || attr.UEFIVal != "Uefi" || prev != "LegacyBios" {
		t.Fatalf("got key=%s uefi=%s prev=%s", attr.Key, attr.UEFIVal, prev)
	}
	if attr.IsUEFI(prev) || !attr.IsUEFI("Uefi") {
		t.Fatalf("IsUEFI broken for HPE values")
	}
}

func TestResolveBootModeAttrPrefersSupermicroKey(t *testing.T) {
	// If both somehow appear, BootModeSelect wins (listed first).
	attr, _, err := resolveBootModeAttr(schemas.SettingsAttributes{
		"BootMode":       "Uefi",
		"BootModeSelect": "Dual",
	})
	if err != nil {
		t.Fatal(err)
	}
	if attr.Key != "BootModeSelect" {
		t.Fatalf("want BootModeSelect, got %s", attr.Key)
	}
}

func TestResolveBootModeAttrMissing(t *testing.T) {
	_, _, err := resolveBootModeAttr(schemas.SettingsAttributes{"QuietBoot": "Enabled"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLooksLikeLicenseError(t *testing.T) {
	if !looksLikeLicenseError(errors.New("BIOS config requires SFT-DCMS-SINGLE license")) {
		t.Fatal("expected license detect")
	}
	if looksLikeLicenseError(errors.New("connection refused")) {
		t.Fatal("false positive")
	}
}
