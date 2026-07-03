package view

import (
	"reflect"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// fullStatusObj is a FrontendApp whose .status exercises every documented
// field, including the "serving last-good while latest build failed" combo
// (Ready=True + Degraded=True) and string/number coercion in the maps.
func fullStatusObj() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "baker.toggle-corp.com/v1alpha1",
		"kind":       "FrontendApp",
		"metadata": map[string]any{
			"namespace": "mapswipe",
			"name":      "mapswipe-uat",
		},
		"status": map[string]any{
			"observedGeneration": int64(7),
			"phase":              "Degraded",
			"nodeName":           "node-3",
			"url":                "https://mapswipe-uat.example.org",
			"specStale":          true,
			"conditions": []any{
				map[string]any{"type": "Ready", "status": "True", "reason": "ServingLastGood", "message": "previous release", "lastTransitionTime": "2026-06-25T10:00:00Z"},
				map[string]any{"type": "Degraded", "status": "True", "reason": "BuildFailed", "message": "yarn build exited 1"},
				map[string]any{"type": "BuildSucceeded", "status": "False"},
				map[string]any{"type": "IngressReady", "status": "True"},
			},
			"build": map[string]any{
				"phase":          "Failed",
				"result":         "Error",
				"jobName":        "mapswipe-uat-build-7",
				"podName":        "mapswipe-uat-build-7-abcde",
				"trigger":        "Manual",
				"triggeredBy":    "octocat",
				"failedStep":     "build",
				"startTime":      "2026-06-25T09:50:00Z",
				"completionTime": "2026-06-25T09:55:00Z",
				"attempts":       int64(3),
				"message":        "build failed",
				"logsRef":        "mapswipe/pod/mapswipe-uat-build-7",
				"steps": []any{
					map[string]any{"name": "clone", "status": "Succeeded"},
					map[string]any{"name": "build", "status": "Failed", "message": "yarn build exited 1", "peakMemoryBytes": int64(3555555555)},
				},
			},
			"buildHistory": []any{
				map[string]any{"jobName": "mapswipe-uat-build-7", "result": "Failed", "trigger": "Manual"},
				map[string]any{"jobName": "mapswipe-uat-build-6", "result": "Succeeded", "trigger": "Scheduled"},
			},
			"lastProcessedRebuild":    "2026-06-24T08:00:00Z",
			"lastBuiltSpecHash":       "abc123",
			"lastBuildTime":           "2026-06-25T09:55:00Z",
			"lastSuccessfulBuildTime": "2026-06-24T09:55:00Z",
			"release": map[string]any{
				"current":      "rel-2026-06-24",
				"previous":     "rel-2026-06-23",
				"servingSince": "2026-06-24T10:00:00Z",
			},
			"storage": map[string]any{
				"measuredAt":     "2026-06-25T09:00:00Z",
				"thresholdState": "OK",
				// mixed string + numeric values must both render
				"sizes":         map[string]any{"data": int64(1048576), "cache": "2Mi"},
				"lastRunDeltas": map[string]any{"data": float64(512)},
			},
			"lastManualTrigger": map[string]any{
				"triggeredBy": "octocat",
				"time":        "2026-06-23T12:00:00Z",
			},
		},
	}}
}

