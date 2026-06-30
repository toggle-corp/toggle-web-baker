// Package view maps an unstructured FrontendApp custom resource into a flat,
// template-friendly view model. It reads .status defensively against the
// documented schema: every field may be absent, the wrong type, or stale, and
// the console must render whatever it can without panicking. Nothing here
// imports the operator's Go types — we only walk the generic object tree.
package view

import (
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Annotation keys the operator observes for a manually requested rebuild.
const (
	AnnotationRebuildRequestedAt = "rebuild.baker.toggle-corp.com/requested-at"
	AnnotationRebuildBy          = "rebuild.baker.toggle-corp.com/by"
)

// Condition is one entry of status.conditions.
type Condition struct {
	Type               string
	Status             string // "True" | "False" | "Unknown"
	Reason             string
	Message            string
	LastTransitionTime string
}

// IsTrue reports whether the condition status is the string "True".
func (c Condition) IsTrue() bool { return c.Status == "True" }

// Step is one entry of a build's ordered per-step timeline (status.build.steps[]
// and each buildHistory[].steps[]). Status ∈ Pending|Running|Succeeded|Failed|Aborted.
type Step struct {
	Name    string
	Status  string
	Message string
}

// Build mirrors status.build and each element of status.buildHistory[] (same shape).
type Build struct {
	Phase          string
	Result         string
	JobName        string
	PodName        string
	Trigger        string // "Scheduled" | "Manual" | "SpecChange"
	StartTime      string
	CompletionTime string
	Attempts       int64
	FailedStep     string
	Message        string
	LogsRef        string
	Steps          []Step
}

// Release mirrors status.release.
type Release struct {
	Current      string
	Previous     string
	ServingSince string
}

// Storage mirrors status.storage.
type Storage struct {
	MeasuredAt     string
	ThresholdState string
	// Volumes is the per-volume rendering: humanized size, last-run delta, and
	// (when a cap is known from spec.storage) a fill bar. Key-sorted.
	Volumes []StorageVolume
}

// StorageVolume is one rendered storage volume row. Bytes is the raw size from
// status.storage.sizes; Cap is the byte cap mapped from spec.storage (0 when
// unknown). HasBar is true only when a positive cap was resolved.
type StorageVolume struct {
	Name   string
	Bytes  int64
	Human  string
	Delta  string
	Cap    int64
	BarPct int
	Over   bool
	HasBar bool
}

// ManualTrigger mirrors status.lastManualTrigger.
type ManualTrigger struct {
	TriggeredBy string
	Time        string
}

// App is the full per-app view model rendered by the detail template; the list
// template uses only the identity + summary fields.
type App struct {
	Namespace string
	Name      string

	ObservedGeneration int64
	Phase              string
	NodeName           string
	URL                string
	SpecStale          bool

	Conditions []Condition
	Build      Build

	// BuildHistory is newest-first, up to 5 entries, each the same shape as Build.
	BuildHistory []Build

	LastProcessedRebuild   string
	LastBuiltSpecHash      string
	LastBuildTime          string
	LastSuccessfulBuild    string
	NextScheduledBuildTime string
	DataFreshness          string

	Release       Release
	Storage       Storage
	ManualTrigger ManualTrigger

	// HasStatus is false when the resource carries no .status yet (freshly
	// created); the templates render an "awaiting first reconcile" note.
	HasStatus bool
}

// Ready / Degraded helpers drive the visual treatment. Ready=True together
// with Degraded=True is the "serving last-good while latest build failed"
// state the brief calls out, so both are exposed independently.
func (a App) Condition(t string) (Condition, bool) {
	for _, c := range a.Conditions {
		if c.Type == t {
			return c, true
		}
	}
	return Condition{}, false
}

// BuildActive reports whether a build is in flight, so the poller can run the
// fast cadence and refresh the live log pane. It mirrors the condition the
// detail page uses for its initial data-active.
func (a App) BuildActive() bool {
	return a.Phase == "Building" || a.Build.Phase == "Running" || a.Build.Phase == "Pending"
}

func (a App) Ready() bool    { c, ok := a.Condition("Ready"); return ok && c.IsTrue() }
func (a App) Degraded() bool { c, ok := a.Condition("Degraded"); return ok && c.IsTrue() }
func (a App) BuildFailed() bool {
	c, ok := a.Condition("BuildSucceeded")
	return ok && c.Status == "False"
}

// HealthClass is the CSS class summarising the app at a glance.
func (a App) HealthClass() string {
	switch {
	case a.Degraded() && a.Ready():
		return "degraded-serving"
	case a.Degraded():
		return "degraded"
	case a.Ready():
		return "ready"
	case a.Phase == "Building":
		return "building"
	default:
		return "pending"
	}
}

// HealthLabel is the human summary paired with HealthClass.
func (a App) HealthLabel() string {
	switch {
	case a.Degraded() && a.Ready():
		return "Serving last-good (latest build failed)"
	case a.Degraded():
		return "Degraded"
	case a.Ready():
		return "Ready"
	case a.Phase == "Building":
		return "Building"
	case a.Phase != "":
		return a.Phase
	default:
		return "Unknown"
	}
}

// FromUnstructured projects a FrontendApp object into the view model. It never
// errors: missing or mistyped fields are simply left at their zero value so a
// half-populated status (mid-reconcile, or an older operator) still renders.
func FromUnstructured(obj *unstructured.Unstructured) App {
	a := App{
		Namespace: obj.GetNamespace(),
		Name:      obj.GetName(),
	}

	status, found, _ := unstructured.NestedMap(obj.Object, "status")
	if !found || status == nil {
		return a
	}
	a.HasStatus = true

	a.ObservedGeneration = asInt(status["observedGeneration"])
	a.Phase = asString(status["phase"])
	a.NodeName = asString(status["nodeName"])
	a.URL = asString(status["url"])
	a.SpecStale = asBool(status["specStale"])

	a.LastProcessedRebuild = asString(status["lastProcessedRebuild"])
	a.LastBuiltSpecHash = asString(status["lastBuiltSpecHash"])
	a.LastBuildTime = asString(status["lastBuildTime"])
	a.LastSuccessfulBuild = asString(status["lastSuccessfulBuildTime"])
	a.NextScheduledBuildTime = asString(status["nextScheduledBuildTime"])
	a.DataFreshness = asString(status["dataFreshness"])

	a.Conditions = conditionsFrom(status["conditions"])
	a.Build = buildFrom(status["build"])
	a.BuildHistory = buildHistoryFrom(status["buildHistory"])
	a.Release = releaseFrom(status["release"])
	specStorage, _, _ := unstructured.NestedMap(obj.Object, "spec", "storage")
	a.Storage = storageFrom(status["storage"], specStorage)
	a.ManualTrigger = manualTriggerFrom(status["lastManualTrigger"])

	return a
}

func conditionsFrom(v any) []Condition {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]Condition, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, Condition{
			Type:               asString(m["type"]),
			Status:             asString(m["status"]),
			Reason:             asString(m["reason"]),
			Message:            asString(m["message"]),
			LastTransitionTime: asString(m["lastTransitionTime"]),
		})
	}
	// Stable, predictable ordering for the template.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

