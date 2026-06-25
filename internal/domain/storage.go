// Package domain holds the operator's pure decision logic, free of Kubernetes
// imports so it can be unit-tested in isolation. The reconciler translates
// FrontendApp CRD types into these calls.
package domain

import "fmt"

// VolumeThresholds are absolute byte thresholds for one storage volume.
// A zero value means the threshold is not set for that volume (local-path
// ignores requested capacity, so all thresholds are absolute bytes, never
// percent-of-capacity).
type VolumeThresholds struct {
	CleanupBytes int64 // over this, regenerable caches are auto-pruned before a build
	AlertBytes   int64 // over this, emit a storage alert
	CapBytes     int64 // hard cap: over this, the deploy is blocked (output only)
}

// StorageConfig mirrors spec.storage: per-volume thresholds.
type StorageConfig struct {
	Cache     VolumeThresholds
	DataCache VolumeThresholds
	Output    VolumeThresholds
}

// ValidateStorage enforces the ordering invariant cleanup < alert < cap for
// every volume, so a CR can never be configured to block before it warns.
func ValidateStorage(cfg StorageConfig) error {
	for _, vol := range []struct {
		name string
		v    VolumeThresholds
	}{
		{"cache", cfg.Cache},
		{"dataCache", cfg.DataCache},
		{"output", cfg.Output},
	} {
		if err := validateVolume(vol.name, vol.v); err != nil {
			return err
		}
	}
	return nil
}

func validateVolume(name string, v VolumeThresholds) error {
	if v.CleanupBytes != 0 && v.AlertBytes != 0 && v.CleanupBytes >= v.AlertBytes {
		return fmt.Errorf("storage.%s: cleanupThreshold (%d) must be < alertThreshold (%d)", name, v.CleanupBytes, v.AlertBytes)
	}
	if v.AlertBytes != 0 && v.CapBytes != 0 && v.AlertBytes >= v.CapBytes {
		return fmt.Errorf("storage.%s: alertThreshold (%d) must be < releaseSizeCap (%d)", name, v.AlertBytes, v.CapBytes)
	}
	return nil
}