func TestFromUnstructured_FullStatus(t *testing.T) {
	a := FromUnstructured(fullStatusObj())

	if a.Namespace != "mapswipe" || a.Name != "mapswipe-uat" {
		t.Fatalf("identity wrong: %s/%s", a.Namespace, a.Name)
	}
	if !a.HasStatus {
		t.Fatal("HasStatus should be true")
	}
	if a.ObservedGeneration != 7 {
		t.Errorf("observedGeneration = %d, want 7", a.ObservedGeneration)
	}
	if a.Phase != "Degraded" || a.NodeName != "node-3" {
		t.Errorf("phase/node wrong: %q %q", a.Phase, a.NodeName)
	}
	if !a.SpecStale {
		t.Error("specStale should be true")
	}
	if a.Build.Attempts != 3 || a.Build.LogsRef == "" {
		t.Errorf("build mapping wrong: %+v", a.Build)
	}
	if a.Build.PodName != "mapswipe-uat-build-7-abcde" || a.Build.Trigger != "Manual" || a.Build.FailedStep != "build" {
		t.Errorf("build new fields wrong: %+v", a.Build)
	}
	if a.Build.TriggeredBy != "octocat" {
		t.Errorf("build triggeredBy wrong: %q", a.Build.TriggeredBy)
	}
	if len(a.Build.Steps) != 2 {
		t.Fatalf("want 2 steps, got %d", len(a.Build.Steps))
	}
	if a.Build.Steps[0].Name != "clone" || a.Build.Steps[0].Status != "Succeeded" {
		t.Errorf("step[0] wrong: %+v", a.Build.Steps[0])
	}
	if a.Build.Steps[1].Status != "Failed" || a.Build.Steps[1].Message != "yarn build exited 1" {
		t.Errorf("step[1] wrong: %+v", a.Build.Steps[1])
	}
	// peakMemoryBytes humanizes into Step.PeakMemory; unmeasured steps stay empty.
	if a.Build.Steps[1].PeakMemory != "3.3 GiB" {
		t.Errorf("step[1] peak wrong: %q, want 3.3 GiB", a.Build.Steps[1].PeakMemory)
	}
	if a.Build.Steps[0].PeakMemory != "" {
		t.Errorf("unmeasured step must have empty PeakMemory, got %q", a.Build.Steps[0].PeakMemory)
	}
	if len(a.BuildHistory) != 2 {
		t.Fatalf("want 2 history entries, got %d", len(a.BuildHistory))
	}
	if a.BuildHistory[0].JobName != "mapswipe-uat-build-7" || a.BuildHistory[0].Result != "Failed" {
		t.Errorf("history[0] wrong: %+v", a.BuildHistory[0])
	}
	if a.BuildHistory[1].Trigger != "Scheduled" {
		t.Errorf("history[1] trigger wrong: %+v", a.BuildHistory[1])
	}
	if a.Release.Current != "rel-2026-06-24" {
		t.Errorf("release.current wrong: %q", a.Release.Current)
	}
	if a.ManualTrigger.TriggeredBy != "octocat" {
		t.Errorf("manual trigger user wrong: %q", a.ManualTrigger.TriggeredBy)
	}
	if a.LastSuccessfulBuild != "2026-06-24T09:55:00Z" {
		t.Errorf("lastSuccessfulBuildTime wrong: %q", a.LastSuccessfulBuild)
	}
}

func TestBuildFrom_Termination(t *testing.T) {
	// Present: the termination map maps into the view-local Termination struct,
	// coercing exitCode (a number) to int64.
	b := buildFrom(map[string]any{
		"termination": map[string]any{
			"reason":      "OOMKilled",
			"container":   "build",
			"exitCode":    int64(137),
			"memoryLimit": "256Mi",
			"finishedAt":  "2026-06-25T09:55:00Z",
		},
	})
	if b.Termination == nil {
		t.Fatal("Termination should be populated when present")
	}
	if b.Termination.Reason != "OOMKilled" || b.Termination.Container != "build" {
		t.Errorf("termination reason/container wrong: %+v", b.Termination)
	}
	if b.Termination.ExitCode != 137 || b.Termination.MemoryLimit != "256Mi" {
		t.Errorf("termination exitCode/memoryLimit wrong: %+v", b.Termination)
	}
	if b.Termination.FinishedAt != "2026-06-25T09:55:00Z" {
		t.Errorf("termination finishedAt wrong: %+v", b.Termination)
	}

	// Absent: no termination key → nil.
	if got := buildFrom(map[string]any{}).Termination; got != nil {
		t.Errorf("absent termination should be nil, got %+v", got)
	}
	// Mistyped: a non-map termination → nil.
	if got := buildFrom(map[string]any{"termination": "boom"}).Termination; got != nil {
		t.Errorf("mistyped termination should be nil, got %+v", got)
	}
}

func TestTerminationFrom(t *testing.T) {
	if got := terminationFrom(nil); got != nil {
		t.Errorf("nil should map to nil, got %+v", got)
	}
	if got := terminationFrom("not-a-map"); got != nil {
		t.Errorf("non-map should map to nil, got %+v", got)
	}
	// exitCode carried as a string still coerces (defensive).
	got := terminationFrom(map[string]any{"reason": "OOMKilled", "exitCode": "137"})
	if got == nil || got.Reason != "OOMKilled" || got.ExitCode != 137 {
		t.Errorf("string exitCode should coerce; got %+v", got)
	}
}

