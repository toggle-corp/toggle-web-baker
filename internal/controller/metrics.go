package controller

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
)

// The purpose-built App metric set the PrometheusRule alerts key on.
var (
	metricDegraded = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "baker_app_degraded",
		Help: "1 when the app's Degraded condition is True (reason = condition reason); 0 with reason=\"\" when healthy.",
	}, []string{"namespace", "name", "reason"})
	metricPhase = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "baker_app_phase",
		Help: "0/1 per lifecycle phase; all four phases are always exported and exactly one is 1.",
	}, []string{"namespace", "name", "phase"})
	metricBuildsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "baker_app_builds_total",
		Help: "Terminal builds by result (Succeeded|Failed).",
	}, []string{"namespace", "name", "result"})
	metricBuildOOMTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "baker_app_build_oom_total",
		Help: "Builds whose step container was OOMKilled.",
	}, []string{"namespace", "name", "step"})
	metricBuildRunningSince = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "baker_app_build_running_since_seconds",
		Help: "Unix start time of the in-flight build; 0 when no build is running.",
	}, []string{"namespace", "name"})
	metricBuildDeadline = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "baker_app_build_deadline_seconds",
		Help: "Build Job activeDeadlineSeconds (spec.pipeline.timeout or the operator default).",
	}, []string{"namespace", "name"})
	metricStorageUsed = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "baker_app_storage_used_bytes",
		Help: "Last measured per-volume usage (status.storage.sizes).",
	}, []string{"namespace", "name", "volume"})
	metricStorageAlert = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "baker_app_storage_alert_bytes",
		Help: "spec.storage alertBytes threshold; exported only for volumes with a threshold > 0.",
	}, []string{"namespace", "name", "volume"})
)

// appMetricVec is the common surface of the Gauge/Counter vecs above
// (registration, per-app series deletion, test reset).
type appMetricVec interface {
	prometheus.Collector
	Reset()
	DeletePartialMatch(prometheus.Labels) int
}

var allAppMetricVecs = []appMetricVec{
	metricDegraded,
	metricPhase,
	metricBuildsTotal,
	metricBuildOOMTotal,
	metricBuildRunningSince,
	metricBuildDeadline,
	metricStorageUsed,
	metricStorageAlert,
}

func init() {
	for _, v := range allAppMetricVecs {
		ctrlmetrics.Registry.MustRegister(v)
	}
}

// appMetricsState is the per-app bookkeeping the Recorder needs to keep the
// exported series exact: counter dedup, stale-series deletion, and the frozen
// build deadline.
type appMetricsState struct {
	lastCountedJob     string
	lastDegradedReason string
	// frozenDeadline pins build_deadline_seconds to the value in force when the
	// in-flight build was first recorded, so a mid-build spec.pipeline.timeout
	// edit cannot shift the BuildStuck alert's baseline under a running build.
	// 0 = no build in flight (the live deadline is exported).
	frozenDeadline int64
}

// Recorder writes the App metric set. The zero value is fully usable:
// the per-app state map is initialized lazily under the Recorder's own mutex.
type Recorder struct {
	mu   sync.Mutex
	apps map[types.NamespacedName]*appMetricsState
}

// state returns (creating if needed) the app's bookkeeping entry. Callers must
// hold mu. First creation pre-seeds the app's counters at 0: `increase()`
// alerts only fire on a step the range vector can see, so the series must be
// born at 0 BEFORE the first real failure — a series born at 1 never fires.
func (m *Recorder) state(app *bakerv1alpha1.App) *appMetricsState {
	key := types.NamespacedName{Namespace: app.Namespace, Name: app.Name}
	if m.apps == nil {
		m.apps = map[types.NamespacedName]*appMetricsState{}
	}
	s, ok := m.apps[key]
	if !ok {
		s = &appMetricsState{}
		m.apps[key] = s
		seedCounters(app.Namespace, app.Name)
	}
	return s
}

// seedCounters Add(0)s every counter series the app can ever increment, so
// they exist from the app's first record on. ForgetApp deletes them with the
// rest of the series; the next RecordApp re-seeds.
func seedCounters(namespace, name string) {
	for _, result := range []bakerv1alpha1.BuildResult{bakerv1alpha1.BuildResultSucceeded, bakerv1alpha1.BuildResultFailed} {
		metricBuildsTotal.WithLabelValues(namespace, name, string(result)).Add(0)
	}
	// Real containers only — the synthetic release step is operator bookkeeping
	// and can never be OOMKilled.
	for _, step := range []string{
		bakerv1alpha1.StepClone,
		bakerv1alpha1.StepSetup,
		bakerv1alpha1.StepFetch,
		bakerv1alpha1.StepBuild,
		bakerv1alpha1.StepCopier,
	} {
		metricBuildOOMTotal.WithLabelValues(namespace, name, step).Add(0)
	}
}

// volumeOutputTotal is the one status.storage.sizes key with no spec threshold:
// the whole output PVC across all retained releases.
const volumeOutputTotal = "outputTotal"

