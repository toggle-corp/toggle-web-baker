package view

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// specWithTriggers builds a minimal FrontendApp carrying the trigger structs
// and an (otherwise empty) status, so we exercise the trigger-derived fields.
func specWithTriggers(scheduled, watch map[string]any) *unstructured.Unstructured {
	spec := map[string]any{"repo": "https://github.com/acme/site.git"}
	if scheduled != nil {
		spec["scheduledBuilds"] = scheduled
	}
	if watch != nil {
		spec["watchCommits"] = watch
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

// The next fire time is derived from spec.scheduledBuilds.schedule relative to
// Now, in UTC — only when the trigger is enabled with an explicit schedule.
func TestNextScheduled_FromEnabledSchedule(t *testing.T) {
	withNow(t, "2026-07-01T04:21:57Z")
	a := FromUnstructured(specWithTriggers(map[string]any{"enabled": true, "schedule": "0 */12 * * *"}, nil))
	if a.NextScheduledBuildTime != "2026-07-01T12:00:00Z" {
		t.Errorf("NextScheduledBuildTime = %q, want 2026-07-01T12:00:00Z", a.NextScheduledBuildTime)
	}
	if !a.ScheduledBuilds.Enabled || a.ScheduledBuilds.Schedule != "0 */12 * * *" {
		t.Errorf("ScheduledBuilds = %+v, want enabled with schedule", a.ScheduledBuilds)
	}
}

// A distinct expression proves we actually parse the cron, not hardcode it.
func TestNextScheduled_DistinctExpression(t *testing.T) {
	withNow(t, "2026-07-01T04:21:57Z")
	a := FromUnstructured(specWithTriggers(map[string]any{"enabled": true, "schedule": "0 9 * * *"}, nil))
	if a.NextScheduledBuildTime != "2026-07-01T09:00:00Z" {
		t.Errorf("daily schedule: got %q, want 2026-07-01T09:00:00Z", a.NextScheduledBuildTime)
	}
}

// Enabled with an EMPTY schedule means "operator default" — the console cannot
// know the operator's config value, so it must NOT compute a next time.
func TestNextScheduled_EnabledEmptyScheduleIsOperatorDefault(t *testing.T) {
	withNow(t, "2026-07-01T04:21:57Z")
	a := FromUnstructured(specWithTriggers(map[string]any{"enabled": true}, nil))
	if a.NextScheduledBuildTime != "" {
		t.Errorf("empty schedule: got %q, want empty (operator default is unknowable)", a.NextScheduledBuildTime)
	}
	if !a.ScheduledBuilds.Enabled || a.ScheduledBuilds.Schedule != "" {
		t.Errorf("ScheduledBuilds = %+v, want enabled with empty schedule", a.ScheduledBuilds)
	}
}

// Disabled or absent scheduledBuilds yields no next time and Enabled=false.
func TestNextScheduled_DisabledOrAbsent(t *testing.T) {
	withNow(t, "2026-07-01T04:21:57Z")
	for name, obj := range map[string]*unstructured.Unstructured{
		"absent":   specWithTriggers(nil, nil),
		"disabled": specWithTriggers(map[string]any{"enabled": false, "schedule": "0 9 * * *"}, nil),
	} {
		a := FromUnstructured(obj)
		if a.NextScheduledBuildTime != "" {
			t.Errorf("%s: NextScheduledBuildTime = %q, want empty", name, a.NextScheduledBuildTime)
		}
		if a.ScheduledBuilds.Enabled {
			t.Errorf("%s: ScheduledBuilds.Enabled = true, want false", name)
		}
	}
}

// An unparseable expression yields "" so the template renders an em-dash.
func TestNextScheduled_InvalidIsEmpty(t *testing.T) {
	withNow(t, "2026-07-01T04:21:57Z")
	a := FromUnstructured(specWithTriggers(map[string]any{"enabled": true, "schedule": "not a cron"}, nil))
	if a.NextScheduledBuildTime != "" {
		t.Errorf("invalid schedule: got %q, want empty", a.NextScheduledBuildTime)
	}
}

// watchCommits projects into the view model, including the last-seen SHA
// annotation and the repo for commit-link derivation.
func TestWatchCommits_Projection(t *testing.T) {
	obj := specWithTriggers(nil, map[string]any{"enabled": true, "interval": "5m"})
	obj.SetAnnotations(map[string]string{AnnotationWatchLastSeen: "cafebabe1234567890"})
	a := FromUnstructured(obj)
	if !a.WatchCommits.Enabled || a.WatchCommits.Interval != "5m" {
		t.Errorf("WatchCommits = %+v, want enabled with 5m interval", a.WatchCommits)
	}
	if a.WatchCommits.LastSeenSHA != "cafebabe1234567890" {
		t.Errorf("LastSeenSHA = %q, want cafebabe1234567890", a.WatchCommits.LastSeenSHA)
	}
	if a.Repo != "https://github.com/acme/site.git" {
		t.Errorf("Repo = %q", a.Repo)
	}
}

func TestWatchCommits_AbsentDisabled(t *testing.T) {
	a := FromUnstructured(specWithTriggers(nil, nil))
	if a.WatchCommits.Enabled || a.WatchCommits.Interval != "" || a.WatchCommits.LastSeenSHA != "" {
		t.Errorf("WatchCommits = %+v, want zero value", a.WatchCommits)
	}
}
