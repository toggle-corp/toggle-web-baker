package controller

import (
	"context"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
)

// MeasureJob mounts the target PVC read-only at /target, runs the du image, and
// carries the measure role + volume labels (distinct from the build role so it
// is never treated as a build pod).
func TestMeasureJob_MountsTargetReadOnlyAndLabels(t *testing.T) {
	app := baseApp()
	r, _ := newReconciler(t, app, wffc())

	job := r.MeasureJob(app, measureTargets(app)[0]) // cache
	if job.Name != "demo-measure-cache" {
		t.Fatalf("job name = %q, want demo-measure-cache", job.Name)
	}
	if got := job.Labels[measureRoleLabel]; got != measureRole {
		t.Fatalf("role label = %q, want %q", got, measureRole)
	}
	if got := job.Labels[measureVolumeLabel]; got != "cache" {
		t.Fatalf("volume label = %q, want cache", got)
	}
	// Must NOT carry the build role, or buildActive / the pod Watch would treat it
	// as a build.
	if job.Labels[measureRoleLabel] == "build" {
		t.Fatalf("measure job must not use the build role")
	}
	c := job.Spec.Template.Spec.Containers[0]
	if c.Image != r.Config.Images.Du {
		t.Fatalf("container image = %q, want du image %q", c.Image, r.Config.Images.Du)
	}
	if len(c.VolumeMounts) != 1 || c.VolumeMounts[0].MountPath != "/target" || !c.VolumeMounts[0].ReadOnly {
		t.Fatalf("expected one read-only /target mount, got %+v", c.VolumeMounts)
	}
	vol := job.Spec.Template.Spec.Volumes[0]
	if vol.PersistentVolumeClaim == nil || vol.PersistentVolumeClaim.ClaimName != cacheePVCName(app) {
		t.Fatalf("volume must reference the cache PVC, got %+v", vol.VolumeSource)
	}
	if job.Spec.TTLSecondsAfterFinished == nil {
		t.Fatalf("measure job must set a TTL so it is reaped")
	}
}

// After a build, with no prior measurement (and no active build), the operator
// spawns one du Job per measured PVC (cache + dataCache), owned by the app.
func TestMaybeStartMeasurement_SpawnsWhenStale(t *testing.T) {
	app := baseApp()
	r, cl := newReconciler(t, app, wffc())

	if err := r.maybeStartMeasurement(context.Background(), app, nil); err != nil {
		t.Fatalf("maybeStartMeasurement: %v", err)
	}
	jobs := &batchv1.JobList{}
	_ = cl.List(context.Background(), jobs, client.InNamespace("apps"), client.MatchingLabels(measureLabelsFor(app)))
	if len(jobs.Items) != 2 {
		t.Fatalf("expected 2 measure jobs (cache + dataCache), got %d", len(jobs.Items))
	}
	for i := range jobs.Items {
		if ref := metav1.GetControllerOf(&jobs.Items[i]); ref == nil || ref.UID != app.UID {
			t.Fatalf("measure job %q must be owned by the app for cascade GC", jobs.Items[i].Name)
		}
	}
}

// A measurement within the debounce interval is skipped (no du Jobs spawned),
// so rapid back-to-back rebuilds don't pile up measurement Jobs.
func TestMaybeStartMeasurement_DebouncesWhenRecent(t *testing.T) {
	app := baseApp()
	r, cl := newReconciler(t, app, wffc()) // Clock = Unix(1000)
	recent := metav1.NewTime(time.Unix(1000, 0).Add(-30 * time.Minute))

	if err := r.maybeStartMeasurement(context.Background(), app, &recent); err != nil {
		t.Fatalf("maybeStartMeasurement: %v", err)
	}
	jobs := &batchv1.JobList{}
	_ = cl.List(context.Background(), jobs, client.InNamespace("apps"), client.MatchingLabels(measureLabelsFor(app)))
	if len(jobs.Items) != 0 {
		t.Fatalf("measurement within the interval must be debounced, got %d jobs", len(jobs.Items))
	}
}

// An active build blocks measurement (RWO contention: the build mounts cache RW).
func TestMaybeStartMeasurement_SkipsWhenBuildActive(t *testing.T) {
	app := baseApp()
	r, cl := newReconciler(t, app, wffc())
	runningJob(t, cl, app, "demo-build-active") // role=build, unfinished

	if err := r.maybeStartMeasurement(context.Background(), app, nil); err != nil {
		t.Fatalf("maybeStartMeasurement: %v", err)
	}
	jobs := &batchv1.JobList{}
	_ = cl.List(context.Background(), jobs, client.InNamespace("apps"), client.MatchingLabels(measureLabelsFor(app)))
	if len(jobs.Items) != 0 {
		t.Fatalf("measurement must be skipped while a build is active, got %d jobs", len(jobs.Items))
	}
}

