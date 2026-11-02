package storage

import (
	"context"
	"fmt"
	"strings"

	"copper_control_plane/internal/license"
)

// Host is a reachable live OS (data IP + SSH creds).
type Host struct {
	IP    string
	Creds Creds
}

// Driver is one RAID-family backend behind the shared Controller JSON.
type Driver interface {
	Family() Family
	Tool() string
	// Config loads the controller layout for the console.
	Config(ctx context.Context, h Host) (Controller, error)
	// Allowed reports whether joined ssacli/storcli-style args may run.
	Allowed(args []string) bool
	// Exec runs an allowlisted tool command; returns combined output.
	Exec(ctx context.Context, h Host, args []string) (string, error)
}

// ForFamily returns the driver for a detected family (hardware RAID only when
// licensed and the hwraid package is linked).
func ForFamily(f Family) (Driver, error) {
	switch f {
	case FamilySmartArray, FamilyMegaRAID:
		if !license.Allows(license.FeatureHWRAID) || hwRAID.ForFamily == nil {
			return nil, fmt.Errorf("no hardware RAID controller on this host")
		}
		return hwRAID.ForFamily(f)
	case FamilyNone:
		return nil, fmt.Errorf("no hardware RAID controller on this host")
	default:
		return nil, fmt.Errorf("unsupported RAID family %q", f)
	}
}

func joinArgs(args []string) string {
	return strings.Join(args, " ")
}

// JoinArgs joins CLI args with spaces (shared with hwraid via thin wrappers).
func JoinArgs(args []string) string {
	return joinArgs(args)
}