func TestBuild_IsOOM(t *testing.T) {
	cases := []struct {
		name  string
		build Build
		want  bool
	}{
		{"oom", Build{Termination: &Termination{Reason: "OOMKilled"}}, true},
		{"non-oom termination", Build{Termination: &Termination{Reason: "Error"}}, false},
		{"nil termination", Build{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.build.IsOOM(); got != tc.want {
				t.Errorf("IsOOM() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBuild_TerminationSummary(t *testing.T) {
	cases := []struct {
		name  string
		build Build
		want  string
	}{
		{
			"oom with limit",
			Build{Termination: &Termination{Reason: "OOMKilled", Container: "build", MemoryLimit: "256Mi"}},
			"OOM Killed — the build step exceeded its 256Mi memory limit.",
		},
		{
			"oom without limit",
			Build{Termination: &Termination{Reason: "OOMKilled", Container: "build"}},
			"OOM Killed — the build step exceeded its memory limit.",
		},
		{
			"non-oom termination",
			Build{Termination: &Termination{Reason: "Error", ExitCode: 1}},
			"Terminated: Error (exit 1)",
		},
		{"nil termination", Build{}, ""},
		{"empty reason", Build{Termination: &Termination{}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.build.TerminationSummary(); got != tc.want {
				t.Errorf("TerminationSummary() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuild_TriggerLabel(t *testing.T) {
	cases := []struct {
		name  string
		build Build
		want  string
	}{
		{"manual attributed", Build{Trigger: "Manual", TriggeredBy: "octocat"}, "Manual · octocat"},
		{"scheduled unattributed", Build{Trigger: "Scheduled"}, "Scheduled"},
		{"manual without user", Build{Trigger: "Manual"}, "Manual"},
		{"empty trigger", Build{}, "—"},
		{"empty trigger with user", Build{TriggeredBy: "octocat"}, "—"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.build.TriggerLabel(); got != tc.want {
				t.Errorf("TriggerLabel() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFromUnstructured_ConditionsSortedAndAccessible(t *testing.T) {
	a := FromUnstructured(fullStatusObj())
	// Conditions are sorted by type.
	if a.Conditions[0].Type != "BuildSucceeded" {
		t.Errorf("expected sorted conditions, got first = %q", a.Conditions[0].Type)
	}
	if c, ok := a.Condition("Ready"); !ok || !c.IsTrue() {
		t.Error("Ready condition should be present and True")
	}
}

func TestServingLastGood_HealthClassAndLabel(t *testing.T) {
	a := FromUnstructured(fullStatusObj())
	if !a.Ready() || !a.Degraded() {
		t.Fatalf("fixture should be Ready=True and Degraded=True")
	}
	if got := a.HealthClass(); got != "degraded-serving" {
		t.Errorf("HealthClass = %q, want degraded-serving", got)
	}
	if got := a.HealthLabel(); got != "Serving last-good (latest build failed)" {
		t.Errorf("HealthLabel = %q", got)
	}
	if !a.BuildFailed() {
		t.Error("BuildSucceeded=False should report BuildFailed()")
	}
}

func TestStorageVolumes_SortedAndHumanized(t *testing.T) {
	a := FromUnstructured(fullStatusObj())
	if len(a.Storage.Volumes) != 2 {
		t.Fatalf("want 2 volumes, got %d", len(a.Storage.Volumes))
	}
	// key-sorted: cache before data
	if a.Storage.Volumes[0].Name != "cache" || a.Storage.Volumes[1].Name != "data" {
		t.Errorf("volumes not key-sorted: %+v", a.Storage.Volumes)
	}
	// numeric byte count humanized (1048576 == 1 MiB)
	if got := a.Storage.Volumes[1].Human; got != HumanizeBytes(1048576) {
		t.Errorf("data size = %q, want %q", got, HumanizeBytes(1048576))
	}
}

func TestStorageVolumes_CapMappingAndBars(t *testing.T) {
	// status.storage.sizes carries an "output" key (mapped to a cap → has a bar)
	// and a "cache" key (no cap → size only). spec.storage supplies the caps.
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": "ns", "name": "vols"},
		"spec": map[string]any{
			"storage": map[string]any{
				"output": map[string]any{"capBytes": int64(1000)},
				// cache has no cleanup/alert → no cap → no bar
				"cache": map[string]any{},
				// dataCache: cleanupBytes wins when alertBytes is zero/absent
				"dataCache": map[string]any{"cleanupBytes": int64(2000)},
			},
		},
		"status": map[string]any{
			"storage": map[string]any{
				"sizes":         map[string]any{"output": int64(500), "cache": int64(123), "data-cache": int64(2500)},
				"lastRunDeltas": map[string]any{"output": int64(100)},
			},
		},
	}}
	a := FromUnstructured(obj)
	vols := a.Storage.Volumes
	if len(vols) != 3 {
		t.Fatalf("want 3 volumes, got %d: %+v", len(vols), vols)
	}
	byName := map[string]StorageVolume{}
	for _, v := range vols {
		byName[v.Name] = v
	}

	out := byName["output"]
	if !out.HasBar || out.Cap != 1000 || out.BarPct != 50 || out.Over {
		t.Errorf("output volume wrong: %+v", out)
	}
	if out.Human != "500 B" {
		t.Errorf("output Human = %q", out.Human)
	}
	if out.Delta != HumanizeDelta(100) {
		t.Errorf("output Delta = %q", out.Delta)
	}

	cache := byName["cache"]
	if cache.HasBar || cache.Cap != 0 {
		t.Errorf("cache should have no bar: %+v", cache)
	}
	if cache.Human != "123 B" {
		t.Errorf("cache Human = %q", cache.Human)
	}

	// "data-cache" key contains "data" → maps to dataCache.cleanupBytes (2000).
	// used 2500 > cap 2000 → over.
	dc := byName["data-cache"]
	if !dc.HasBar || dc.Cap != 2000 || !dc.Over {
		t.Errorf("data-cache volume wrong: %+v", dc)
	}
}

func TestStorageVolumes_OutputTotalHasNoBar(t *testing.T) {
	// "output" is the current release (bounded by output.capBytes → bar).
	// "outputTotal" is the whole output PVC across all retained releases; the
	// per-release cap does NOT bound it, so it must render as a number with no bar.
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": "ns", "name": "vols"},
		"spec": map[string]any{
			"storage": map[string]any{
				"output": map[string]any{"capBytes": int64(1000)},
			},
		},
		"status": map[string]any{
			"storage": map[string]any{
				"sizes": map[string]any{"output": int64(500), "outputTotal": int64(5000)},
			},
		},
	}}
	a := FromUnstructured(obj)
	byName := map[string]StorageVolume{}
	for _, v := range a.Storage.Volumes {
		byName[v.Name] = v
	}

	out := byName["output"]
	if !out.HasBar || out.Cap != 1000 {
		t.Errorf("output (current release) should keep its per-release bar: %+v", out)
	}

	total := byName["outputTotal"]
	if total.HasBar || total.Cap != 0 {
		t.Errorf("outputTotal must NOT reuse the per-release cap / bar: %+v", total)
	}
}

func TestCapForKey_TotalIgnoresPerReleaseCap(t *testing.T) {
	spec := map[string]any{"output": map[string]any{"capBytes": int64(1000)}}
	if got := capForKey("output", spec); got != 1000 {
		t.Errorf("capForKey(output) = %d, want 1000", got)
	}
	if got := capForKey("outputTotal", spec); got != 0 {
		t.Errorf("capForKey(outputTotal) = %d, want 0 (total is not bounded by the per-release cap)", got)
	}
}

func TestVolumeLabel(t *testing.T) {
	cases := map[string]string{
		"output":      "Output (current release)",
		"outputTotal": "Output (all releases)",
		"cache":       "Cache",
		"dataCache":   "Data cache",
		"unknownKey":  "unknownKey", // unmapped keys fall through to the raw name
	}
	for key, want := range cases {
		if got := volumeLabel(key); got != want {
			t.Errorf("volumeLabel(%q) = %q, want %q", key, got, want)
		}
		if got := (StorageVolume{Name: key}).DisplayName(); got != want {
			t.Errorf("StorageVolume{%q}.DisplayName() = %q, want %q", key, got, want)
		}
	}
}

func TestFromUnstructured_NoStatus(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": "ns", "name": "fresh"},
	}}
	a := FromUnstructured(obj)
	if a.HasStatus {
		t.Error("HasStatus should be false when .status is absent")
	}
	if a.HealthLabel() != "Unknown" {
		t.Errorf("HealthLabel for no-status = %q, want Unknown", a.HealthLabel())
	}
}

func TestFromUnstructured_DefensiveOnWrongTypes(t *testing.T) {
	// status present but fields are the wrong type / partial; must not panic
	// and must leave zero values rather than crashing the renderer.
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": "ns", "name": "weird"},
		"status": map[string]any{
			"phase":      map[string]any{}, // not a scalar -> coerces to empty
			"specStale":  "true",           // string instead of bool -> coerced true
			"conditions": "not-a-list",     // wrong type -> nil
			"build":      "nope",           // wrong type -> empty Build
		},
	}}
	a := FromUnstructured(obj)
	if !a.HasStatus {
		t.Fatal("HasStatus should be true")
	}
	if a.Phase != "" {
		t.Errorf("non-scalar phase should coerce to empty, got %q", a.Phase)
	}
	if !a.SpecStale {
		t.Error("string \"true\" specStale should coerce to true")
	}
	if a.Conditions != nil {
		t.Error("non-list conditions should be nil")
	}
	if !reflect.DeepEqual(a.Build, Build{}) {
		t.Error("non-map build should be zero Build")
	}
}

func TestFromUnstructured_Cleanup(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": "mapswipe", "name": "mapswipe-uat"},
		"status": map[string]any{
			"cleanup": map[string]any{
				"cache": map[string]any{
					"requestedAt":    "2026-06-25T09:00:00Z",
					"requestedBy":    "octocat",
					"phase":          "Succeeded",
					"completedAt":    "2026-06-25T09:05:00Z",
					"reclaimedBytes": int64(1048576),
					"message":        "pruned 3 layers",
				},
				"releases": map[string]any{
					"phase":          "Running",
					"requestedBy":    "hubber",
					"reclaimedBytes": float64(512),
				},
			},
		},
	}}
	a := FromUnstructured(obj)
	if a.Cleanup.Cache.Phase != "Succeeded" || a.Cleanup.Cache.RequestedBy != "octocat" {
		t.Errorf("cache cleanup wrong: %+v", a.Cleanup.Cache)
	}
	if a.Cleanup.Cache.ReclaimedBytes != 1048576 {
		t.Errorf("cache reclaimedBytes = %d, want 1048576", a.Cleanup.Cache.ReclaimedBytes)
	}
	if a.Cleanup.Cache.Message != "pruned 3 layers" || a.Cleanup.Cache.LastCompleted != "2026-06-25T09:05:00Z" {
		t.Errorf("cache message/completedAt wrong: %+v", a.Cleanup.Cache)
	}
	if a.Cleanup.Releases.Phase != "Running" || a.Cleanup.Releases.ReclaimedBytes != 512 {
		t.Errorf("releases cleanup wrong: %+v", a.Cleanup.Releases)
	}
}

// A cluster still running an older operator writes the legacy lastCompleted
// string; the view keeps rendering it until the operator is upgraded.
func TestFromUnstructured_CleanupLegacyLastCompleted(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": "mapswipe", "name": "mapswipe-uat"},
		"status": map[string]any{
			"cleanup": map[string]any{
				"cache": map[string]any{
					"phase":         "Succeeded",
					"lastCompleted": "2026-06-25T09:05:00Z",
				},
			},
		},
	}}
	a := FromUnstructured(obj)
	if a.Cleanup.Cache.LastCompleted != "2026-06-25T09:05:00Z" {
		t.Errorf("legacy lastCompleted must still populate, got %+v", a.Cleanup.Cache)
	}
}

func TestFromUnstructured_CleanupAbsentIsZero(t *testing.T) {
	a := FromUnstructured(fullStatusObj()) // no status.cleanup
	if (a.Cleanup != Cleanup{}) {
		t.Errorf("absent cleanup should be zero value, got %+v", a.Cleanup)
	}
	// Wrong type must not panic and stays zero.
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": "ns", "name": "x"},
		"status":   map[string]any{"cleanup": "not-a-map"},
	}}
	if (FromUnstructured(obj).Cleanup != Cleanup{}) {
		t.Error("mistyped cleanup should be zero value")
	}
}

