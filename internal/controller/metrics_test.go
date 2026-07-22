package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
)

// resetMetrics clears every App metric vec. The vecs are process-global
// (registered once in init()); each test resets them and uses a fresh reconciler
// (fresh Recorder state), so tests never see each other's series.
func resetMetrics() {
	for _, v := range allAppMetricVecs {
		v.Reset()
	}
}

// A terminal build is counted exactly once even when a status-write conflict
// forces the same Complete Job to be re-observed (in-memory lastCountedJob
// guard); a subsequent build with a NEW JobName counts again.
func TestMetrics_TerminalBuildCountedOnce(t *testing.T) {
	resetMetrics()
	app := baseApp()
	specHash := buildSpecFrom(app).Hash()
	r, cl := newReconciler(t, app, wffc())
	completeJob(t, cl, app, "demo-build-1", specHash)
	app.Status.Build = bakerv1alpha1.BuildStatus{Phase: bakerv1alpha1.BuildPhaseRunning, JobName: "demo-build-1"}

	if err := r.observeBuild(context.Background(), app); err != nil {
		t.Fatalf("observeBuild: %v", err)
	}
	// Simulate a status-write-conflict retry: the in-memory terminal result is
	// discarded and the SAME Job is observed again from a Running status.
	app.Status.Build.Phase = bakerv1alpha1.BuildPhaseRunning
	app.Status.Build.Result = ""
	if err := r.observeBuild(context.Background(), app); err != nil {
		t.Fatalf("observeBuild retry: %v", err)
	}
	if got := testutil.ToFloat64(metricBuildsTotal.WithLabelValues("apps", "demo", "Succeeded", "Scheduled")); got != 1 {
		t.Fatalf("builds_total{Succeeded} = %v after write-conflict retry, want 1", got)
	}

	// A new build (new JobName) increments again.
	completeJob(t, cl, app, "demo-build-2", specHash)
	app.Status.Build = bakerv1alpha1.BuildStatus{Phase: bakerv1alpha1.BuildPhaseRunning, JobName: "demo-build-2"}
	if err := r.observeBuild(context.Background(), app); err != nil {
		t.Fatalf("observeBuild second build: %v", err)
	}
	if got := testutil.ToFloat64(metricBuildsTotal.WithLabelValues("apps", "demo", "Succeeded", "Scheduled")); got != 2 {
		t.Fatalf("builds_total{Succeeded} = %v after second build, want 2", got)
	}
}

// An OOMKilled build increments build_oom_total for the killed step alongside
// the Failed build count.
func TestMetrics_OOMBuildCounted(t *testing.T) {
	resetMetrics()
	app := baseApp()
	r, cl := newReconciler(t, app, wffc())
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: "demo-build-oom", Namespace: app.Namespace, UID: "oom-uid",
			Labels:      buildLabelsFor(app),
			Annotations: map[string]string{bakerv1alpha1.SpecHashAnnotation: buildSpecFrom(app).Hash()},
		},
		Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Message: "boom"}}},
	}
	if err := cl.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	buildPodForJob(t, cl, app, job, "demo-build-oom-pod",
		[]corev1.ContainerStatus{
			term("clone", 0),
			termReason("build", 137, "OOMKilled"),
		},
		nil,
	)
	app.Status.Build = bakerv1alpha1.BuildStatus{Phase: bakerv1alpha1.BuildPhaseRunning, JobName: "demo-build-oom"}

	if err := r.observeBuild(context.Background(), app); err != nil {
		t.Fatalf("observeBuild: %v", err)
	}
	if got := testutil.ToFloat64(metricBuildOOMTotal.WithLabelValues("apps", "demo", "build")); got != 1 {
		t.Fatalf("build_oom_total{step=build} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metricBuildsTotal.WithLabelValues("apps", "demo", "Failed", "Scheduled")); got != 1 {
		t.Fatalf("builds_total{Failed} = %v, want 1", got)
	}
}

