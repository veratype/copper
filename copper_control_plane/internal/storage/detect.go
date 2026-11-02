package storage

import (
	"context"
	"fmt"
	"time"

	"copper_control_plane/internal/license"
)

// Detect SSHs into the live OS and figures out which RAID tool/family is present.
// Without a valid license or without the hwraid package linked, returns FamilyNone
// with no error noise (enterprise features stay invisible).
func Detect(ctx context.Context, h Host) DetectResult {
	now := time.Now().UTC()
	if !license.Allows(license.FeatureHWRAID) || hwRAID.Detect == nil {
		return DetectResult{
			OK:         true,
			Family:     FamilyNone,
			DataIP:     h.IP,
			DetectedAt: now,
		}
	}
	return hwRAID.Detect(ctx, h)
}

// ResolveDriver picks a driver from a prior DetectResult.
func ResolveDriver(d DetectResult) (Driver, error) {
	if !d.OK && d.Family == "" {
		return nil, fmt.Errorf("RAID not detected yet — run Detect first")
	}
	if d.Family == FamilyNone {
		return nil, fmt.Errorf("no hardware RAID controller")
	}
	if d.Error != "" && d.Family == FamilyUnknown {
		return nil, fmt.Errorf("RAID detect failed: %s", d.Error)
	}
	if !license.Allows(license.FeatureHWRAID) || hwRAID.ResolveDriver == nil {
		return nil, fmt.Errorf("no hardware RAID controller")
	}
	return hwRAID.ResolveDriver(d)
}

// FormatDetectError makes a short UI message.
func FormatDetectError(d DetectResult) string {
	if d.OK {
		return ""
	}
	if d.Error != "" {
		return d.Error
	}
	return fmt.Sprintf("detect incomplete (family=%s)", d.Family)
}
