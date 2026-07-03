// Package domain holds the operator's pure decision logic, free of Kubernetes
// imports so it can be unit-tested in isolation. The reconciler translates
// FrontendApp CRD types into these calls.
package domain

import (
	"fmt"
	"strings"
)

// Threshold states reported in status.storage.thresholdState. The console
// renders "OK" as a healthy badge and any other non-empty value as a warning
// badge; "" means nothing has been measured yet.
const (
	ThresholdStateOK       = "OK"
	ThresholdStateAlert    = "Alert"
	ThresholdStateCritical = "Critical"
)

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

// NamedVolume pairs a volume's status.storage.sizes key with its thresholds.
type NamedVolume struct {
	Name string
	VolumeThresholds
}

// Volumes enumerates the per-volume thresholds in pipeline order. It is THE
// name→thresholds mapping: validation and the metrics exporter both iterate it,
// so adding a volume here is the single change that keeps them in lockstep.
func (cfg StorageConfig) Volumes() []NamedVolume {
	return []NamedVolume{
		{"cache", cfg.Cache},
		{"dataCache", cfg.DataCache},
		{"output", cfg.Output},
	}
}

// ValidateStorage enforces the ordering invariant cleanup < alert < cap for
// every volume, so a CR can never be configured to block before it warns.
func ValidateStorage(cfg StorageConfig) error {
	for _, vol := range cfg.Volumes() {
		if err := validateVolume(vol.Name, vol.VolumeThresholds); err != nil {
			return err
		}
	}
	return nil
}

// EvaluateThresholdState classifies the worst per-volume storage state from the
// measured sizes against the configured thresholds. It is the pure source of
// status.storage.thresholdState. Returns "" when nothing has been measured.
//
//   - Critical: a volume is at/over its hard cap (capBytes) — output only — so
//     a deploy would be blocked.
//   - Alert:    any volume is at/over its alertBytes.
//   - OK:       sizes are known and nothing crosses an alert/cap threshold.
//
// Critical outranks Alert. Keys with no configured thresholds (e.g. the
// copier's "source" checkout) are ignored.
func EvaluateThresholdState(sizes map[string]int64, cfg StorageConfig) string {
	if len(sizes) == 0 {
		return ""
	}
	critical, alert := false, false
	for key, size := range sizes {
		v, ok := thresholdsForKey(key, cfg)
		if !ok {
			continue
		}
		if v.CapBytes > 0 && size >= v.CapBytes {
			critical = true
		}
		if v.AlertBytes > 0 && size >= v.AlertBytes {
			alert = true
		}
	}
	switch {
	case critical:
		return ThresholdStateCritical
	case alert:
		return ThresholdStateAlert
	default:
		return ThresholdStateOK
	}
}

// thresholdsForKey maps a status.storage.sizes key to its configured thresholds
// by substring, mirroring the console's capForKey resolution: "total" → none
// (checked before "output"), "output" → Output, "data" → DataCache (checked
// before "cache" so "dataCache" resolves correctly), else "cache" → Cache.
func thresholdsForKey(key string, cfg StorageConfig) (VolumeThresholds, bool) {
	k := strings.ToLower(key)
	switch {
	case strings.Contains(k, "total"):
		// "outputTotal" is the whole output PVC across all retained releases; the
		// per-release output thresholds don't bound it and there is no total
		// threshold, so it has no threshold state. Match "total" BEFORE "output"
		// so it isn't evaluated against output.alertBytes/capBytes.
		return VolumeThresholds{}, false
	case strings.Contains(k, "output"):
		return cfg.Output, true
	case strings.Contains(k, "data"):
		return cfg.DataCache, true
	case strings.Contains(k, "cache"):
		return cfg.Cache, true
	default:
		return VolumeThresholds{}, false
	}
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