// A full Reconcile that degrades through fail() (image not in the registry
// allowlist) exports degraded{reason} plus the KSM-style phase set: all four
// phases written, exactly one == 1. The deadline/running-since gauges are set
// on the same exit.
func TestMetrics_ReconcileFailPathSetsGauges(t *testing.T) {
	resetMetrics()
	app := baseApp()
	app.Spec.Pipeline.Phases.Build.Image = "docker.io/evil/builder:latest"
	r, _ := newReconciler(t, app, wffc())
	reconcile(t, r, app)

	if got := testutil.ToFloat64(metricDegraded.WithLabelValues("apps", "demo", "ImageNotAllowed")); got != 1 {
		t.Fatalf("degraded{reason=ImageNotAllowed} = %v, want 1", got)
	}
	wantPhases := map[string]float64{"AwaitingFirstBuild": 0, "Building": 0, "Ready": 0, "Degraded": 1}
	for phase, want := range wantPhases {
		if got := testutil.ToFloat64(metricPhase.WithLabelValues("apps", "demo", phase)); got != want {
			t.Fatalf("phase{%s} = %v, want %v", phase, got, want)
		}
	}
	if got := testutil.CollectAndCount(metricPhase); got != 4 {
		t.Fatalf("phase series count = %d, want exactly 4", got)
	}
	if got := testutil.ToFloat64(metricBuildDeadline.WithLabelValues("apps", "demo")); got != 1800 {
		t.Fatalf("build_deadline_seconds = %v, want 1800", got)
	}
	if got := testutil.ToFloat64(metricBuildRunningSince.WithLabelValues("apps", "demo")); got != 0 {
		t.Fatalf("build_running_since_seconds = %v, want 0 (idle)", got)
	}
}

// Degraded -> healthy: the old reason-labelled series is deleted, leaving only
// the healthy reason="" 0 series (no stale reason keeps an alert pinned firing).
func TestMetrics_DegradedReasonChurnDeletesOldSeries(t *testing.T) {
	resetMetrics()
	app := baseApp()
	r, _ := newReconciler(t, app, wffc())

	r.setCondition(app, bakerv1alpha1.ConditionDegraded, metav1.ConditionTrue, bakerv1alpha1.ReasonBuildFailed, "boom")
	app.Status.Phase = bakerv1alpha1.PhaseDegraded
	r.Metrics.RecordApp(app, 1800, alertThresholdsFrom(app), 3)

	r.setCondition(app, bakerv1alpha1.ConditionDegraded, metav1.ConditionFalse, bakerv1alpha1.ReasonReady, "build succeeded")
	app.Status.Phase = bakerv1alpha1.PhaseReady
	r.Metrics.RecordApp(app, 1800, alertThresholdsFrom(app), 3)

	expected := `
# HELP baker_app_degraded 1 when the app's Degraded condition is True (reason = condition reason); 0 with reason="" when healthy.
# TYPE baker_app_degraded gauge
baker_app_degraded{name="demo",namespace="apps",reason=""} 0
`
	if err := testutil.CollectAndCompare(metricDegraded, strings.NewReader(expected)); err != nil {
		t.Fatalf("degraded family must contain ONLY the healthy series: %v", err)
	}
}

// Storage gauges: used_bytes for every status.storage.sizes key; alert_bytes
// ONLY for volumes with spec alertBytes > 0 (never outputTotal). Vanished size
// keys and unset thresholds delete their series.
func TestMetrics_StorageSeries(t *testing.T) {
	resetMetrics()
	app := baseApp()
	app.Spec.Storage.Cache.AlertBytes = 500
	app.Status.Storage.Sizes = map[string]int64{"cache": 100, "outputTotal": 900}
	app.Status.Phase = bakerv1alpha1.PhaseReady
	r, _ := newReconciler(t, app, wffc())
	r.Metrics.RecordApp(app, 1800, alertThresholdsFrom(app), 3)

	if got := testutil.ToFloat64(metricStorageUsed.WithLabelValues("apps", "demo", "cache")); got != 100 {
		t.Fatalf("used_bytes{cache} = %v, want 100", got)
	}
	if got := testutil.ToFloat64(metricStorageUsed.WithLabelValues("apps", "demo", "outputTotal")); got != 900 {
		t.Fatalf("used_bytes{outputTotal} = %v, want 900", got)
	}
	expected := `
# HELP baker_app_storage_alert_bytes spec.storage alertBytes threshold; exported only for volumes with a threshold > 0.
# TYPE baker_app_storage_alert_bytes gauge
baker_app_storage_alert_bytes{name="demo",namespace="apps",volume="cache"} 500
`
	if err := testutil.CollectAndCompare(metricStorageAlert, strings.NewReader(expected)); err != nil {
		t.Fatalf("alert_bytes must have ONLY the cache series: %v", err)
	}

	// alertBytes unset -> the alert series is deleted; a vanished size key
	// deletes its used series too.
	app.Spec.Storage.Cache.AlertBytes = 0
	delete(app.Status.Storage.Sizes, "outputTotal")
	r.Metrics.RecordApp(app, 1800, alertThresholdsFrom(app), 3)
	if got := testutil.CollectAndCount(metricStorageAlert); got != 0 {
		t.Fatalf("alert_bytes series count = %d after unset, want 0", got)
	}
	if got := testutil.CollectAndCount(metricStorageUsed); got != 1 {
		t.Fatalf("used_bytes series count = %d after outputTotal vanished, want 1 (cache)", got)
	}
}