func TestApp_CleanupBusy(t *testing.T) {
	cases := []struct {
		name     string
		appPhase string
		bldPhase string
		cachePh  string
		relPh    string
		wantBusy bool
	}{
		{"all idle", "Ready", "Complete", "Succeeded", "Succeeded", false},
		{"empty", "", "", "", "", false},
		{"build active", "Building", "", "", "", true},
		{"cache pending", "Ready", "Complete", "Pending", "", true},
		{"cache running", "Ready", "Complete", "Running", "", true},
		{"releases pending", "Ready", "Complete", "", "Pending", true},
		{"releases running", "Ready", "Complete", "Succeeded", "Running", true},
		{"both failed -> idle", "Ready", "Complete", "Failed", "Failed", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := App{
				Phase: c.appPhase,
				Build: Build{Phase: c.bldPhase},
				Cleanup: Cleanup{
					Cache:    CleanupAction{Phase: c.cachePh},
					Releases: CleanupAction{Phase: c.relPh},
				},
			}
			if got := a.CleanupBusy(); got != c.wantBusy {
				t.Errorf("CleanupBusy()=%v, want %v", got, c.wantBusy)
			}
		})
	}
}

func TestFromUnstructured_Group(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": "ns", "name": "grouped"},
		"spec":     map[string]any{"group": "ifrc-go"},
	}}
	if got := FromUnstructured(obj).Group; got != "ifrc-go" {
		t.Errorf("Group = %q, want ifrc-go", got)
	}

	// Absent spec.group (older resources) → "".
	if got := FromUnstructured(fullStatusObj()).Group; got != "" {
		t.Errorf("absent spec.group should map to empty, got %q", got)
	}
	// Mistyped spec.group must not panic and coerces to "".
	weird := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": "ns", "name": "weird"},
		"spec":     map[string]any{"group": map[string]any{}},
	}}
	if got := FromUnstructured(weird).Group; got != "" {
		t.Errorf("mistyped spec.group should coerce to empty, got %q", got)
	}
}