// RecordApp writes the app's gauge set from its (about to be persisted) status
// + spec-derived inputs. alertThresholds is the per-volume alertBytes map from
// alertThresholdsFrom. Idempotent — called on the early-reconcile entry and on
// every status-finalizing reconcile exit.
func (m *Recorder) RecordApp(app *bakerv1alpha1.App, deadlineSeconds int64, alertThresholds map[string]int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.state(app)

	// An empty status phase means refreshPhase has never run for this app —
	// by definition it is still awaiting its first build. Mapping it (rather
	// than exporting all-zero phases) keeps the exactly-one==1 invariant on the
	// early-reconcile record of a fresh app.
	phase := app.Status.Phase
	if phase == "" {
		phase = bakerv1alpha1.PhaseAwaitingFirstBuild
	}
	for _, p := range bakerv1alpha1.AllPhases {
		v := 0.0
		if phase == p {
			v = 1
		}
		metricPhase.WithLabelValues(app.Namespace, app.Name, string(p)).Set(v)
	}

	reason, degraded := "", 0.0
	if cond := findCondition(app, bakerv1alpha1.ConditionDegraded); cond != nil && cond.Status == metav1.ConditionTrue {
		reason, degraded = cond.Reason, 1
	}
	if reason != s.lastDegradedReason {
		metricDegraded.DeleteLabelValues(app.Namespace, app.Name, s.lastDegradedReason)
		s.lastDegradedReason = reason
	}
	metricDegraded.WithLabelValues(app.Namespace, app.Name, reason).Set(degraded)

	bp := app.Status.Build.Phase
	buildRunning := bp == bakerv1alpha1.BuildPhasePending || bp == bakerv1alpha1.BuildPhaseRunning
	runningSince := 0.0
	if buildRunning && app.Status.Build.StartTime != nil {
		runningSince = float64(app.Status.Build.StartTime.Unix())
	}
	metricBuildRunningSince.WithLabelValues(app.Namespace, app.Name).Set(runningSince)

	// Freeze the deadline for the duration of an in-flight build. The ==0 check
	// (rather than freezing only on the idle→running transition) also covers an
	// operator restart mid-build: the first record after restart re-freezes at
	// whatever deadline is then in force.
	if buildRunning {
		if s.frozenDeadline == 0 {
			s.frozenDeadline = deadlineSeconds
		}
		deadlineSeconds = s.frozenDeadline
	} else {
		s.frozenDeadline = 0
	}
	metricBuildDeadline.WithLabelValues(app.Namespace, app.Name).Set(float64(deadlineSeconds))

	// Volume series are synced state-free over the CLOSED key set (the
	// threshold map's volumes + outputTotal): present keys are Set, absent keys
	// are DeleteLabelValues'd (a no-op when no series exists), so a present
	// series is only ever Set — no scrape gap — and nothing stale survives.
	// alert_bytes is exported ONLY for volumes with a positive spec threshold,
	// so the storage alert self-guards via one-to-one vector matching
	// (outputTotal has no threshold and never gets a series).
	sizes := app.Status.Storage.Sizes
	for volume, threshold := range alertThresholds {
		syncUsedSeries(app, volume, sizes)
		if threshold > 0 {
			metricStorageAlert.WithLabelValues(app.Namespace, app.Name, volume).Set(float64(threshold))
		} else {
			metricStorageAlert.DeleteLabelValues(app.Namespace, app.Name, volume)
		}
	}
	syncUsedSeries(app, volumeOutputTotal, sizes)
}

// syncUsedSeries sets the app's used_bytes series for volume when measured,
// and deletes it when the size key is absent.
func syncUsedSeries(app *bakerv1alpha1.App, volume string, sizes map[string]int64) {
	if size, ok := sizes[volume]; ok {
		metricStorageUsed.WithLabelValues(app.Namespace, app.Name, volume).Set(float64(size))
	} else {
		metricStorageUsed.DeleteLabelValues(app.Namespace, app.Name, volume)
	}
}

// ForgetApp drops every series for a deleted app so its metrics never linger
// as stale alert fodder. Series exist iff a state entry does (RecordApp /
// RecordTerminalBuild always create state before writing any series), so an
// unknown app skips the per-vec scans entirely.
func (m *Recorder) ForgetApp(namespace, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := types.NamespacedName{Namespace: namespace, Name: name}
	defer delete(m.apps, key)
	if _, ok := m.apps[key]; !ok {
		return
	}
	labels := prometheus.Labels{"namespace": namespace, "name": name}
	for _, v := range allAppMetricVecs {
		v.DeletePartialMatch(labels)
	}
}

// RecordTerminalBuild counts a build that just reached a terminal result.
// Keyed on status.build.jobName (unique per build) so a status-write-conflict
// retry that re-observes the same Job never double-counts.
func (m *Recorder) RecordTerminalBuild(app *bakerv1alpha1.App) {
	result := app.Status.Build.Result
	if result != bakerv1alpha1.BuildResultSucceeded && result != bakerv1alpha1.BuildResultFailed {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.state(app)
	if app.Status.Build.JobName == "" || app.Status.Build.JobName == s.lastCountedJob {
		return
	}
	metricBuildsTotal.WithLabelValues(app.Namespace, app.Name, string(result)).Inc()
	if term := app.Status.Build.Termination; term != nil && term.Reason == bakerv1alpha1.TerminationReasonOOMKilled {
		metricBuildOOMTotal.WithLabelValues(app.Namespace, app.Name, term.Container).Inc()
	}
	s.lastCountedJob = app.Status.Build.JobName
}
