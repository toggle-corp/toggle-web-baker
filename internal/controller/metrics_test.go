package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
)

// resetMetrics clears every FrontendApp metric vec. The vecs are process-global
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
	if got := testutil.ToFloat64(metricBuildsTotal.WithLabelValues("apps", "demo", "Succeeded")); got != 1 {
		t.Fatalf("builds_total{Succeeded} = %v after write-conflict retry, want 1", got)
	}

	// A new build (new JobName) increments again.
	completeJob(t, cl, app, "demo-build-2", specHash)
	app.Status.Build = bakerv1alpha1.BuildStatus{Phase: bakerv1alpha1.BuildPhaseRunning, JobName: "demo-build-2"}
	if err := r.observeBuild(context.Background(), app); err != nil {
		t.Fatalf("observeBuild second build: %v", err)
	}
	if got := testutil.ToFloat64(metricBuildsTotal.WithLabelValues("apps", "demo", "Succeeded")); got != 2 {
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
	if got := testutil.ToFloat64(metricBuildsTotal.WithLabelValues("apps", "demo", "Failed")); got != 1 {
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
	r.metrics().RecordApp(app, 1800)

	r.setCondition(app, bakerv1alpha1.ConditionDegraded, metav1.ConditionFalse, bakerv1alpha1.ReasonReady, "build succeeded")
	app.Status.Phase = bakerv1alpha1.PhaseReady
	r.metrics().RecordApp(app, 1800)

	expected := `
# HELP frontendapp_degraded 1 when the app's Degraded condition is True (reason = condition reason); 0 with reason="" when healthy.
# TYPE frontendapp_degraded gauge
frontendapp_degraded{name="demo",namespace="apps",reason=""} 0
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
	r.metrics().RecordApp(app, 1800)

	if got := testutil.ToFloat64(metricStorageUsed.WithLabelValues("apps", "demo", "cache")); got != 100 {
		t.Fatalf("used_bytes{cache} = %v, want 100", got)
	}
	if got := testutil.ToFloat64(metricStorageUsed.WithLabelValues("apps", "demo", "outputTotal")); got != 900 {
		t.Fatalf("used_bytes{outputTotal} = %v, want 900", got)
	}
	expected := `
# HELP frontendapp_storage_alert_bytes spec.storage alertBytes threshold; exported only for volumes with a threshold > 0.
# TYPE frontendapp_storage_alert_bytes gauge
frontendapp_storage_alert_bytes{name="demo",namespace="apps",volume="cache"} 500
`
	if err := testutil.CollectAndCompare(metricStorageAlert, strings.NewReader(expected)); err != nil {
		t.Fatalf("alert_bytes must have ONLY the cache series: %v", err)
	}

	// alertBytes unset -> the alert series is deleted; a vanished size key
	// deletes its used series too.
	app.Spec.Storage.Cache.AlertBytes = 0
	delete(app.Status.Storage.Sizes, "outputTotal")
	r.metrics().RecordApp(app, 1800)
	if got := testutil.CollectAndCount(metricStorageAlert); got != 0 {
		t.Fatalf("alert_bytes series count = %d after unset, want 0", got)
	}
	if got := testutil.CollectAndCount(metricStorageUsed); got != 1 {
		t.Fatalf("used_bytes series count = %d after outputTotal vanished, want 1 (cache)", got)
	}
}

// totalAppSeries sums the series count across every FrontendApp metric vec.
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
	r.metrics().RecordApp(app, 1800)
	reconcile(t, r, app)
	if got := totalAppSeries(t); got != 0 {
		t.Fatalf("series count = %d after not-found reconcile, want 0", got)
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