func TestApp_URLHost(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{"https URL", "https://mapswipe.org", "mapswipe.org"},
		{"URL with path", "https://go.ifrc.org/emergencies", "go.ifrc.org"},
		{"empty", "", ""},
		{"schemeless falls back to raw", "mapswipe.org", "mapswipe.org"},
		{"unparseable falls back to raw", "http://[::1:bad", "http://[::1:bad"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := (App{URL: c.url}).URLHost(); got != c.want {
				t.Errorf("URLHost(%q) = %q, want %q", c.url, got, c.want)
			}
		})
	}
}

// appWithHealth builds a minimal App carrying the conditions/phase that map to
// the given health class (see HealthClass).
func appWithHealth(class string) App {
	switch class {
	case "degraded":
		return App{Conditions: []Condition{{Type: "Degraded", Status: "True"}}}
	case "degraded-serving":
		return App{Conditions: []Condition{
			{Type: "Degraded", Status: "True"}, {Type: "Ready", Status: "True"},
		}}
	case "ready":
		return App{Conditions: []Condition{{Type: "Ready", Status: "True"}}}
	case "building":
		return App{Phase: "Building"}
	default:
		return App{}
	}
}

func TestApp_HealthRank_OrdersMostBrokenFirst(t *testing.T) {
	order := []string{"degraded", "degraded-serving", "building", "pending", "ready"}
	for i := 1; i < len(order); i++ {
		prev := appWithHealth(order[i-1])
		cur := appWithHealth(order[i])
		if prev.HealthRank() >= cur.HealthRank() {
			t.Errorf("HealthRank(%s)=%d should sort before HealthRank(%s)=%d",
				order[i-1], prev.HealthRank(), order[i], cur.HealthRank())
		}
	}
}

