//go:build !enterprise

package license

// Configure is a no-op in OSS builds (no cert verification, no HW RAID).
func Configure(cert string) {}

// Allows is always false for enterprise features in OSS builds.
func Allows(feature string) bool {
	_ = feature
	return false
}
