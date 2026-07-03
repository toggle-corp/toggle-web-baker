package controller

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
)

// The purpose-built FrontendApp metric set the PrometheusRule alerts key on.
var (
	metricDegraded = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "frontendapp_degraded",
		Help: "1 when the app's Degraded condition is True (reason = condition reason); 0 with reason=\"\" when healthy.",
	}, []string{"namespace", "name", "reason"})
	metricPhase = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "frontendapp_phase",
		Help: "0/1 per lifecycle phase; all four phases are always exported and exactly one is 1.",
	}, []string{"namespace", "name", "phase"})
	metricBuildsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "frontendapp_builds_total",
		Help: "Terminal builds by result (Succeeded|Failed).",
	}, []string{"namespace", "name", "result"})
	metricBuildOOMTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "frontendapp_build_oom_total",
		Help: "Builds whose step container was OOMKilled.",
	}, []string{"namespace", "name", "step"})
	metricBuildRunningSince = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "frontendapp_build_running_since_seconds",
		Help: "Unix start time of the in-flight build; 0 when no build is running.",
	}, []string{"namespace", "name"})
	metricBuildDeadline = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "frontendapp_build_deadline_seconds",
		Help: "Build Job activeDeadlineSeconds (spec.pipeline.timeout or the operator default).",
	}, []string{"namespace", "name"})
	metricStorageUsed = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "frontendapp_storage_used_bytes",
		Help: "Last measured per-volume usage (status.storage.sizes).",
	}, []string{"namespace", "name", "volume"})
	metricStorageAlert = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "frontendapp_storage_alert_bytes",
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
// exported series exact: counter dedup and stale-series deletion.
type appMetricsState struct {
	lastCountedJob     string
	lastDegradedReason string
	lastUsedVolumes    map[string]bool
	lastAlertVolumes   map[string]bool
}

// Recorder writes the FrontendApp metric set. The zero value is ready to use.
type Recorder struct {
	mu   sync.Mutex
	apps map[types.NamespacedName]*appMetricsState
}

func (m *Recorder) state(app *bakerv1alpha1.FrontendApp) *appMetricsState {
	key := types.NamespacedName{Namespace: app.Namespace, Name: app.Name}
	if m.apps == nil {
		m.apps = map[types.NamespacedName]*appMetricsState{}
	}
	s, ok := m.apps[key]
	if !ok {
		s = &appMetricsState{}
		m.apps[key] = s
	}
	return s
}

// allPhases is the closed phase set exported KSM-style: every phase series is
// always written (exactly one == 1) so a phase flip never leaves a scrape gap
// that resets an alert's `for:` timer.
var allPhases = []bakerv1alpha1.Phase{
	bakerv1alpha1.PhaseAwaitingFirstBuild,
	bakerv1alpha1.PhaseBuilding,
	bakerv1alpha1.PhaseReady,
	bakerv1alpha1.PhaseDegraded,
}

// RecordApp writes the app's gauge set from its (about to be persisted) status
// + spec. Idempotent — called on every status-finalizing reconcile exit.
func (m *Recorder) RecordApp(app *bakerv1alpha1.FrontendApp, deadlineSeconds int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.state(app)

	for _, p := range allPhases {
		v := 0.0
		if app.Status.Phase == p {
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

	runningSince := 0.0
	if bp := app.Status.Build.Phase; (bp == bakerv1alpha1.BuildPhasePending || bp == bakerv1alpha1.BuildPhaseRunning) &&
		app.Status.Build.StartTime != nil {
		runningSince = float64(app.Status.Build.StartTime.Unix())
	}
	metricBuildRunningSince.WithLabelValues(app.Namespace, app.Name).Set(runningSince)
	metricBuildDeadline.WithLabelValues(app.Namespace, app.Name).Set(float64(deadlineSeconds))

	used := make(map[string]bool, len(app.Status.Storage.Sizes))
	for volume, size := range app.Status.Storage.Sizes {
		metricStorageUsed.WithLabelValues(app.Namespace, app.Name, volume).Set(float64(size))
		used[volume] = true
	}
	s.lastUsedVolumes = syncVolumeSeries(metricStorageUsed, app, s.lastUsedVolumes, used)

	// alert_bytes is exported ONLY for volumes with a positive spec threshold,
	// so the storage alert self-guards via one-to-one vector matching
	// (outputTotal has no threshold and never gets a series).
	alerts := map[string]bool{}
	st := app.Spec.Storage
	for volume, threshold := range map[string]int64{
		"cache":     st.Cache.AlertBytes,
		"dataCache": st.DataCache.AlertBytes,
		"output":    st.Output.AlertBytes,
	} {
		if threshold > 0 {
			metricStorageAlert.WithLabelValues(app.Namespace, app.Name, volume).Set(float64(threshold))
			alerts[volume] = true
		}
	}
	s.lastAlertVolumes = syncVolumeSeries(metricStorageAlert, app, s.lastAlertVolumes, alerts)
}

// syncVolumeSeries deletes the app's series for volume keys present last time
// but absent now, returning the new key set.
func syncVolumeSeries(vec *prometheus.GaugeVec, app *bakerv1alpha1.FrontendApp, last, current map[string]bool) map[string]bool {
	for volume := range last {
		if !current[volume] {
			vec.DeleteLabelValues(app.Namespace, app.Name, volume)
		}
	}
	return current
}

// ForgetApp drops every series for a deleted app so its metrics never linger
// as stale alert fodder.
func (m *Recorder) ForgetApp(namespace, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	labels := prometheus.Labels{"namespace": namespace, "name": name}
	for _, v := range allAppMetricVecs {
		v.DeletePartialMatch(labels)
	}
	delete(m.apps, types.NamespacedName{Namespace: namespace, Name: name})
}

// RecordTerminalBuild counts a build that just reached a terminal result.
// Keyed on status.build.jobName (unique per build) so a status-write-conflict
// retry that re-observes the same Job never double-counts.
func (m *Recorder) RecordTerminalBuild(app *bakerv1alpha1.FrontendApp) {
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