func TestApp_HealthShortLabel(t *testing.T) {
	if got := appWithHealth("degraded-serving").HealthShortLabel(); got != "Serving last-good" {
		t.Errorf("degraded-serving short label = %q", got)
	}
	if got := appWithHealth("ready").HealthShortLabel(); got != "Ready" {
		t.Errorf("ready short label = %q", got)
	}
	if got := appWithHealth("pending").HealthShortLabel(); got != "Unknown" {
		t.Errorf("pending (no phase) short label = %q", got)
	}
}

func TestApp_StorageBadge(t *testing.T) {
	cases := []struct {
		state     string
		wantLabel string
		wantClass string
	}{
		{"Alert", "STORAGE ALERT", "b-stale"},
		{"Critical", "STORAGE CRITICAL", "b-degraded"},
		{"OK", "", ""},
		{"", "", ""},
		{"weird", "", ""},
	}
	for _, c := range cases {
		a := App{Storage: Storage{ThresholdState: c.state}}
		if got := a.StorageBadgeLabel(); got != c.wantLabel {
			t.Errorf("StorageBadgeLabel(%q) = %q, want %q", c.state, got, c.wantLabel)
		}
		if got := a.StorageBadgeClass(); got != c.wantClass {
			t.Errorf("StorageBadgeClass(%q) = %q, want %q", c.state, got, c.wantClass)
		}
	}
}

