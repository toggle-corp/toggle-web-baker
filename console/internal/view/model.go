// Package view maps an unstructured FrontendApp custom resource into a flat,
// template-friendly view model. It reads .status defensively against the
// documented schema: every field may be absent, the wrong type, or stale, and
// the console must render whatever it can without panicking. Nothing here
// imports the operator's Go types — we only walk the generic object tree.
package view

import (
	"sort"
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

// Build mirrors status.build.
type Build struct {
	Phase          string
	Result         string
	JobName        string
	StartTime      string
	CompletionTime string
	Attempts       int64
	Message        string
	LogsRef        string
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
	Sizes          []KV // sorted by key for stable rendering
	LastRunDeltas  []KV
}

// KV is a single rendered map entry.
type KV struct {
	Key   string
	Value string
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
	a.Release = releaseFrom(status["release"])
	a.Storage = storageFrom(status["storage"])
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
		StartTime:      asString(m["startTime"]),
		CompletionTime: asString(m["completionTime"]),
		Attempts:       asInt(m["attempts"]),
		Message:        asString(m["message"]),
		LogsRef:        asString(m["logsRef"]),
	}
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

func storageFrom(v any) Storage {
	m, ok := v.(map[string]any)
	if !ok {
		return Storage{}
	}
	return Storage{
		MeasuredAt:     asString(m["measuredAt"]),
		ThresholdState: asString(m["thresholdState"]),
		Sizes:          mapToKV(m["sizes"]),
		LastRunDeltas:  mapToKV(m["lastRunDeltas"]),
	}
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

// mapToKV flattens a string-keyed status map into a key-sorted slice. Values
// are rendered with asString so numbers (byte counts) and strings both work.
func mapToKV(v any) []KV {
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 {
		return nil
	}
	out := make([]KV, 0, len(m))
	for k, val := range m {
		out = append(out, KV{Key: k, Value: asString(val)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Now is overridable in tests; production rebuild timestamps use the real clock.
var Now = func() time.Time { return time.Now().UTC() }