// totalAppSeries sums the series count across every App metric vec.
func totalAppSeries(t *testing.T) int {
	t.Helper()
	n := 0
	for _, v := range allAppMetricVecs {
		n += testutil.CollectAndCount(v)
	}
	return n
}

// Deleting the app removes every series for its namespace/name — both on the
// finalizer-removal exit and, as the backstop, on a not-found reconcile.
func TestMetrics_LifecycleForgetsDeletedApp(t *testing.T) {
	resetMetrics()
	app := baseApp()
	r, cl := newReconciler(t, app, wffc())
	reconcile(t, r, app) // finalizer
	reconcile(t, r, app) // steady: gauges populated
	if totalAppSeries(t) == 0 {
		t.Fatal("setup: expected series after a steady reconcile")
	}

	// Delete: finalizer holds the object; reconcileDelete removes it and forgets.
	if err := cl.Delete(context.Background(), getApp(t, cl, "demo", "apps")); err != nil {
		t.Fatal(err)
	}
	reconcile(t, r, app)
	if got := totalAppSeries(t); got != 0 {
		t.Fatalf("series count = %d after finalizer removal, want 0", got)
	}

	// Backstop: series that reappear for a gone app are dropped by the
	// not-found reconcile.
	r.Metrics.RecordApp(app, 1800, alertThresholdsFrom(app), 3)
	reconcile(t, r, app)
	if got := totalAppSeries(t); got != 0 {
		t.Fatalf("series count = %d after not-found reconcile, want 0", got)
	}
}

// First RecordApp for an app pre-seeds its counters at 0 — builds_total for
// both results and build_oom_total for every REAL container step (never the
// synthetic release step) — so an `increase()>0` alert can fire on the very
// first failure. ForgetApp removes them; the next RecordApp re-seeds.
func TestMetrics_CountersPreseededOnFirstRecord(t *testing.T) {
	resetMetrics()
	app := baseApp()
	var rec Recorder
	rec.RecordApp(app, 1800, alertThresholdsFrom(app), 3)

	expected := `
# HELP baker_app_builds_total Terminal builds by result (Succeeded|Failed) and trigger (Scheduled|Manual|Commit|SpecChange).
# TYPE baker_app_builds_total counter
baker_app_builds_total{name="demo",namespace="apps",result="Failed",trigger="Commit"} 0
baker_app_builds_total{name="demo",namespace="apps",result="Failed",trigger="Manual"} 0
baker_app_builds_total{name="demo",namespace="apps",result="Failed",trigger="Scheduled"} 0
baker_app_builds_total{name="demo",namespace="apps",result="Failed",trigger="SpecChange"} 0
baker_app_builds_total{name="demo",namespace="apps",result="Succeeded",trigger="Commit"} 0
baker_app_builds_total{name="demo",namespace="apps",result="Succeeded",trigger="Manual"} 0
baker_app_builds_total{name="demo",namespace="apps",result="Succeeded",trigger="Scheduled"} 0
baker_app_builds_total{name="demo",namespace="apps",result="Succeeded",trigger="SpecChange"} 0
`
	if err := testutil.CollectAndCompare(metricBuildsTotal, strings.NewReader(expected)); err != nil {
		t.Fatalf("builds_total must be pre-seeded at 0 for every result×trigger: %v", err)
	}
	for _, step := range []string{"clone", "setup", "fetch", "build", "copier"} {
		if got := testutil.ToFloat64(metricBuildOOMTotal.WithLabelValues("apps", "demo", step)); got != 0 {
			t.Fatalf("build_oom_total{step=%s} = %v, want pre-seeded 0", step, got)
		}
	}
	if got := testutil.CollectAndCount(metricBuildOOMTotal); got != 5 {
		t.Fatalf("build_oom_total series count = %d, want 5 (no synthetic release step)", got)
	}

	rec.ForgetApp("apps", "demo")
	if got := testutil.CollectAndCount(metricBuildsTotal) + testutil.CollectAndCount(metricBuildOOMTotal); got != 0 {
		t.Fatalf("counter series count = %d after ForgetApp, want 0", got)
	}
	rec.RecordApp(app, 1800, alertThresholdsFrom(app), 3)
	if got := testutil.CollectAndCount(metricBuildsTotal); got != 8 {
		t.Fatalf("builds_total series count = %d after re-record, want re-seeded 8 (2 results × 4 triggers)", got)
	}
}