func buildFrom(v any) Build {
	m, ok := v.(map[string]any)
	if !ok {
		return Build{}
	}
	return Build{
		Phase:          asString(m["phase"]),
		Result:         asString(m["result"]),
		JobName:        asString(m["jobName"]),
		PodName:        asString(m["podName"]),
		Trigger:        asString(m["trigger"]),
		StartTime:      asString(m["startTime"]),
		CompletionTime: asString(m["completionTime"]),
		Attempts:       asInt(m["attempts"]),
		FailedStep:     asString(m["failedStep"]),
		Message:        asString(m["message"]),
		LogsRef:        asString(m["logsRef"]),
		Steps:          stepsFrom(m["steps"]),
	}
}

// stepsFrom maps status.build.steps[] (and buildHistory[].steps[]) defensively;
// non-list or non-map entries are skipped, never panicking.
func stepsFrom(v any) []Step {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]Step, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, Step{
			Name:    asString(m["name"]),
			Status:  asString(m["status"]),
			Message: asString(m["message"]),
		})
	}
	return out
}

// buildHistoryFrom maps status.buildHistory[] (newest-first), reusing buildFrom
// so the history rows and the current build share one mapping (DRY).
func buildHistoryFrom(v any) []Build {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]Build, 0, len(list))
	for _, item := range list {
		out = append(out, buildFrom(item))
	}
	return out
}

