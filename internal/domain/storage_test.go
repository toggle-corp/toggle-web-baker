package domain

import "testing"

// The storage invariant resolved in design: for each volume, any thresholds
// present must satisfy cleanup < alert < cap. The operator rejects a CR that
// violates this (so you can never configure "block before warn").

func TestValidateStorage_RejectsCacheCleanupNotBelowAlert(t *testing.T) {
	cfg := StorageConfig{
		Cache: VolumeThresholds{CleanupBytes: 5 << 30, AlertBytes: 4 << 30},
	}
	err := ValidateStorage(cfg)
	if err == nil {
		t.Fatalf("expected error: cache cleanup (5Gi) >= alert (4Gi) must be rejected")
	}
	if !contains(err.Error(), "cache") {
		t.Fatalf("error should name the offending volume %q, got: %v", "cache", err)
	}
}

func TestValidateStorage_RejectsOutputAlertNotBelowCap(t *testing.T) {
	cfg := StorageConfig{
		Output: VolumeThresholds{AlertBytes: 10 << 30, CapBytes: 8 << 30},
	}
	err := ValidateStorage(cfg)
	if err == nil {
		t.Fatalf("expected error: output alert (10Gi) >= cap (8Gi) must be rejected")
	}
	if !contains(err.Error(), "output") {
		t.Fatalf("error should name the offending volume %q, got: %v", "output", err)
	}
}

func TestValidateStorage_RejectsCleanupAboveCapWhenAlertUnset(t *testing.T) {
	// With alert unset (0), the cleanup<alert and alert<cap checks both skip;
	// the cleanup<cap invariant must still be enforced transitively.
	cfg := StorageConfig{
		Output: VolumeThresholds{CleanupBytes: 10 << 30, CapBytes: 5 << 30},
	}
	if err := ValidateStorage(cfg); err == nil {
		t.Fatalf("cleanup (10Gi) >= cap (5Gi) with alert unset must be rejected")
	}
}

func TestValidateStorage_AcceptsValidOrdering(t *testing.T) {
	cfg := StorageConfig{
		Cache:     VolumeThresholds{CleanupBytes: 2 << 30, AlertBytes: 4 << 30},
		DataCache: VolumeThresholds{CleanupBytes: 1 << 30, AlertBytes: 3 << 30},
		Output:    VolumeThresholds{AlertBytes: 8 << 30, CapBytes: 10 << 30},
	}
	if err := ValidateStorage(cfg); err != nil {
		t.Fatalf("valid ordering should pass, got: %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