func TestApp_ShowFlow(t *testing.T) {
	cases := []struct {
		name string
		app  App
		want bool
	}{
		{"building app", App{Phase: "Building"}, true},
		{"running build", App{Build: Build{Phase: "Running"}}, true},
		{"failed last result", App{Build: Build{Phase: "Complete", Result: "Failed"}}, true},
		{"healthy idle", App{Phase: "Ready", Build: Build{Phase: "Complete", Result: "Succeeded"}}, false},
		{"empty", App{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.app.ShowFlow(); got != c.want {
				t.Errorf("ShowFlow() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestApp_ServingJobName(t *testing.T) {
	history := []Build{
		{JobName: "b-3", Result: "Failed"},
		{JobName: "b-2", Result: "Succeeded"},
		{JobName: "b-1", Result: "Succeeded"},
	}
	ready := App{
		Conditions:   []Condition{{Type: "Ready", Status: "True"}},
		BuildHistory: history,
	}
	if got := ready.ServingJobName(); got != "b-2" {
		t.Errorf("ServingJobName = %q, want b-2 (newest successful)", got)
	}
	notReady := App{BuildHistory: history}
	if got := notReady.ServingJobName(); got != "" {
		t.Errorf("not-Ready app should have no serving marker, got %q", got)
	}
	noSuccess := App{
		Conditions:   []Condition{{Type: "Ready", Status: "True"}},
		BuildHistory: []Build{{JobName: "b-9", Result: "Failed"}},
	}
	if got := noSuccess.ServingJobName(); got != "" {
		t.Errorf("no successful history should have no serving marker, got %q", got)
	}
}

func TestBuild_Duration(t *testing.T) {
	cases := []struct {
		name  string
		build Build
		want  string
	}{
		{"seconds", Build{StartTime: "2026-06-25T09:00:00Z", CompletionTime: "2026-06-25T09:00:34Z"}, "34s"},
		{"minutes+seconds", Build{StartTime: "2026-06-25T09:00:00Z", CompletionTime: "2026-06-25T09:06:12Z"}, "6m12s"},
		{"no completion", Build{StartTime: "2026-06-25T09:00:00Z"}, ""},
		{"no start", Build{CompletionTime: "2026-06-25T09:00:00Z"}, ""},
		{"completion before start", Build{StartTime: "2026-06-25T09:10:00Z", CompletionTime: "2026-06-25T09:00:00Z"}, ""},
		{"garbage", Build{StartTime: "nope", CompletionTime: "also nope"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.build.Duration(); got != c.want {
				t.Errorf("Duration() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestApp_BuildActive(t *testing.T) {
	cases := []struct {
		name     string
		appPhase string
		bldPhase string
		wantTrue bool
	}{
		{"app building", "Building", "", true},
		{"build running", "Ready", "Running", true},
		{"build pending", "Ready", "Pending", true},
		{"idle", "Ready", "Complete", false},
		{"succeeded", "Ready", "Succeeded", false},
		{"empty", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := App{Phase: c.appPhase, Build: Build{Phase: c.bldPhase}}
			if got := a.BuildActive(); got != c.wantTrue {
				t.Errorf("BuildActive()=%v, want %v", got, c.wantTrue)
			}
		})
	}
}

func TestStep_Duration(t *testing.T) {
	// Freeze the clock so a Running step's live elapsed is deterministic.
	old := Now
	Now = func() time.Time { return time.Date(2026, 6, 25, 9, 2, 0, 0, time.UTC) }
	t.Cleanup(func() { Now = old })

	cases := []struct {
		name string
		step Step
		want string
	}{
		{"finished span trims to compact form", Step{Status: "Succeeded", StartedAt: "2026-06-25T09:00:00Z", FinishedAt: "2026-06-25T09:01:30Z"}, "1m30s"},
		{"running uses live elapsed from Now", Step{Status: "Running", StartedAt: "2026-06-25T09:01:15Z"}, "45s"},
		{"pending with no start renders nothing", Step{Status: "Pending"}, ""},
		{"started but not running and unfinished renders nothing", Step{Status: "Aborted", StartedAt: "2026-06-25T09:00:00Z"}, ""},
		{"garbage start renders nothing", Step{Status: "Succeeded", StartedAt: "nope", FinishedAt: "2026-06-25T09:01:00Z"}, ""},
		{"garbage finish renders nothing", Step{Status: "Succeeded", StartedAt: "2026-06-25T09:00:00Z", FinishedAt: "nope"}, ""},
		{"finish before start (clock skew) renders nothing", Step{Status: "Succeeded", StartedAt: "2026-06-25T09:05:00Z", FinishedAt: "2026-06-25T09:00:00Z"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.step.Duration(); got != c.want {
				t.Errorf("Duration() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestCompactDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{2 * time.Minute, "2m"},     // "2m0s" trims the zero seconds
		{time.Hour, "1h"},           // "1h0m0s" trims both zero units
		{45 * time.Second, "45s"},   // plain seconds stay as-is
		{90 * time.Second, "1m30s"}, // nothing to trim
		{time.Hour + 30*time.Minute, "1h30m"},
		{500 * time.Millisecond, "1s"}, // rounds to seconds
		{100 * time.Millisecond, "0s"}, // sub-second rounds down to "0s"
	}
	for _, c := range cases {
		if got := compactDuration(c.d); got != c.want {
			t.Errorf("compactDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestStepsFrom_TimesAndMemoryLimit(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": "ns", "name": "timed"},
		"status": map[string]any{
			"build": map[string]any{
				"steps": []any{
					map[string]any{
						"name":        "clone",
						"status":      "Succeeded",
						"startedAt":   "2026-06-25T09:00:00Z",
						"finishedAt":  "2026-06-25T09:00:20Z",
						"memoryLimit": "128Mi",
					},
					map[string]any{
						"name":      "build",
						"status":    "Running",
						"startedAt": "2026-06-25T09:00:21Z",
					},
					map[string]any{"name": "copier", "status": "Pending"},
				},
			},
		},
	}}
	a := FromUnstructured(obj)
	if len(a.Build.Steps) != 3 {
		t.Fatalf("want 3 steps, got %d", len(a.Build.Steps))
	}
	clone := a.Build.Steps[0]
	if clone.StartedAt != "2026-06-25T09:00:00Z" || clone.FinishedAt != "2026-06-25T09:00:20Z" {
		t.Errorf("clone timestamps wrong: %+v", clone)
	}
	if clone.MemoryLimit != "128Mi" {
		t.Errorf("clone MemoryLimit = %q, want 128Mi", clone.MemoryLimit)
	}
	build := a.Build.Steps[1]
	if build.StartedAt != "2026-06-25T09:00:21Z" || build.FinishedAt != "" {
		t.Errorf("running step should carry startedAt only: %+v", build)
	}
	// Unrecorded fields stay empty rather than defaulting.
	copier := a.Build.Steps[2]
	if copier.StartedAt != "" || copier.FinishedAt != "" || copier.MemoryLimit != "" {
		t.Errorf("pending step should have no times/limit: %+v", copier)
	}
}

func TestVolumesFrom_PVCCapacityFallback(t *testing.T) {
	// spec.storage supplies a cap only for cache; the output PVC's provisioned
	// capacity (status.storage.capacities) is the fallback bound for output and
	// outputTotal. dataCache has neither → no bar.
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": "ns", "name": "vols"},
		"spec": map[string]any{
			"storage": map[string]any{
				"cache": map[string]any{"cleanupBytes": int64(3000)},
			},
		},
		"status": map[string]any{
			"storage": map[string]any{
				"sizes": map[string]any{
					"output":      int64(500),
					"outputTotal": int64(5000),
					"cache":       int64(1500),
					"dataCache":   int64(42),
				},
				"capacities": map[string]any{
					"output": int64(10000),
					// cache capacity present but must NOT win over the spec cap
					"cache": int64(99999),
				},
			},
		},
	}}
	a := FromUnstructured(obj)
	byName := map[string]StorageVolume{}
	for _, v := range a.Storage.Volumes {
		byName[v.Name] = v
	}

	total := byName["outputTotal"]
	if !total.HasBar || total.Cap != 10000 || total.BarPct != 50 {
		t.Errorf("outputTotal should fall back to the PVC capacity: %+v", total)
	}
	if total.CapHuman != HumanizeBytes(10000) {
		t.Errorf("outputTotal CapHuman = %q, want %q", total.CapHuman, HumanizeBytes(10000))
	}

	// output has no spec cap here either, so it too falls back to the output PVC.
	out := byName["output"]
	if !out.HasBar || out.Cap != 10000 {
		t.Errorf("output should fall back to the PVC capacity when spec has no cap: %+v", out)
	}

	// Spec cap wins over the PVC capacity for cache.
	cache := byName["cache"]
	if !cache.HasBar || cache.Cap != 3000 {
		t.Errorf("cache spec cap must win over PVC capacity: %+v", cache)
	}
	if cache.BarPct != 50 || cache.CapHuman != HumanizeBytes(3000) {
		t.Errorf("cache bar/CapHuman wrong: %+v", cache)
	}

	// No cap and no capacity → size only, no bar.
	dc := byName["dataCache"]
	if dc.HasBar || dc.Cap != 0 || dc.CapHuman != "" {
		t.Errorf("dataCache without cap or capacity must have no bar: %+v", dc)
	}
}

func TestStorageVolume_DisplayNameWithReleases(t *testing.T) {
	cases := []struct {
		name string
		vol  StorageVolume
		want string
	}{
		{"plural releases", StorageVolume{Name: "outputTotal", Releases: 3}, "Output (3 releases)"},
		{"singular release", StorageVolume{Name: "outputTotal", Releases: 1}, "Output (1 release)"},
		{"unreported falls back to the generic label", StorageVolume{Name: "outputTotal"}, "Output (all releases)"},
		{"count only decorates outputTotal", StorageVolume{Name: "output", Releases: 3}, "Output (current release)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.vol.DisplayName(); got != c.want {
				t.Errorf("DisplayName() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestStorageFrom_ReleaseCount(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": "ns", "name": "rels"},
		"status": map[string]any{
			"storage": map[string]any{
				"releaseCount": int64(4),
				"sizes":        map[string]any{"outputTotal": int64(5000), "cache": int64(10)},
			},
		},
	}}
	a := FromUnstructured(obj)
	if a.Storage.ReleaseCount != 4 {
		t.Errorf("Storage.ReleaseCount = %d, want 4", a.Storage.ReleaseCount)
	}
	byName := map[string]StorageVolume{}
	for _, v := range a.Storage.Volumes {
		byName[v.Name] = v
	}
	if got := byName["outputTotal"].Releases; got != 4 {
		t.Errorf("outputTotal Releases = %d, want 4", got)
	}
	if got := byName["cache"].Releases; got != 0 {
		t.Errorf("release count must only be set on outputTotal, cache got %d", got)
	}
	if byName["outputTotal"].DisplayName() != "Output (4 releases)" {
		t.Errorf("outputTotal DisplayName = %q", byName["outputTotal"].DisplayName())
	}
}

func TestCondition_HealthClass(t *testing.T) {
	cases := []struct {
		name     string
		condType string
		status   string
		want     string
	}{
		{"ready true is healthy", "Ready", "True", "true"},
		{"ready false is unhealthy", "Ready", "False", "false"},
		{"degraded false is healthy (negative polarity)", "Degraded", "False", "true"},
		{"degraded true is unhealthy", "Degraded", "True", "false"},
		{"unknown stays unknown", "Ready", "Unknown", "unknown"},
		{"degraded unknown stays unknown", "Degraded", "Unknown", "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cond := Condition{Type: c.condType, Status: c.status}
			if got := cond.HealthClass(); got != c.want {
				t.Errorf("HealthClass()=%q, want %q", got, c.want)
			}
		})
	}
}
