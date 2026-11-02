package provision

import (
	"strings"
	"testing"
)

func TestBuildReidentifyScript_SafetyNets(t *testing.T) {
	script := buildReidentifyScript("/dev/sdg")
	needles := []string{
		"REIDENTIFY_BEGIN",
		"sgdisk",
		"-G",
		"c12a7328-f81f-11d2-ba4b-00a0c93ec93b",
		"bc13c2ff-59e6-4262-a352-b275fd6f7172",
		"cloudimg-rootfs",
		"/etc/fstab",
		"40-force-partuuid.cfg",
		"EFI/ubuntu/grub.cfg",
		"REIDENTIFY_OK",
		"/dev/sdg",
	}
	for _, n := range needles {
		if !strings.Contains(script, n) {
			t.Fatalf("script missing %q", n)
		}
	}
	for _, bad := range []string{
		"tune2fs", "e2fsck", "-U random",
		"${DEV}1", "${DEV}15", "${DEV}16",
		"/dev/sdg1", "/dev/sdg15", "/dev/sdg16",
	} {
		if strings.Contains(script, bad) {
			t.Fatalf("script must not contain %q", bad)
		}
	}
}

func TestProvisionPipelineOrder_ReidentifyBeforeCidata(t *testing.T) {
	if !strings.Contains(buildGPTFixScript("/dev/sda"), "GPT_OK") {
		t.Fatal("gpt script")
	}
	if !strings.Contains(buildReidentifyScript("/dev/sda"), "REIDENTIFY_OK") {
		t.Fatal("reidentify script")
	}
	if !strings.Contains(buildEFIBootProbeScript("/dev/sda"), "EFIBOOT_PROBE_OK") {
		t.Fatal("efiboot probe script")
	}
}