// On build success, observeBuild spawns the cache/dataCache measurement Jobs
// (the post-build measurement edge), since cache/dataCache change only during a
// build and no build is active once it completes.
func TestObserveBuild_SuccessSpawnsMeasurement(t *testing.T) {
	app := baseApp()
	specHash := buildSpecFrom(app).Hash()
	r, cl := newReconciler(t, app, wffc())
	job := completeJob(t, cl, app, "demo-build-meas", specHash)
	buildPodForJob(t, cl, app, job, "demo-build-meas-pod",
		[]corev1.ContainerStatus{{Name: "build", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}},
		[]corev1.ContainerStatus{{Name: "copier", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Message: `{"sizes":{"output":10,"source":20}}`}}}},
	)
	app.Status.Build = bakerv1alpha1.BuildStatus{Phase: bakerv1alpha1.BuildPhaseRunning, JobName: "demo-build-meas"}

	if err := r.observeBuild(context.Background(), app); err != nil {
		t.Fatalf("observeBuild: %v", err)
	}
	jobs := &batchv1.JobList{}
	_ = cl.List(context.Background(), jobs, client.InNamespace("apps"), client.MatchingLabels(measureLabelsFor(app)))
	if len(jobs.Items) != 2 {
		t.Fatalf("build success must spawn 2 measure jobs, got %d", len(jobs.Items))
	}
}

// applyCopierTermination writes the copier's sizes payload into
// status.storage.sizes. The generic map carries whatever keys the copier emits,
// so output + outputTotal both flow through with no parser change.
func TestApplyCopierTermination_MergesOutputAndOutputTotal(t *testing.T) {
	app := baseApp()
	r, cl := newReconciler(t, app, wffc())
	job := completeJob(t, cl, app, "demo-build-sizes", "hash")
	buildPodForJob(t, cl, app, job, "demo-build-sizes-pod",
		[]corev1.ContainerStatus{{Name: "build", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}},
		[]corev1.ContainerStatus{{Name: "copier", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Message: `{"sizes":{"output":10,"outputTotal":30}}`}}}},
	)

	r.applyCopierTermination(context.Background(), app, job)

	if app.Status.Storage.Sizes["output"] != 10 {
		t.Fatalf("output = %d, want 10", app.Status.Storage.Sizes["output"])
	}
	if app.Status.Storage.Sizes["outputTotal"] != 30 {
		t.Fatalf("outputTotal = %d, want 30", app.Status.Storage.Sizes["outputTotal"])
	}
}

// applyCopierTermination prunes any stale "source" key on merge so CRs carrying
// a leftover status.storage.sizes.source (from an older copier) self-heal, while
// the copier's own keys and the du-measured cache/dataCache keys survive untouched.
func TestApplyCopierTermination_PrunesStaleSourceKey(t *testing.T) {
	app := baseApp()
	r, cl := newReconciler(t, app, wffc())
	// Seed a stale status: a leftover "source" plus a du-measured "cache".
	app.Status.Storage.Sizes = map[string]int64{"source": 2000, "cache": 500}
	job := completeJob(t, cl, app, "demo-build-prune", "hash")
	buildPodForJob(t, cl, app, job, "demo-build-prune-pod",
		[]corev1.ContainerStatus{{Name: "build", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}},
		[]corev1.ContainerStatus{{Name: "copier", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Message: `{"sizes":{"output":10,"outputTotal":30}}`}}}},
	)

	r.applyCopierTermination(context.Background(), app, job)

	if _, ok := app.Status.Storage.Sizes["source"]; ok {
		t.Fatalf("stale source key must be pruned, got %+v", app.Status.Storage.Sizes)
	}
	if app.Status.Storage.Sizes["output"] != 10 || app.Status.Storage.Sizes["outputTotal"] != 30 {
		t.Fatalf("copier keys must be present, got %+v", app.Status.Storage.Sizes)
	}
	if app.Status.Storage.Sizes["cache"] != 500 {
		t.Fatalf("du-measured cache key must be preserved, got %+v", app.Status.Storage.Sizes)
	}
}

