// Package view maps an unstructured App custom resource into a flat,
// template-friendly view model. It reads .status defensively against the
// documented schema: every field may be absent, the wrong type, or stale, and
// the console must render whatever it can without panicking. Nothing here
// imports the operator's Go types — we only walk the generic object tree.
package view

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Annotation keys the operator observes for a manually requested rebuild.
const (
	AnnotationRebuildRequestedAt = "rebuild.baker.toggle-corp.com/requested-at"
	AnnotationRebuildBy          = "rebuild.baker.toggle-corp.com/by"

	// AnnotationRebuildCommit carries the SHA that triggered a commit-watch
	// build; the console clears it alongside setting "by" on a manual rebuild so
	// the operator can't misclassify the manual build as Commit. Duplicated as a
	// plain string like the other keys.
	AnnotationRebuildCommit = "rebuild.baker.toggle-corp.com/commit"

	// AnnotationWatchLastSeen is the commit watcher's dedup state (last remote
	// SHA it observed) — display-only here.
	AnnotationWatchLastSeen = "watch.baker.toggle-corp.com/last-seen-sha"

	// Annotation keys the operator observes for the two cleanup actions. Duplicated
	// here as plain strings (not imported from the operator) exactly as the rebuild
	// keys are — the console never imports the operator's Go types.
	AnnotationCleanupCacheRequestedAt    = "cleanup-cache.baker.toggle-corp.com/requested-at"
	AnnotationCleanupCacheBy             = "cleanup-cache.baker.toggle-corp.com/by"
	AnnotationCleanupReleasesRequestedAt = "cleanup-releases.baker.toggle-corp.com/requested-at"
	AnnotationCleanupReleasesBy          = "cleanup-releases.baker.toggle-corp.com/by"
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

// HealthClass maps the condition to its display color class ("true" green,
// "false" red, "unknown" muted) by HEALTHINESS, not raw status: Degraded is a
// negative-polarity condition, so Degraded=False is the healthy (green) value
// and Degraded=True the unhealthy (red) one.
func (c Condition) HealthClass() string {
	healthyWhenTrue := c.Type != "Degraded"
	switch c.Status {
	case "True", "False":
		if (c.Status == "True") == healthyWhenTrue {
			return "true"
		}
		return "false"
	default:
		return "unknown"
	}
}

// Step is one entry of a build's ordered per-step timeline (status.build.steps[]
// and each buildHistory[].steps[]). Status ∈ Pending|Running|Succeeded|Failed|Aborted.
type Step struct {
	Name    string
	Status  string
	Message string
	// PeakMemory is the humanized true peak memory of the phase (from
	// status peakMemoryBytes, the shim-recorded cgroup high-water mark).
	// Empty when unmeasured — shim-less steps (clone/copier/release) or a
	// still-running phase.
	PeakMemory string
	// MemoryLimit is the memory ceiling the step's container ran with (raw
	// quantity string from status, e.g. "2Gi") — the "allocated" figure paired
	// with PeakMemory in the flow-strip tooltip. Empty when unrecorded.
	MemoryLimit string
	// StartedAt / FinishedAt are the step container's RFC3339 timestamps from
	// status (startedAt/finishedAt). FinishedAt is empty while running.
	StartedAt  string
	FinishedAt string
}

// Duration renders the step's runtime compactly ("48s", "2m", "2m30s"): the
// finished span for a terminated step, or the live elapsed-so-far for a Running
// one (the fragment re-renders every few seconds, so it ticks). Empty when the
// step has not started, timestamps are unparseable, or the span is negative
// (clock skew).
func (s Step) Duration() string {
	start, err := time.Parse(time.RFC3339, s.StartedAt)
	if err != nil {
		return ""
	}
	end := Now()
	if s.FinishedAt != "" {
		t, err := time.Parse(time.RFC3339, s.FinishedAt)
		if err != nil {
			return ""
		}
		end = t
	} else if s.Status != "Running" {
		return ""
	}
	d := end.Sub(start)
	if d < 0 {
		return ""
	}
	return compactDuration(d)
}

// compactDuration renders a duration rounded to seconds with zero-valued
// trailing units dropped: "2m0s" → "2m", "1h0m0s" → "1h" (Go's String() keeps
// them). Sub-second spans render "0s".
func compactDuration(d time.Duration) string {
	out := d.Round(time.Second).String()
	if strings.HasSuffix(out, "m0s") {
		out = strings.TrimSuffix(out, "0s")
	}
	if strings.HasSuffix(out, "h0m") {
		out = strings.TrimSuffix(out, "0m")
	}
	return out
}

// Build mirrors status.build and each element of status.buildHistory[] (same shape).
type Build struct {
	Phase          string
	Result         string
	JobName        string
	PodName        string
	Trigger        string // "Scheduled" | "Manual" | "Commit" | "SpecChange"
	TriggeredBy    string // github username for an attributed manual build; empty otherwise
	Commit         string // SHA that triggered a commit-watch build; empty otherwise
	StartTime      string
	CompletionTime string
	Attempts       int64
	FailedStep     string
	Message        string
	LogsRef        string
	Steps          []Step
	// Termination carries a container's terminated reason (e.g. OOMKilled) when
	// the operator recorded one; nil when absent or mistyped in status.
	Termination *Termination
}

// Termination is a view-local mirror of status.build.termination (and each
// buildHistory[].termination — same shape). It is deliberately NOT the
// operator's type: the console only ever walks the generic object tree.
type Termination struct {
	Reason      string
	Container   string
	MemoryLimit string // quantity string, e.g. "256Mi" — rendered as-is
	FinishedAt  string
	ExitCode    int64
}

// IsOOM reports whether this build was terminated by the OOM killer, driving the
// loud memory-limit callout in the template.
func (b Build) IsOOM() bool {
	return b.Termination != nil && b.Termination.Reason == "OOMKilled"
}

// TerminationSummary renders a one-line human summary of the build's
// termination, kept here (not in the template) so it stays logic-free and
// unit-testable. It returns the empty string when there is no termination.
// For OOM it reads "OOM Killed — the <container> step exceeded its <limit>
// memory limit." (the "its <limit>" clause is dropped when the limit is
// unknown). For any other non-empty reason it reads
// "Terminated: <reason> (exit <exitCode>)".
func (b Build) TerminationSummary() string {
	if b.Termination == nil || b.Termination.Reason == "" {
		return ""
	}
	if b.IsOOM() {
		if b.Termination.MemoryLimit != "" {
			return fmt.Sprintf("OOM Killed — the %s step exceeded its %s memory limit.", b.Termination.Container, b.Termination.MemoryLimit)
		}
		return fmt.Sprintf("OOM Killed — the %s step exceeded its memory limit.", b.Termination.Container)
	}
	return fmt.Sprintf("Terminated: %s (exit %d)", b.Termination.Reason, b.Termination.ExitCode)
}

// TriggerLabel renders the build trigger with its provenance when known:
// "Manual · octocat" for an attributed manual build, "Commit · cafebab" (short
// SHA) for a commit-watch build, else just the trigger ("Scheduled"), or the
// em-dash (matching the `dash` template func) when the trigger is unknown. Kept
// here — not in the template — so it stays logic-free and unit-testable.
// TriggeredBy/Commit are only meaningful alongside a trigger, so provenance
// with no trigger still renders the em-dash.
func (b Build) TriggerLabel() string {
	if b.Trigger == "" {
		return "—"
	}
	if b.TriggeredBy != "" {
		return b.Trigger + " · " + b.TriggeredBy
	}
	if b.Commit != "" {
		return b.Trigger + " · " + ShortSHA(b.Commit)
	}
	return b.Trigger
}

// Duration renders the build's wall-clock duration ("34s", "6m12s") from its
// start/completion timestamps. Empty when either timestamp is missing or
// unparseable, or when completion precedes start (clock skew).
func (b Build) Duration() string {
	start, err := time.Parse(time.RFC3339, b.StartTime)
	if err != nil {
		return ""
	}
	end, err := time.Parse(time.RFC3339, b.CompletionTime)
	if err != nil {
		return ""
	}
	d := end.Sub(start)
	if d < 0 {
		return ""
	}
	return d.Round(time.Second).String()
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
	// ReleaseCount is the copier-reported number of release dirs retained on
	// the output PVC (0 = never reported), surfaced in the outputTotal label.
	ReleaseCount int64
	// Volumes is the per-volume rendering: humanized size, last-run delta, and
	// (when a cap is known from spec.storage or the PVC capacity) a fill bar.
	// Key-sorted.
	Volumes []StorageVolume
}

// StorageVolume is one rendered storage volume row. Bytes is the raw size from
// status.storage.sizes; Cap is the byte cap resolved from spec.storage, falling
// back to the PVC's provisioned capacity (status.storage.capacities) so every
// PVC-backed volume gets a bar. HasBar is true only when a positive cap was
// resolved; CapHuman is its humanized form for the "used / cap" pairing.
type StorageVolume struct {
	Name     string
	Bytes    int64
	Human    string
	Delta    string
	Cap      int64
	CapHuman string
	BarPct   int
	Over     bool
	HasBar   bool
	// Releases is the retained-release count, set ONLY on the outputTotal row
	// (0 elsewhere / when never reported) to drive its "(N releases)" label.
	Releases int64
}

// DisplayName is the human label for the volume row; the raw status key (Name)
// is not friendly. It delegates to volumeLabel so the mapping stays a pure,
// unit-testable helper — except outputTotal, whose label carries the REAL
// retained-release count when the copier has reported one. Name itself is kept
// intact — sorting and tests rely on it.
func (v StorageVolume) DisplayName() string {
	if v.Name == "outputTotal" && v.Releases > 0 {
		if v.Releases == 1 {
			return "Output (1 release)"
		}
		return fmt.Sprintf("Output (%d releases)", v.Releases)
	}
	return volumeLabel(v.Name)
}

// volumeLabel maps a status.storage.sizes key to a readable card label. Unmapped
// keys fall through to the raw key so a new operator key still renders (unpretty
// but never blank).
func volumeLabel(key string) string {
	switch key {
	case "output":
		return "Output (current release)"
	case "outputTotal":
		return "Output (all releases)"
	case "cache":
		return "Cache"
	case "dataCache":
		return "Data cache"
	default:
		return key
	}
}

// CleanupAction mirrors one entry of status.cleanup ({cache,releases}). Phase ∈
// Pending|Running|Succeeded|Failed.
type CleanupAction struct {
	RequestedAt, RequestedBy, Phase, LastCompleted, Message string
	ReclaimedBytes                                          int64
}

// Cleanup mirrors status.cleanup, the operator's report of the two prune actions.
type Cleanup struct {
	Cache, Releases CleanupAction
}

// ManualTrigger mirrors status.lastManualTrigger.
type ManualTrigger struct {
	TriggeredBy string
	Time        string
}

// ScheduledBuilds mirrors spec.scheduledBuilds (absent → zero value, disabled).
// An empty Schedule with Enabled=true means the operator's config default,
// which the console cannot know — templates render "operator default".
type ScheduledBuilds struct {
	Enabled  bool
	Schedule string
}

// WatchCommits mirrors spec.watchCommits plus the watcher's last-seen-SHA
// annotation. An empty Interval with Enabled=true means the operator default.
type WatchCommits struct {
	Enabled     bool
	Interval    string
	LastSeenSHA string
}

// Label renders the scheduled-builds config state ("Disabled" / "operator
// default" / the cron expression). Kept in Go — not the template — so the
// enabled/tuned/disabled precedence stays logic-free in templates and
// unit-tested once (same rationale as Build.TriggerLabel).
func (s ScheduledBuilds) Label() string {
	switch {
	case !s.Enabled:
		return "Disabled"
	case s.Schedule == "":
		return "operator default"
	default:
		return s.Schedule
	}
}

// Label renders the watch-commits config state ("Disabled" / "operator
// default" / "every <interval>").
func (w WatchCommits) Label() string {
	switch {
	case !w.Enabled:
		return "Disabled"
	case w.Interval == "":
		return "operator default"
	default:
		return "every " + w.Interval
	}
}

// App is the full per-app view model rendered by the detail template; the list
// template uses only the identity + summary fields.
type App struct {
	Namespace string
	Name      string

	// Group is the optional spec.group label ("" when unset — read defensively;
	// older resources have no such field). It drives the list page's group
	// filter chips and the small group text under the app name.
	Group string

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

	Release       Release
	Storage       Storage
	Cleanup       Cleanup
	ManualTrigger ManualTrigger

	// Repo is spec.repo, kept for commit-link derivation (CommitURL).
	Repo            string
	ScheduledBuilds ScheduledBuilds
	WatchCommits    WatchCommits

	// HasStatus is false when the resource carries no .status yet (freshly
	// created); the templates render an "awaiting first reconcile" note.
	HasStatus bool

	// BuildMetrics is the live usage of the build pod's active container. It is
	// populated by the server AFTER the live kubelet-stats fetch, NOT from
	// .status, and is nil when there is no running build / the kubelet stats are
	// unreachable.
	BuildMetrics *BuildMetrics
	// BuildMetricsNote is set by the server ONLY when a build is live
	// (BuildActive && PodName != "") but the metrics fetch failed, so a
	// misconfiguration (e.g. missing nodes/proxy RBAC) is visible while idle
	// stays clean.
	BuildMetricsNote string
}

// BuildMetrics carries the build pod's active-container live memory usage,
// populated by the handler. It is nil on the idle path. HasMemBar gates the %
// bar, which only renders when a positive memory limit was resolved from the
// pod. CPU is deliberately not exposed: the console's usage block shows memory
// only (memory is what OOM-kills builds; CPU is throttled, never fatal).
type BuildMetrics struct {
	Container     string
	MemoryBytes   int64
	MemoryHuman   string // reuse HumanizeBytes
	MemLimitBytes int64  // 0 = unknown (no bar)
	MemLimitHuman string // humanized limit for the "used / available" pairing; "" when unknown
	MemBarPct     int
	MemOver       bool
	HasMemBar     bool
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

// Active reports whether this cleanup action is in flight (Pending or Running).
func (c CleanupAction) Active() bool { return c.Phase == "Pending" || c.Phase == "Running" }

// ReclaimedHuman renders ReclaimedBytes via HumanizeBytes for the template.
func (c CleanupAction) ReclaimedHuman() string { return HumanizeBytes(c.ReclaimedBytes) }

// CleanupActive reports whether either cleanup action is in flight.
func (a App) CleanupActive() bool { return a.Cleanup.Cache.Active() || a.Cleanup.Releases.Active() }

// CleanupBusy gates the cleanup buttons: a cleanup is serialized behind builds
// and other cleanups, so the buttons disable while a build OR a cleanup runs.
func (a App) CleanupBusy() bool { return a.BuildActive() || a.CleanupActive() }

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

// HealthShortLabel is the compact list-row variant of HealthLabel: the
// degraded-serving state shrinks to "Serving last-good" (the full explanation
// lives in the badge title / detail page); everything else is unchanged. The
// health badge itself carries the serving-last-good message, so the list needs
// no extra SERVING LAST-GOOD badge.
func (a App) HealthShortLabel() string {
	if a.HealthClass() == "degraded-serving" {
		return "Serving last-good"
	}
	return a.HealthLabel()
}

// HealthRank orders list rows most-broken-first: degraded, degraded-serving,
// building, pending, ready. Sorting is stable within a rank.
func (a App) HealthRank() int {
	switch a.HealthClass() {
	case "degraded":
		return 0
	case "degraded-serving":
		return 1
	case "building":
		return 2
	case "pending":
		return 3
	default: // ready
		return 4
	}
}

// URLHost is the bare host of the app URL for compact display ("mapswipe.org"
// for "https://mapswipe.org/x"). It falls back to the raw URL when parsing
// yields no host, so a malformed status URL still renders something.
func (a App) URLHost() string {
	if a.URL == "" {
		return ""
	}
	if u, err := url.Parse(a.URL); err == nil && u.Host != "" {
		return u.Host
	}
	return a.URL
}

// StorageBadgeLabel is the list-row storage badge text: "STORAGE ALERT" /
// "STORAGE CRITICAL" when status.storage.thresholdState is Alert / Critical,
// else "" (no badge). Paired with StorageBadgeClass.
func (a App) StorageBadgeLabel() string {
	switch a.Storage.ThresholdState {
	case "Alert":
		return "STORAGE ALERT"
	case "Critical":
		return "STORAGE CRITICAL"
	default:
		return ""
	}
}

// StorageBadgeClass is the badge CSS class paired with StorageBadgeLabel:
// amber (b-stale) for Alert, red (b-degraded) for Critical, "" otherwise.
func (a App) StorageBadgeClass() string {
	switch a.Storage.ThresholdState {
	case "Alert":
		return "b-stale"
	case "Critical":
		return "b-degraded"
	default:
		return ""
	}
}

// ShowFlow gates the list-row flow strip: only rows with a build in flight or
// a failed last result need the step detail; healthy rows carry their state in
// the health badge alone.
func (a App) ShowFlow() bool {
	return a.BuildActive() || a.Build.Result == "Failed"
}

// ServingJobName is the job name of the newest successful history build while
// the app is Ready — the row the Recent-builds timeline marks "← serving".
// Empty when the app is not Ready or no successful build is in the history.
func (a App) ServingJobName() string {
	if !a.Ready() {
		return ""
	}
	for _, b := range a.BuildHistory {
		if b.Result == "Succeeded" {
			return b.JobName
		}
	}
	return ""
}

// storageTotals walks Storage.Volumes ONCE, bucketing by volume name into a
// StorageTotals. Grand is the non-overlapping footprint (Cache + DataCache +
// OutputTotal); OutputActive is a subset of OutputTotal, so it is set but never
// added into Grand. It is the single reader behind the per-category getters, the
// per-row total/tooltip, and AggregateStorage — the slice is scanned once, not
// once per category.
func (a App) storageTotals() StorageTotals {
	var t StorageTotals
	for _, v := range a.Storage.Volumes {
		switch v.Name {
		case "cache":
			t.Cache = v.Bytes
		case "dataCache":
			t.DataCache = v.Bytes
		case "outputTotal":
			t.OutputTotal = v.Bytes
		case "output":
			t.OutputActive = v.Bytes
		}
	}
	t.Grand = t.Cache + t.DataCache + t.OutputTotal
	return t
}

// StorageCacheBytes is the build cache volume's size (0 if absent).
func (a App) StorageCacheBytes() int64 { return a.storageTotals().Cache }

// StorageDataCacheBytes is the data cache volume's size (0 if absent).
func (a App) StorageDataCacheBytes() int64 { return a.storageTotals().DataCache }

// StorageOutputTotalBytes is the output volume across ALL retained releases (0
// if absent).
func (a App) StorageOutputTotalBytes() int64 { return a.storageTotals().OutputTotal }

// StorageOutputActiveBytes is the CURRENT (active) release's output size (0 if
// absent). Informational only — a subset of StorageOutputTotalBytes, never
// summed into the grand total.
func (a App) StorageOutputActiveBytes() int64 { return a.storageTotals().OutputActive }

// StorageTotalBytes is the app's non-overlapping physical footprint: cache +
// dataCache + outputTotal. The active release (OutputActive) is a subset of
// outputTotal, so it is deliberately NOT added — that would double-count.
func (a App) StorageTotalBytes() int64 { return a.storageTotals().Grand }

// StorageTotalHuman is the app's non-overlapping footprint (StorageTotalBytes)
// humanized — the value shown in the list-row Storage cell.
func (a App) StorageTotalHuman() string { return a.storageTotals().GrandHuman() }

// StorageTooltip is the list-row Storage cell's title=, breaking the total down
// by category: "Cache 4.0 GiB · Data cache 2.0 GiB · Output 6.4 GiB (active
// 3.0 GiB)". Active is a subset of Output, shown parenthetically. Labels match
// the list-heading roll-up.
func (a App) StorageTooltip() string {
	t := a.storageTotals()
	return fmt.Sprintf("Cache %s · Data cache %s · Output %s (active %s)",
		t.CacheHuman(), t.DataCacheHuman(), t.OutputHuman(), t.ActiveHuman(),
	)
}

// StorageTotals is a cross-app storage roll-up by category. Grand is the
// non-overlapping footprint (Cache + DataCache + OutputTotal); OutputActive is
// informational only (a subset of OutputTotal) and is NOT part of Grand.
type StorageTotals struct {
	Cache        int64
	DataCache    int64
	OutputTotal  int64
	OutputActive int64
	Grand        int64
}

// Humanized accessors for the list-heading roll-up, so the template stays
// logic-free. OutputHuman/ActiveHuman name the outputTotal/output figures.
func (t StorageTotals) GrandHuman() string     { return HumanizeBytes(t.Grand) }
func (t StorageTotals) CacheHuman() string     { return HumanizeBytes(t.Cache) }
func (t StorageTotals) DataCacheHuman() string { return HumanizeBytes(t.DataCache) }
func (t StorageTotals) OutputHuman() string    { return HumanizeBytes(t.OutputTotal) }
func (t StorageTotals) ActiveHuman() string    { return HumanizeBytes(t.OutputActive) }

// AggregateStorage sums each storage category across apps and sets Grand to the
// non-overlapping footprint (Cache + DataCache + OutputTotal). OutputActive is
// summed for display but excluded from Grand. Each app's volumes are walked once
// (storageTotals), not once per category.
func AggregateStorage(apps []App) StorageTotals {
	var t StorageTotals
	for _, a := range apps {
		at := a.storageTotals()
		t.Cache += at.Cache
		t.DataCache += at.DataCache
		t.OutputTotal += at.OutputTotal
		t.OutputActive += at.OutputActive
	}
	t.Grand = t.Cache + t.DataCache + t.OutputTotal
	return t
}

// FromUnstructured projects a App object into the view model. It never
// errors: missing or mistyped fields are simply left at their zero value so a
// half-populated status (mid-reconcile, or an older operator) still renders.
func FromUnstructured(obj *unstructured.Unstructured) App {
	a := App{
		Namespace: obj.GetNamespace(),
		Name:      obj.GetName(),
	}

	// Trigger config is derived from the spec (the App status carries no
	// next-build field). Compute it BEFORE the status guard so a freshly-created
	// app (valid spec, not yet reconciled) already shows its trigger setup.
	spec, _, _ := unstructured.NestedMap(obj.Object, "spec")
	a.Repo = asString(spec["repo"])
	if sb, ok := spec["scheduledBuilds"].(map[string]any); ok {
		a.ScheduledBuilds = ScheduledBuilds{
			Enabled:  asBool(sb["enabled"]),
			Schedule: asString(sb["schedule"]),
		}
	}
	if wc, ok := spec["watchCommits"].(map[string]any); ok {
		a.WatchCommits = WatchCommits{
			Enabled:  asBool(wc["enabled"]),
			Interval: asString(wc["interval"]),
		}
	}
	a.WatchCommits.LastSeenSHA = obj.GetAnnotations()[AnnotationWatchLastSeen]
	a.NextScheduledBuildTime = nextScheduled(a.ScheduledBuilds)

	// spec.group is optional and newer than some deployed resources — read it
	// defensively (missing/mistyped → ""), and before the status guard so a
	// freshly-created app is already filterable by group.
	a.Group = asString(spec["group"])

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

	a.Conditions = conditionsFrom(status["conditions"])
	a.Build = buildFrom(status["build"])
	a.BuildHistory = buildHistoryFrom(status["buildHistory"])
	a.Release = releaseFrom(status["release"])
	specStorage, _, _ := unstructured.NestedMap(obj.Object, "spec", "storage")
	a.Storage = storageFrom(status["storage"], specStorage)
	a.Cleanup = cleanupFrom(status["cleanup"])
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
		TriggeredBy:    asString(m["triggeredBy"]),
		Commit:         asString(m["commit"]),
		StartTime:      asString(m["startTime"]),
		CompletionTime: asString(m["completionTime"]),
		Attempts:       asInt(m["attempts"]),
		FailedStep:     asString(m["failedStep"]),
		Message:        asString(m["message"]),
		LogsRef:        asString(m["logsRef"]),
		Steps:          stepsFrom(m["steps"]),
		Termination:    terminationFrom(m["termination"]),
	}
}

// terminationFrom maps status.build.termination (and each
// buildHistory[].termination) defensively. It returns nil when the value is
// absent or not a map, so a status from an older operator simply carries no
// termination rather than panicking, mirroring the other *From helpers.
func terminationFrom(v any) *Termination {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return &Termination{
		Reason:      asString(m["reason"]),
		Container:   asString(m["container"]),
		MemoryLimit: asString(m["memoryLimit"]),
		FinishedAt:  asString(m["finishedAt"]),
		ExitCode:    asInt(m["exitCode"]),
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
		st := Step{
			Name:        asString(m["name"]),
			Status:      asString(m["status"]),
			Message:     asString(m["message"]),
			MemoryLimit: asString(m["memoryLimit"]),
			StartedAt:   asString(m["startedAt"]),
			FinishedAt:  asString(m["finishedAt"]),
		}
		if b := asInt(m["peakMemoryBytes"]); b > 0 {
			st.PeakMemory = HumanizeBytes(b)
		}
		out = append(out, st)
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
	releaseCount := asInt(m["releaseCount"])
	return Storage{
		MeasuredAt:     asString(m["measuredAt"]),
		ThresholdState: asString(m["thresholdState"]),
		ReleaseCount:   releaseCount,
		Volumes:        volumesFrom(m["sizes"], m["lastRunDeltas"], specStorage, m["capacities"], releaseCount),
	}
}

// volumesFrom builds the rich per-volume rows. Each status.storage.sizes entry
// becomes a StorageVolume; a cap is resolved DEFENSIVELY by normalizing the key
// name (lowercased): containing "output" → spec.storage.output.capBytes;
// "data" → dataCache.cleanupBytes or .alertBytes (first positive); else
// "cache" → cache.cleanupBytes or .alertBytes. When spec.storage yields no cap,
// the PVC's provisioned capacity (status.storage.capacities, keyed by the same
// volume names) is the fallback — the physical bound, so every PVC-backed
// volume gets a fill bar like the memory one. No cap at all → no bar.
func volumesFrom(sizes, deltas, specStorage, capacities any, releaseCount int64) []StorageVolume {
	sizeMap, ok := sizes.(map[string]any)
	if !ok || len(sizeMap) == 0 {
		return nil
	}
	deltaMap, _ := deltas.(map[string]any)
	capacityMap, _ := capacities.(map[string]any)

	out := make([]StorageVolume, 0, len(sizeMap))
	for name, raw := range sizeMap {
		bytes := asInt(raw)
		capBytes := capForKey(name, specStorage)
		if capBytes <= 0 {
			capBytes = capacityForKey(name, capacityMap)
		}
		v := StorageVolume{
			Name:  name,
			Bytes: bytes,
			Human: HumanizeBytes(bytes),
			Delta: HumanizeDelta(asInt(deltaMap[name])),
			Cap:   capBytes,
		}
		if name == "outputTotal" {
			v.Releases = releaseCount
		}
		if capBytes > 0 {
			v.CapHuman = HumanizeBytes(capBytes)
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
// status size key. Order matters: "total" is checked before "output" (a total is
// not bounded by the per-release cap), and "output" before "cache" because a
// hypothetical "output-cache" should map to the output cap.
func capForKey(key string, specStorage any) int64 {
	spec, ok := specStorage.(map[string]any)
	if !ok {
		return 0
	}
	k := strings.ToLower(key)
	switch {
	case strings.Contains(k, "total"):
		// "outputTotal" is the whole output PVC across all retained releases; the
		// per-release output.capBytes bounds a single release, not the total, and no
		// total cap exists in the spec. Its bound is the PVC capacity fallback.
		return 0
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

// capacityForKey maps a status size key to its PVC's provisioned capacity from
// status.storage.capacities (keys: cache / dataCache / output). The same fuzzy
// normalization as capForKey: outputTotal AND output both live on the output
// PVC; "data" before "cache" so dataCache never matches the cache PVC.
func capacityForKey(key string, capacityMap map[string]any) int64 {
	if len(capacityMap) == 0 {
		return 0
	}
	k := strings.ToLower(key)
	switch {
	case strings.Contains(k, "output"):
		return asInt(capacityMap["output"])
	case strings.Contains(k, "data"):
		return asInt(capacityMap["dataCache"])
	case strings.Contains(k, "cache"):
		return asInt(capacityMap["cache"])
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

// cleanupFrom maps status.cleanup defensively; a missing/mistyped value yields a
// zero Cleanup, mirroring releaseFrom/manualTriggerFrom.
func cleanupFrom(v any) Cleanup {
	m, ok := v.(map[string]any)
	if !ok {
		return Cleanup{}
	}
	return Cleanup{
		Cache:    cleanupActionFrom(m["cache"]),
		Releases: cleanupActionFrom(m["releases"]),
	}
}

func cleanupActionFrom(v any) CleanupAction {
	m, ok := v.(map[string]any)
	if !ok {
		return CleanupAction{}
	}
	// completedAt (metav1.Time) replaced the legacy lastCompleted string in the
	// operator; keep reading the old key so a console pointed at a cluster with
	// an older operator still renders the completion time.
	completed := asString(m["completedAt"])
	if completed == "" {
		completed = asString(m["lastCompleted"])
	}
	return CleanupAction{
		RequestedAt:    asString(m["requestedAt"]),
		RequestedBy:    asString(m["requestedBy"]),
		Phase:          asString(m["phase"]),
		LastCompleted:  completed,
		Message:        asString(m["message"]),
		ReclaimedBytes: asInt(m["reclaimedBytes"]),
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

// Now is overridable in tests; production rebuild timestamps use the real clock.
var Now = func() time.Time { return time.Now().UTC() }
