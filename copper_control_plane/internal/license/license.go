// Package license gates enterprise features (hardware RAID via storcli/ssacli)
// and collects the machine fingerprint used to bind signed certificates.
//
// OSS builds (!enterprise): Allows always false; Configure is a no-op.
// Enterprise builds: Configure verifies config.yaml license against the
// embedded public key + local fingerprint + valid_until.
package license

// Feature names used at gate points.
const (
	FeatureHWRAID  = "hwraid"
	FeatureStorcli = "storcli"
	FeatureSsacli  = "ssacli"
)