// RecordApp exports the scheduled-failure threshold pair (streak + threshold)
// from status + the passed effective threshold, so the compare-two-gauges alert
// has both series.
func TestMetrics_ScheduledFailureThresholdGauges(t *testing.T) {
	resetMetrics()
	app := baseApp()
	app.Status.ConsecutiveScheduledFailures = 4
	var rec Recorder
	rec.RecordApp(app, 1800, alertThresholdsFrom(app), 3)

	if got := testutil.ToFloat64(metricConsecutiveScheduledFailures.WithLabelValues("apps", "demo")); got != 4 {
		t.Fatalf("consecutive_scheduled_failures = %v, want 4", got)
	}
	if got := testutil.ToFloat64(metricScheduledFailureThreshold.WithLabelValues("apps", "demo")); got != 3 {
		t.Fatalf("scheduled_failure_threshold = %v, want 3", got)
	}
}

// RecordTerminalBuild splits builds_total by the build's trigger, so the alert
// rules can treat a Scheduled failure (threshold-gated) differently from a
// Manual one (instant).
func TestMetrics_BuildsTotalSplitByTrigger(t *testing.T) {
	resetMetrics()
	app := baseApp()
	var rec Recorder
	rec.state(app) // seed

	// A failed MANUAL build.
	app.Status.Build = bakerv1alpha1.BuildStatus{
		JobName: "j-manual", Result: bakerv1alpha1.BuildResultFailed, Trigger: bakerv1alpha1.BuildTriggerManual,
	}
	rec.RecordTerminalBuild(app)
	// A failed SCHEDULED build.
	app.Status.Build = bakerv1alpha1.BuildStatus{
		JobName: "j-sched", Result: bakerv1alpha1.BuildResultFailed, Trigger: bakerv1alpha1.BuildTriggerScheduled,
	}
	rec.RecordTerminalBuild(app)

	if got := testutil.ToFloat64(metricBuildsTotal.WithLabelValues("apps", "demo", "Failed", "Manual")); got != 1 {
		t.Fatalf("builds_total{Failed,Manual} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metricBuildsTotal.WithLabelValues("apps", "demo", "Failed", "Scheduled")); got != 1 {
		t.Fatalf("builds_total{Failed,Scheduled} = %v, want 1", got)
	}
}

// An empty status phase (refreshPhase never ran — e.g. the early-reconcile
// record of a fresh app) is exported as AwaitingFirstBuild, preserving the
// exactly-one==1 invariant.
func TestMetrics_EmptyPhaseRecordsAwaitingFirstBuild(t *testing.T) {
	resetMetrics()
	app := baseApp() // status.phase unset
	var rec Recorder
	rec.RecordApp(app, 1800, alertThresholdsFrom(app), 3)

	wantPhases := map[string]float64{"AwaitingFirstBuild": 1, "Building": 0, "Ready": 0, "Degraded": 0}
	for phase, want := range wantPhases {
		if got := testutil.ToFloat64(metricPhase.WithLabelValues("apps", "demo", phase)); got != want {
			t.Fatalf("phase{%s} = %v, want %v", phase, got, want)
		}
	}
	if got := testutil.CollectAndCount(metricPhase); got != 4 {
		t.Fatalf("phase series count = %d, want exactly 4", got)
	}
}

// A reconcile that errors mid-way (ensureInfra PVC create rejected) still
// exports the app's series via the early RecordApp, so a persistently erroring
// app is never blind to alerting.
func TestMetrics_EarlyRecordSurvivesReconcileError(t *testing.T) {
	resetMetrics()
	app := baseApp()
	funcs := interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*corev1.PersistentVolumeClaim); ok {
				return errors.New("pvc create rejected")
			}
			return cl.Create(ctx, obj, opts...)
		},
	}
	r, _ := newReconcilerWithFuncs(t, funcs, app, wffc())

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(app)})
	if err == nil {
		t.Fatal("expected the injected ensureInfra error")
	}
	// The error struck before step 11 / fail(), so only the early record ran:
	// the as-loaded empty phase maps to AwaitingFirstBuild.
	if got := testutil.ToFloat64(metricPhase.WithLabelValues("apps", "demo", "AwaitingFirstBuild")); got != 1 {
		t.Fatalf("phase{AwaitingFirstBuild} = %v after erroring reconcile, want 1", got)
	}
	if got := testutil.CollectAndCount(metricPhase); got != 4 {
		t.Fatalf("phase series count = %d after erroring reconcile, want 4", got)
	}
	if got := testutil.ToFloat64(metricBuildDeadline.WithLabelValues("apps", "demo")); got != 1800 {
		t.Fatalf("build_deadline_seconds = %v after erroring reconcile, want 1800", got)
	}
}

