package storage

import "context"

// HardwareRAIDHooks is registered by internal/hwraid via init so storage can
// call detect/resolve without importing hwraid (avoids an import cycle).
type HardwareRAIDHooks struct {
	Detect        func(ctx context.Context, h Host) DetectResult
	ResolveDriver func(d DetectResult) (Driver, error)
	ForFamily     func(f Family) (Driver, error)
}

var hwRAID HardwareRAIDHooks

// RegisterHardwareRAID wires the proprietary HW RAID backend. Called from hwraid.init.
func RegisterHardwareRAID(h HardwareRAIDHooks) {
	hwRAID = h
}

// HardwareRAIDRegistered reports whether the hwraid package was linked in.
func HardwareRAIDRegistered() bool {
	return hwRAID.Detect != nil
}