func releaseFrom(v any) Release {
	m, ok := v.(map[string]any)
	if !ok {
		return Release{}
	}
	return Release{
		Current:      asString(m["current"]),
		Previous:     asString(m["previous"]),
		ServingSince: asString(m["servingSince"]),
	}
}

func storageFrom(v any, specStorage map[string]any) Storage {
	m, ok := v.(map[string]any)
	if !ok {
		return Storage{}
	}
	return Storage{
		MeasuredAt:     asString(m["measuredAt"]),
		ThresholdState: asString(m["thresholdState"]),
		Volumes:        volumesFrom(m["sizes"], m["lastRunDeltas"], specStorage),
	}
}

// volumesFrom builds the rich per-volume rows. Each status.storage.sizes entry
// becomes a StorageVolume; a cap is resolved DEFENSIVELY by normalizing the key
// name (lowercased): containing "output" → spec.storage.output.capBytes;
// "data" → dataCache.cleanupBytes or .alertBytes (first positive); else
// "cache" → cache.cleanupBytes or .alertBytes. No match or cap<=0 → no bar.
func volumesFrom(sizes, deltas, specStorage any) []StorageVolume {
	sizeMap, ok := sizes.(map[string]any)
	if !ok || len(sizeMap) == 0 {
		return nil
	}
	deltaMap, _ := deltas.(map[string]any)

	out := make([]StorageVolume, 0, len(sizeMap))
	for name, raw := range sizeMap {
		bytes := asInt(raw)
		capBytes := capForKey(name, specStorage)
		v := StorageVolume{
			Name:  name,
			Bytes: bytes,
			Human: HumanizeBytes(bytes),
			Delta: HumanizeDelta(asInt(deltaMap[name])),
			Cap:   capBytes,
		}
		if capBytes > 0 {
			pct, over := StorageBar(bytes, capBytes)
			if pct != StorageBarNoBar {
				v.HasBar = true
				v.BarPct = pct
				v.Over = over
			}
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// capForKey resolves a volume's byte cap from spec.storage by fuzzy-matching the
// status size key. Order matters: "output" is checked before "cache" because a
// hypothetical "output-cache" should map to the output cap.
func capForKey(key string, specStorage any) int64 {
	spec, ok := specStorage.(map[string]any)
	if !ok {
		return 0
	}
	k := strings.ToLower(key)
	switch {
	case strings.Contains(k, "output"):
		return nestedInt(spec, "output", "capBytes")
	case strings.Contains(k, "data"):
		return firstPositive(
			nestedInt(spec, "dataCache", "cleanupBytes"),
			nestedInt(spec, "dataCache", "alertBytes"),
		)
	case strings.Contains(k, "cache"):
		return firstPositive(
			nestedInt(spec, "cache", "cleanupBytes"),
			nestedInt(spec, "cache", "alertBytes"),
		)
	default:
		return 0
	}
}

// nestedInt reads spec[group][field] as an int64, defensively (0 on any miss).
func nestedInt(spec map[string]any, group, field string) int64 {
	g, ok := spec[group].(map[string]any)
	if !ok {
		return 0
	}
	return asInt(g[field])
}

func firstPositive(vals ...int64) int64 {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

func manualTriggerFrom(v any) ManualTrigger {
	m, ok := v.(map[string]any)
	if !ok {
		return ManualTrigger{}
	}
	return ManualTrigger{
		TriggeredBy: asString(m["triggeredBy"]),
		Time:        asString(m["time"]),
	}
}

// Now is overridable in tests; production rebuild timestamps use the real clock.
var Now = func() time.Time { return time.Now().UTC() }