// While a build is in flight the exported deadline is FROZEN at the value in
// force when the build was first recorded: a mid-build spec.pipeline.timeout
// edit must not shift the BuildStuck baseline. Once the build ends the gauge
// follows the live value again, and the next build freezes that.
func TestMetrics_DeadlineFrozenWhileBuildRunning(t *testing.T) {
	resetMetrics()
	app := baseApp()
	app.Status.Build = bakerv1alpha1.BuildStatus{
		Phase:     bakerv1alpha1.BuildPhaseRunning,
		StartTime: ptr.To(metav1.NewTime(time.Unix(1000, 0))),
	}
	var rec Recorder
	th := alertThresholdsFrom(app)

	rec.RecordApp(app, 1800, th, 3)
	if got := testutil.ToFloat64(metricBuildDeadline.WithLabelValues("apps", "demo")); got != 1800 {
		t.Fatalf("deadline = %v at build start, want 1800", got)
	}
	// Mid-build timeout edit: the gauge must NOT move.
	rec.RecordApp(app, 300, th, 3)
	if got := testutil.ToFloat64(metricBuildDeadline.WithLabelValues("apps", "demo")); got != 1800 {
		t.Fatalf("deadline = %v after mid-build edit, want frozen 1800", got)
	}
	// Build ends: the gauge follows the live value.
	app.Status.Build.Phase = bakerv1alpha1.BuildPhaseComplete
	rec.RecordApp(app, 300, th, 3)
	if got := testutil.ToFloat64(metricBuildDeadline.WithLabelValues("apps", "demo")); got != 300 {
		t.Fatalf("deadline = %v after build end, want live 300", got)
	}
	// Next build freezes the new value.
	app.Status.Build.Phase = bakerv1alpha1.BuildPhasePending
	rec.RecordApp(app, 300, th, 3)
	rec.RecordApp(app, 999, th, 3)
	if got := testutil.ToFloat64(metricBuildDeadline.WithLabelValues("apps", "demo")); got != 300 {
		t.Fatalf("deadline = %v during second build, want frozen 300", got)
	}
}

// ForgetApp for an app the Recorder never recorded is a no-op that leaves
// other apps' series intact (the state-entry guard skips the vec scans).
func TestMetrics_ForgetUnknownAppKeepsOtherSeries(t *testing.T) {
	resetMetrics()
	app := baseApp()
	var rec Recorder
	rec.RecordApp(app, 1800, alertThresholdsFrom(app), 3)
	before := 0
	for _, v := range allAppMetricVecs {
		before += testutil.CollectAndCount(v)
	}
	if before == 0 {
		t.Fatal("setup: expected series after RecordApp")
	}

	rec.ForgetApp("apps", "other")
	after := 0
	for _, v := range allAppMetricVecs {
		after += testutil.CollectAndCount(v)
	}
	if after != before {
		t.Fatalf("series count changed %d -> %d on forgetting an unknown app", before, after)
	}
}

// buildDeadlineSeconds: spec.pipeline.timeout wins; unset falls back to the
// operator-config ActiveDeadlineSeconds.
func TestBuildDeadlineSeconds(t *testing.T) {
	app := baseApp()
	r, _ := newReconciler(t, app, wffc())
	if got := r.buildDeadlineSeconds(app); got != 1800 {
		t.Fatalf("fallback deadline = %d, want 1800 (config default)", got)
	}
	app.Spec.Pipeline.Timeout = &metav1.Duration{Duration: 5 * time.Minute}
	if got := r.buildDeadlineSeconds(app); got != 300 {
		t.Fatalf("deadline = %d, want 300 (spec timeout)", got)
	}
}
