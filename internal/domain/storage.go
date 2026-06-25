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
	// Enforce strictly-increasing order across whichever thresholds are present
	// (0 = unset). Checking only adjacent pairs would lose transitivity when the
	// middle term (alert) is unset, so we compare each present threshold against
	// the previous present one.
	ordered := []struct {
		label string
		val   int64
	}{
		{"cleanupThreshold", v.CleanupBytes},
		{"alertThreshold", v.AlertBytes},
		{"releaseSizeCap", v.CapBytes},
	}
	prev := -1
	for i := range ordered {
		if ordered[i].val == 0 {
			continue
		}
		if prev >= 0 && ordered[prev].val >= ordered[i].val {
			return fmt.Errorf("storage.%s: %s (%d) must be < %s (%d)", name,
				ordered[prev].label, ordered[prev].val, ordered[i].label, ordered[i].val)
		}
		prev = i
	}
	return nil
}