// Reconcile recomputes status.storage.thresholdState from the merged sizes vs
// spec.storage thresholds, so the console badge means something.
func TestReconcile_ComputesThresholdState(t *testing.T) {
	app := baseApp()
	app.Spec.Storage.Cache = bakerv1alpha1.CacheThresholds{AlertBytes: 1 << 30}
	r, cl := newReconciler(t, app, wffc())
	reconcile(t, r, app) // finalizer

	// Seed a measured cache size over the alert threshold + mark succeeded-once so
	// the reconcile takes the steady-state path.
	seeded := getApp(t, cl, "demo", "apps")
	seeded.Status.LastBuiltSpecHash = "seed"
	seeded.Status.Storage.Sizes = map[string]int64{"cache": 2 << 30}
	if err := cl.Status().Update(context.Background(), seeded); err != nil {
		t.Fatalf("seed status: %v", err)
	}

	reconcile(t, r, app)

	got := getApp(t, cl, "demo", "apps")
	if got.Status.Storage.ThresholdState != "Alert" {
		t.Fatalf("thresholdState = %q, want Alert", got.Status.Storage.ThresholdState)
	}
}

// completeMeasureJob registers a finished du Job for one volume + its pod whose
// du container terminated with the given termination message.
func completeMeasureJob(t *testing.T, cl client.Client, app *bakerv1alpha1.FrontendApp, key, msg string, exit int32) *batchv1.Job {
	t.Helper()
	labels := measureLabelsFor(app)
	labels[measureVolumeLabel] = key
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: "demo-measure-" + key, Namespace: app.Namespace, UID: types.UID("m-" + key),
			Labels: labels,
		},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
		},
	}
	if err := cl.Create(context.Background(), job); err != nil {
		t.Fatalf("create measure job: %v", err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "demo-measure-" + key + "-pod", Namespace: app.Namespace, Labels: labels,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: ptr.To(true),
			}},
		},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:  measureContainerName,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: exit, Message: msg}},
		}}},
	}
	if err := cl.Create(context.Background(), pod); err != nil {
		t.Fatalf("create measure pod: %v", err)
	}
	return job
}

// observeMeasurement parses the bare integer from a finished du pod and MERGES
// it into status.storage.sizes WITHOUT clobbering the copier's output/source
// keys, then deletes the processed Job.
func TestObserveMeasurement_MergesSizesPreservingCopierKeys(t *testing.T) {
	app := baseApp()
	r, cl := newReconciler(t, app, wffc())
	// Pre-seed the copier's keys (what applyCopierTermination would have written).
	app.Status.Storage.Sizes = map[string]int64{"output": 1000, "source": 2000}
	job := completeMeasureJob(t, cl, app, "cache", "123456", 0)

	if err := r.observeMeasurement(context.Background(), app); err != nil {
		t.Fatalf("observeMeasurement: %v", err)
	}
	if app.Status.Storage.Sizes["cache"] != 123456 {
		t.Fatalf("cache size = %d, want 123456", app.Status.Storage.Sizes["cache"])
	}
	if app.Status.Storage.Sizes["output"] != 1000 || app.Status.Storage.Sizes["source"] != 2000 {
		t.Fatalf("copier keys must be preserved, got %+v", app.Status.Storage.Sizes)
	}
	if app.Status.Storage.MeasuredAt == nil {
		t.Fatalf("MeasuredAt must be refreshed")
	}
	// The processed Job is deleted (recorded once).
	got := &batchv1.Job{}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(job), got); err == nil {
		t.Fatalf("processed measure job must be deleted")
	}
}

// A second measurement records the delta from the prior size.
func TestObserveMeasurement_RecordsDelta(t *testing.T) {
	app := baseApp()
	r, cl := newReconciler(t, app, wffc())
	app.Status.Storage.Sizes = map[string]int64{"dataCache": 100}
	completeMeasureJob(t, cl, app, "dataCache", "150", 0)

	if err := r.observeMeasurement(context.Background(), app); err != nil {
		t.Fatalf("observeMeasurement: %v", err)
	}
	if app.Status.Storage.Sizes["dataCache"] != 150 {
		t.Fatalf("dataCache size = %d, want 150", app.Status.Storage.Sizes["dataCache"])
	}
	if got := app.Status.Storage.LastRunDeltas["dataCache"]; got != 50 {
		t.Fatalf("delta = %d, want 50", got)
	}
}

// A failed du Job is ignored (no size recorded) and left for its TTL.
func TestObserveMeasurement_IgnoresFailedJob(t *testing.T) {
	app := baseApp()
	r, cl := newReconciler(t, app, wffc())
	labels := measureLabelsFor(app)
	labels[measureVolumeLabel] = "cache"
	failed := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-measure-cache", Namespace: app.Namespace, UID: "mf", Labels: labels},
		Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{
			{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Message: "du: target /target not found"},
		}},
	}
	if err := cl.Create(context.Background(), failed); err != nil {
		t.Fatal(err)
	}

	if err := r.observeMeasurement(context.Background(), app); err != nil {
		t.Fatalf("observeMeasurement: %v", err)
	}
	if _, ok := app.Status.Storage.Sizes["cache"]; ok {
		t.Fatalf("failed measurement must not record a size")
	}
}
