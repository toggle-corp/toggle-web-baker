package view

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// specWithSchedule builds a minimal FrontendApp carrying spec.schedule and an
// (otherwise empty) status, so we exercise the next-scheduled derivation.
func specWithSchedule(schedule string) *unstructured.Unstructured {
	spec := map[string]any{}
	if schedule != "" {
		spec["schedule"] = schedule
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "baker.toggle-corp.com/v1alpha1",
		"kind":       "FrontendApp",
		"metadata":   map[string]any{"namespace": "mapswipe", "name": "mapswipe-uat"},
		"spec":       spec,
		"status":     map[string]any{"phase": "Ready"},
	}}
}

func withNow(t *testing.T, at string) {
	t.Helper()
	fixed, err := time.Parse(time.RFC3339, at)
	if err != nil {
		t.Fatalf("bad fixed time %q: %v", at, err)
	}
	old := Now
	Now = func() time.Time { return fixed }
	t.Cleanup(func() { Now = old })
}

// The next fire time is derived from spec.schedule relative to Now, in UTC.
func TestNextScheduled_FromSpecSchedule(t *testing.T) {
	withNow(t, "2026-07-01T04:21:57Z")
	a := FromUnstructured(specWithSchedule("0 */12 * * *"))
	if a.NextScheduledBuildTime != "2026-07-01T12:00:00Z" {
		t.Errorf("NextScheduledBuildTime = %q, want 2026-07-01T12:00:00Z", a.NextScheduledBuildTime)
	}
}

// An absent schedule falls back to the operator's default (every 12h).
func TestNextScheduled_EmptyUsesDefault(t *testing.T) {
	withNow(t, "2026-07-01T04:21:57Z")
	a := FromUnstructured(specWithSchedule(""))
	if a.NextScheduledBuildTime != "2026-07-01T12:00:00Z" {
		t.Errorf("empty schedule: got %q, want default-derived 2026-07-01T12:00:00Z", a.NextScheduledBuildTime)
	}
}

// A distinct expression proves we actually parse the cron, not hardcode it.
func TestNextScheduled_DistinctExpression(t *testing.T) {
	withNow(t, "2026-07-01T04:21:57Z")
	a := FromUnstructured(specWithSchedule("0 9 * * *")) // daily 09:00 UTC
	if a.NextScheduledBuildTime != "2026-07-01T09:00:00Z" {
		t.Errorf("daily schedule: got %q, want 2026-07-01T09:00:00Z", a.NextScheduledBuildTime)
	}
}

// An unparseable expression yields "" so the template renders an em-dash.
func TestNextScheduled_InvalidIsEmpty(t *testing.T) {
	withNow(t, "2026-07-01T04:21:57Z")
	a := FromUnstructured(specWithSchedule("not a cron"))
	if a.NextScheduledBuildTime != "" {
		t.Errorf("invalid schedule: got %q, want empty", a.NextScheduledBuildTime)
	}
}
