package view

import (
	"reflect"
	"testing"

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
				"failedStep":     "build",
				"startTime":      "2026-06-25T09:50:00Z",
				"completionTime": "2026-06-25T09:55:00Z",
				"attempts":       int64(3),
				"message":        "build failed",
				"logsRef":        "mapswipe/pod/mapswipe-uat-build-7",
				"steps": []any{
					map[string]any{"name": "clone", "status": "Succeeded"},
					map[string]any{"name": "build", "status": "Failed", "message": "yarn build exited 1"},
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
			"nextScheduledBuildTime":  "2026-06-26T09:00:00Z",
			"dataFreshness":           "stale-12h",
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
	if len(a.Build.Steps) != 2 {
		t.Fatalf("want 2 steps, got %d", len(a.Build.Steps))
	}
	if a.Build.Steps[0].Name != "clone" || a.Build.Steps[0].Status != "Succeeded" {
		t.Errorf("step[0] wrong: %+v", a.Build.Steps[0])
	}
	if a.Build.Steps[1].Status != "Failed" || a.Build.Steps[1].Message != "yarn build exited 1" {
		t.Errorf("step[1] wrong: %+v", a.Build.Steps[1])
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
