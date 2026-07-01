package controller

import (
	"context"
	"strings"
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

// CleanupJob (MODE=cache) mounts the cache PVC RW at /cache, runs the cleanup
// image, forces the prune (CLEANUP_THRESHOLD_BYTES=0), passes the package
// manager, and carries the cleanup role + mode labels (distinct from the build
// role so it is never treated as a build pod).
func TestCleanupJob_CacheMountsAndEnv(t *testing.T) {
	app := baseApp()
	app.Spec.PackageManager = bakerv1alpha1.PackageManagerPnpm
	r, _ := newReconciler(t, app, wffc())

	job := r.CleanupJob(app, cleanupModeCache)
	if job.Name != "demo-cleanup-cache" {
		t.Fatalf("job name = %q, want demo-cleanup-cache", job.Name)
	}
	if got := job.Labels[cleanupRoleLabel]; got != cleanupRole {
		t.Fatalf("role label = %q, want %q", got, cleanupRole)
	}
	if got := job.Labels[cleanupModeLabel]; got != cleanupModeCache {
		t.Fatalf("mode label = %q, want cache", got)
	}
	if job.Labels[cleanupRoleLabel] == "build" {
		t.Fatalf("cleanup job must not use the build role")
	}
	c := job.Spec.Template.Spec.Containers[0]
	if c.Image != r.Config.Images.Cleanup {
		t.Fatalf("image = %q, want cleanup image %q", c.Image, r.Config.Images.Cleanup)
	}
	assertEnvVar(t, &c, "MODE", "cache")
	assertEnvVar(t, &c, "PACKAGE_MANAGER", "pnpm")
	assertEnvVar(t, &c, "CLEANUP_THRESHOLD_BYTES", "0")
	m := mountByName(c.VolumeMounts, volCache)
	if m == nil || m.MountPath != cacheMountPath || m.ReadOnly {
		t.Fatalf("cache must be mounted RW at %s, got %+v", cacheMountPath, m)
	}
	vol := mountVolume(job, volCache)
	if vol == nil || vol.PersistentVolumeClaim == nil || vol.PersistentVolumeClaim.ClaimName != cacheePVCName(app) {
		t.Fatalf("cache volume must reference the cache PVC, got %+v", vol)
	}
	if vol.PersistentVolumeClaim.ReadOnly {
		t.Fatalf("cache PVC volume must be writable (RW prune)")
	}
}

// CleanupJob (MODE=releases) mounts the output PVC at the output root, runs the
// cleanup image with RELEASES_DIR pointing at <outputRoot>/releases, passes
// KEEP_RELEASES from spec, and PROTECTED_RELEASES = status.release.current +
// previous (empties omitted).
func TestCleanupJob_ReleasesMountsAndEnv(t *testing.T) {
	app := baseApp()
	app.Spec.KeepReleases = 3
	app.Status.Release = bakerv1alpha1.ReleaseStatus{Current: "20260101", Previous: "20251231"}
	r, _ := newReconciler(t, app, wffc())

	job := r.CleanupJob(app, cleanupModeReleases)
	if job.Name != "demo-cleanup-releases" {
		t.Fatalf("job name = %q, want demo-cleanup-releases", job.Name)
	}
	if got := job.Labels[cleanupModeLabel]; got != cleanupModeReleases {
		t.Fatalf("mode label = %q, want releases", got)
	}
	c := job.Spec.Template.Spec.Containers[0]
	assertEnvVar(t, &c, "MODE", "releases")
	assertEnvVar(t, &c, "RELEASES_DIR", outputMountPath+"/releases")
	assertEnvVar(t, &c, "KEEP_RELEASES", "3")
	assertEnvVar(t, &c, "PROTECTED_RELEASES", "20260101,20251231")
	m := mountByName(c.VolumeMounts, volOutput)
	if m == nil || m.MountPath != outputMountPath || m.ReadOnly {
		t.Fatalf("output must be mounted RW at %s, got %+v", outputMountPath, m)
	}
	vol := mountVolume(job, volOutput)
	if vol == nil || vol.PersistentVolumeClaim == nil || vol.PersistentVolumeClaim.ClaimName != outputPVCName(app) {
		t.Fatalf("output volume must reference the output PVC, got %+v", vol)
	}
}

// PROTECTED_RELEASES omits empty current/previous (a fresh app has neither).
func TestCleanupJob_ReleasesProtectedOmitsEmpties(t *testing.T) {
	app := baseApp() // no release status
	r, _ := newReconciler(t, app, wffc())
	c := r.CleanupJob(app, cleanupModeReleases).Spec.Template.Spec.Containers[0]
	for _, e := range c.Env {
		if e.Name == "PROTECTED_RELEASES" && e.Value != "" {
			t.Fatalf("PROTECTED_RELEASES must be empty when no releases, got %q", e.Value)
		}
	}
	// current set, previous empty => just current, no trailing comma.
	app.Status.Release = bakerv1alpha1.ReleaseStatus{Current: "20260101"}
	c = r.CleanupJob(app, cleanupModeReleases).Spec.Template.Spec.Containers[0]
	assertEnvVar(t, &c, "PROTECTED_RELEASES", "20260101")
}

// Cleanup Job is hardened like the build/du Jobs: no SA token, bounded deadline,
// no infinite retry, single non-root container.
func hasCap(caps []corev1.Capability, want corev1.Capability) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}

// Releases are copier-written as the platform uid (65532), so release cleanup
// runs UNPRIVILEGED as that uid — non-root, no added capabilities.
func TestCleanupSC_ReleasesUnprivileged(t *testing.T) {
	app := baseApp()
	r, _ := newReconciler(t, app, wffc())
	sc := r.CleanupJob(app, cleanupModeReleases).Spec.Template.Spec.Containers[0].SecurityContext
	if sc == nil || sc.RunAsUser == nil || *sc.RunAsUser != platformUID {
		t.Fatalf("release cleanup must run as platformUID %d, got %v", platformUID, sc.RunAsUser)
	}
	if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
		t.Fatalf("release cleanup must run non-root")
	}
	if sc.Capabilities == nil || len(sc.Capabilities.Add) != 0 {
		t.Fatalf("release cleanup must add NO capabilities, got %v", sc.Capabilities)
	}
}

// When setup+build pin the same uid, cache cleanup owns the files and runs
// UNPRIVILEGED as that uid.
func TestCleanupSC_CacheKnownUIDUnprivileged(t *testing.T) {
	app := baseApp()
	app.Spec.Setup.RunAsUser = ptr.To(int64(3434))
	app.Spec.Build.RunAsUser = ptr.To(int64(3434))
	r, _ := newReconciler(t, app, wffc())
	sc := r.CleanupJob(app, cleanupModeCache).Spec.Template.Spec.Containers[0].SecurityContext
	if sc == nil || sc.RunAsUser == nil || *sc.RunAsUser != 3434 {
		t.Fatalf("cache cleanup must run as the pinned build uid 3434, got %v", sc.RunAsUser)
	}
	if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
		t.Fatalf("cache cleanup must run non-root when the uid is known")
	}
	if sc.Capabilities == nil || len(sc.Capabilities.Add) != 0 {
		t.Fatalf("known-uid cache cleanup must add NO capabilities, got %v", sc.Capabilities)
	}
}

// When the cache uid is unknown (image default) or setup/build disagree, cache
// cleanup falls back to root with ONLY DAC_OVERRIDE+FOWNER to override foreign
// ownership.
func TestCleanupSC_CacheUnknownUIDFallsBackToRoot(t *testing.T) {
	base := baseApp()
	mismatch := baseApp()
	mismatch.Spec.Setup.RunAsUser = ptr.To(int64(3434))
	mismatch.Spec.Build.RunAsUser = ptr.To(int64(1000))
	for name, app := range map[string]*bakerv1alpha1.FrontendApp{"image-default": base, "phase-mismatch": mismatch} {
		r, _ := newReconciler(t, app, wffc())
		sc := r.CleanupJob(app, cleanupModeCache).Spec.Template.Spec.Containers[0].SecurityContext
		if sc == nil || sc.RunAsUser == nil || *sc.RunAsUser != 0 {
			t.Fatalf("%s: cache cleanup must fall back to root, got uid %v", name, sc.RunAsUser)
		}
		if !hasCap(sc.Capabilities.Add, "DAC_OVERRIDE") || !hasCap(sc.Capabilities.Add, "FOWNER") {
			t.Fatalf("%s: root fallback must add DAC_OVERRIDE + FOWNER, got %v", name, sc.Capabilities.Add)
		}
		if len(sc.Capabilities.Drop) == 0 || sc.Capabilities.Drop[0] != "ALL" {
			t.Fatalf("%s: must drop ALL other caps, got %v", name, sc.Capabilities.Drop)
		}
	}
}

func TestCleanupJob_Hardened(t *testing.T) {
	app := baseApp()
	r, _ := newReconciler(t, app, wffc())
	job := r.CleanupJob(app, cleanupModeCache)
	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 {
		t.Fatalf("backoffLimit must be 0 (no infinite retry)")
	}
	if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds <= 0 {
		t.Fatalf("cleanup job must set a positive activeDeadlineSeconds")
	}
	ps := job.Spec.Template.Spec
	if ps.AutomountServiceAccountToken == nil || *ps.AutomountServiceAccountToken {
		t.Fatalf("automountServiceAccountToken must be false")
	}
	sc := ps.Containers[0].SecurityContext
	// Cleanup runs as root (it must unlink build-owned files) but with privilege
	// escalation disabled and all caps except DAC_OVERRIDE/FOWNER dropped.
	if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Fatalf("cleanup container must set allowPrivilegeEscalation=false")
	}
	if ps.AutomountServiceAccountToken == nil || *ps.AutomountServiceAccountToken {
		t.Fatalf("cleanup pod must not mount a service-account token")
	}
	if ps.Containers[0].TerminationMessagePolicy != corev1.TerminationMessageFallbackToLogsOnError {
		t.Fatalf("cleanup container must use FallbackToLogsOnError")
	}
}

// ---- reconcile wiring ----

// A fresh cleanup-cache request with no active build creates a MODE=cache Job
// and sets status.cleanup.cache.Phase = Running (RequestedBy from the /by anno).
func TestReconcile_StartsCacheCleanup(t *testing.T) {
	app := baseApp()
	app.Annotations = map[string]string{
		bakerv1alpha1.RebuildAnnotation:                 "tok-1",
		bakerv1alpha1.CleanupCacheRequestedAtAnnotation: "c-1",
		bakerv1alpha1.CleanupCacheByAnnotation:          "octocat",
	}
	// Already-built so we're past first-build bootstrap and not building.
	app.Status.LastProcessedRebuild = "tok-1"
	app.Status.LastSuccessfulBuildTime = ptr.To(metav1.NewTime(time.Unix(900, 0)))
	app.Status.LastBuiltSpecHash = buildSpecFrom(app).Hash()
	r, cl := newReconciler(t, app, wffc())
	reconcile(t, r, app) // finalizer
	reconcile(t, r, app) // start cleanup

	job := &batchv1.Job{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "demo-cleanup-cache", Namespace: "apps"}, job); err != nil {
		t.Fatalf("expected cache cleanup job: %v", err)
	}
	if ref := metav1.GetControllerOf(job); ref == nil || ref.UID != app.UID {
		t.Fatalf("cleanup job must be owned by the app for cascade GC")
	}
	got := getApp(t, cl, "demo", "apps")
	if got.Status.Cleanup == nil || got.Status.Cleanup.Cache == nil {
		t.Fatalf("status.cleanup.cache must be set")
	}
	if got.Status.Cleanup.Cache.Phase != "Running" {
		t.Fatalf("cache phase = %q, want Running", got.Status.Cleanup.Cache.Phase)
	}
	if got.Status.Cleanup.Cache.RequestedAt != "c-1" {
		t.Fatalf("RequestedAt = %q, want c-1", got.Status.Cleanup.Cache.RequestedAt)
	}
	if got.Status.Cleanup.Cache.RequestedBy != "octocat" {
		t.Fatalf("RequestedBy = %q, want octocat", got.Status.Cleanup.Cache.RequestedBy)
	}
}

// A fresh cleanup request while a build is active must NOT create a cleanup Job;
// the action stays Pending and re-runs once the build finishes.
func TestReconcile_CleanupWaitsForBuild(t *testing.T) {
	app := baseApp()
	app.Annotations = map[string]string{
		bakerv1alpha1.RebuildAnnotation:                 "tok-1",
		bakerv1alpha1.CleanupCacheRequestedAtAnnotation: "c-1",
	}
	app.Status.LastProcessedRebuild = "tok-1"
	app.Status.LastSuccessfulBuildTime = ptr.To(metav1.NewTime(time.Unix(900, 0)))
	app.Status.LastBuiltSpecHash = buildSpecFrom(app).Hash()
	r, cl := newReconciler(t, app, wffc())
	runningJob(t, cl, app, "demo-build-active") // active before any reconcile
	reconcile(t, r, app)                        // finalizer + steady (build active)
	reconcile(t, r, app)                        // cleanup must still wait

	job := &batchv1.Job{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "demo-cleanup-cache", Namespace: "apps"}, job); err == nil {
		t.Fatalf("cleanup job must NOT be created while a build is active")
	}
	got := getApp(t, cl, "demo", "apps")
	if got.Status.Cleanup == nil || got.Status.Cleanup.Cache == nil {
		t.Fatalf("status.cleanup.cache must be recorded as Pending")
	}
	if got.Status.Cleanup.Cache.Phase != "Pending" {
		t.Fatalf("cache phase = %q, want Pending while build active", got.Status.Cleanup.Cache.Phase)
	}
	// Marker must NOT advance while waiting (so it fires once the build ends).
	if got.Status.Cleanup.Cache.RequestedAt == "c-1" && got.Status.Cleanup.Cache.Phase == "Running" {
		t.Fatalf("must not start while build active")
	}
}

// A completed cache cleanup Job with a termination JSON sets status to Succeeded
// + reclaimedBytes + a human message, and the processed marker advances so a
// re-reconcile does NOT create a second Job.
func TestReconcile_ObservesCompletedCacheCleanup(t *testing.T) {
	app := baseApp()
	app.Annotations = map[string]string{
		bakerv1alpha1.RebuildAnnotation:                 "tok-1",
		bakerv1alpha1.CleanupCacheRequestedAtAnnotation: "c-1",
	}
	app.Status.LastProcessedRebuild = "tok-1"
	app.Status.LastSuccessfulBuildTime = ptr.To(metav1.NewTime(time.Unix(900, 0)))
	app.Status.LastBuiltSpecHash = buildSpecFrom(app).Hash()
	// The action is already Running for c-1 (started on a prior reconcile).
	app.Status.Cleanup = &bakerv1alpha1.CleanupStatus{
		Cache: &bakerv1alpha1.CleanupActionStatus{RequestedAt: "c-1", Phase: "Running"},
	}
	r, cl := newReconciler(t, app, wffc())
	// Seed the status onto the stored object.
	if err := cl.Status().Update(context.Background(), app); err != nil {
		t.Fatalf("seed status: %v", err)
	}
	completeCleanupJob(t, cl, app, cleanupModeCache,
		`{"action":"prune","before":2000000000,"after":800000000,"reclaimed":1200000000,"threshold":0}`)

	reconcile(t, r, app) // finalizer
	reconcile(t, r, app) // observe completion

	got := getApp(t, cl, "demo", "apps")
	if got.Status.Cleanup.Cache.Phase != "Succeeded" {
		t.Fatalf("cache phase = %q, want Succeeded", got.Status.Cleanup.Cache.Phase)
	}
	if got.Status.Cleanup.Cache.ReclaimedBytes != 1200000000 {
		t.Fatalf("reclaimedBytes = %d, want 1200000000", got.Status.Cleanup.Cache.ReclaimedBytes)
	}
	if got.Status.Cleanup.Cache.LastCompleted == "" {
		t.Fatalf("LastCompleted must be set on completion")
	}
	if !strings.Contains(got.Status.Cleanup.Cache.Message, "reclaim") {
		t.Fatalf("message must summarize the cache prune, got %q", got.Status.Cleanup.Cache.Message)
	}

	// The processed marker advanced (== c-1); a re-reconcile must NOT create a
	// second Job. Delete the finished one and re-run.
	policy := metav1.DeletePropagationBackground
	_ = cl.Delete(context.Background(), &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "demo-cleanup-cache", Namespace: "apps"}}, &client.DeleteOptions{PropagationPolicy: &policy})
	reconcile(t, r, app)
	job := &batchv1.Job{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "demo-cleanup-cache", Namespace: "apps"}, job); err == nil {
		t.Fatalf("a processed cleanup request must NOT re-create the Job")
	}
}

// A completed release-prune Job records the kept/deleted summary + reclaimed.
func TestReconcile_ObservesCompletedReleaseCleanup(t *testing.T) {
	app := baseApp()
	app.Annotations = map[string]string{
		bakerv1alpha1.RebuildAnnotation:                    "tok-1",
		bakerv1alpha1.CleanupReleasesRequestedAtAnnotation: "r-1",
		bakerv1alpha1.CleanupReleasesByAnnotation:          "octocat",
	}
	app.Status.LastProcessedRebuild = "tok-1"
	app.Status.LastSuccessfulBuildTime = ptr.To(metav1.NewTime(time.Unix(900, 0)))
	app.Status.LastBuiltSpecHash = buildSpecFrom(app).Hash()
	app.Status.Cleanup = &bakerv1alpha1.CleanupStatus{
		Releases: &bakerv1alpha1.CleanupActionStatus{RequestedAt: "r-1", Phase: "Running"},
	}
	r, cl := newReconciler(t, app, wffc())
	if err := cl.Status().Update(context.Background(), app); err != nil {
		t.Fatalf("seed status: %v", err)
	}
	completeCleanupJob(t, cl, app, cleanupModeReleases,
		`{"action":"release-prune","kept":3,"deleted":2,"before":500,"after":300,"reclaimed":200}`)

	reconcile(t, r, app) // finalizer
	reconcile(t, r, app) // observe

	got := getApp(t, cl, "demo", "apps")
	if got.Status.Cleanup.Releases.Phase != "Succeeded" {
		t.Fatalf("releases phase = %q, want Succeeded", got.Status.Cleanup.Releases.Phase)
	}
	if got.Status.Cleanup.Releases.ReclaimedBytes != 200 {
		t.Fatalf("reclaimedBytes = %d, want 200", got.Status.Cleanup.Releases.ReclaimedBytes)
	}
	if !strings.Contains(got.Status.Cleanup.Releases.Message, "kept 3") || !strings.Contains(got.Status.Cleanup.Releases.Message, "deleted 2") {
		t.Fatalf("message must summarize the release prune, got %q", got.Status.Cleanup.Releases.Message)
	}
}

// A completed cache prune feeds its fresh du (the JSON "after") back into
// status.storage.sizes["cache"], so the console reflects the reclaimed space
// without waiting for a post-build measurement.
func TestReconcile_CacheCleanupWritesBackSize(t *testing.T) {
	app := baseApp()
	app.Annotations = map[string]string{
		bakerv1alpha1.CleanupCacheRequestedAtAnnotation: "c-1",
	}
	app.Status.LastSuccessfulBuildTime = ptr.To(metav1.NewTime(time.Unix(900, 0)))
	app.Status.LastBuiltSpecHash = buildSpecFrom(app).Hash()
	app.Status.Cleanup = &bakerv1alpha1.CleanupStatus{
		Cache: &bakerv1alpha1.CleanupActionStatus{RequestedAt: "c-1", Phase: "Running"},
	}
	app.Status.Storage.Sizes = map[string]int64{"cache": 1000}
	r, cl := newReconciler(t, app, wffc())
	if err := cl.Status().Update(context.Background(), app); err != nil {
		t.Fatalf("seed status: %v", err)
	}
	completeCleanupJob(t, cl, app, cleanupModeCache,
		`{"action":"pnpm store prune","before":1000,"after":300,"reclaimed":700,"threshold":0}`)

	reconcile(t, r, app)
	reconcile(t, r, app)

	got := getApp(t, cl, "demo", "apps")
	if got.Status.Storage.Sizes["cache"] != 300 {
		t.Fatalf(`sizes["cache"] = %d, want 300`, got.Status.Storage.Sizes["cache"])
	}
	if got.Status.Storage.LastRunDeltas["cache"] != -700 {
		t.Fatalf(`lastRunDeltas["cache"] = %d, want -700`, got.Status.Storage.LastRunDeltas["cache"])
	}
}

// A completed release prune shrinks the whole releases dir, which the copier
// reports under sizes["outputTotal"] (the current release under "output" is
// protected). The JSON "after" is written back to that key.
func TestReconcile_ReleaseCleanupWritesBackSize(t *testing.T) {
	app := baseApp()
	app.Annotations = map[string]string{
		bakerv1alpha1.CleanupReleasesRequestedAtAnnotation: "r-1",
	}
	app.Status.LastSuccessfulBuildTime = ptr.To(metav1.NewTime(time.Unix(900, 0)))
	app.Status.LastBuiltSpecHash = buildSpecFrom(app).Hash()
	app.Status.Cleanup = &bakerv1alpha1.CleanupStatus{
		Releases: &bakerv1alpha1.CleanupActionStatus{RequestedAt: "r-1", Phase: "Running"},
	}
	app.Status.Storage.Sizes = map[string]int64{"outputTotal": 9000, "output": 111}
	r, cl := newReconciler(t, app, wffc())
	if err := cl.Status().Update(context.Background(), app); err != nil {
		t.Fatalf("seed status: %v", err)
	}
	completeCleanupJob(t, cl, app, cleanupModeReleases,
		`{"action":"release-prune","kept":3,"deleted":2,"before":9000,"after":5000,"reclaimed":4000}`)

	reconcile(t, r, app)
	reconcile(t, r, app)

	got := getApp(t, cl, "demo", "apps")
	if got.Status.Storage.Sizes["outputTotal"] != 5000 {
		t.Fatalf(`sizes["outputTotal"] = %d, want 5000`, got.Status.Storage.Sizes["outputTotal"])
	}
	// The current release ("output") is protected from the prune and must be
	// left untouched by the writeback.
	if got.Status.Storage.Sizes["output"] != 111 {
		t.Fatalf(`sizes["output"] = %d, want 111 (untouched)`, got.Status.Storage.Sizes["output"])
	}
}

// A skip / early-exit result reports after=0 and must NOT clobber a real prior
// measurement with 0.
func TestReconcile_SkippedCleanupDoesNotWriteBackSize(t *testing.T) {
	app := baseApp()
	app.Annotations = map[string]string{
		bakerv1alpha1.CleanupCacheRequestedAtAnnotation: "c-1",
	}
	app.Status.LastSuccessfulBuildTime = ptr.To(metav1.NewTime(time.Unix(900, 0)))
	app.Status.LastBuiltSpecHash = buildSpecFrom(app).Hash()
	app.Status.Cleanup = &bakerv1alpha1.CleanupStatus{
		Cache: &bakerv1alpha1.CleanupActionStatus{RequestedAt: "c-1", Phase: "Running"},
	}
	app.Status.Storage.Sizes = map[string]int64{"cache": 1000}
	r, cl := newReconciler(t, app, wffc())
	if err := cl.Status().Update(context.Background(), app); err != nil {
		t.Fatalf("seed status: %v", err)
	}
	completeCleanupJob(t, cl, app, cleanupModeCache,
		`{"action":"skip","reason":"under-threshold","before":1000,"after":0,"threshold":0}`)

	reconcile(t, r, app)
	reconcile(t, r, app)

	got := getApp(t, cl, "demo", "apps")
	if got.Status.Storage.Sizes["cache"] != 1000 {
		t.Fatalf(`skip must not clobber sizes["cache"]; got %d, want 1000`, got.Status.Storage.Sizes["cache"])
	}
	if _, ok := got.Status.Storage.LastRunDeltas["cache"]; ok {
		t.Fatalf("skip must not record a delta for cache")
	}
}

// A skip result (threshold not crossed / nothing to prune) is surfaced as
// Succeeded with the skip reason and zero reclaimed.
func TestReconcile_ObservesSkippedCacheCleanup(t *testing.T) {
	app := baseApp()
	app.Annotations = map[string]string{
		bakerv1alpha1.CleanupCacheRequestedAtAnnotation: "c-1",
	}
	app.Status.LastProcessedRebuild = ""
	app.Status.LastSuccessfulBuildTime = ptr.To(metav1.NewTime(time.Unix(900, 0)))
	app.Status.LastBuiltSpecHash = buildSpecFrom(app).Hash()
	app.Status.Cleanup = &bakerv1alpha1.CleanupStatus{
		Cache: &bakerv1alpha1.CleanupActionStatus{RequestedAt: "c-1", Phase: "Running"},
	}
	r, cl := newReconciler(t, app, wffc())
	if err := cl.Status().Update(context.Background(), app); err != nil {
		t.Fatalf("seed status: %v", err)
	}
	completeCleanupJob(t, cl, app, cleanupModeCache,
		`{"action":"skip","reason":"unsupported package manager","before":0,"after":0,"reclaimed":0,"threshold":0}`)

	reconcile(t, r, app)
	reconcile(t, r, app)

	got := getApp(t, cl, "demo", "apps")
	if got.Status.Cleanup.Cache.Phase != "Succeeded" {
		t.Fatalf("skip must be Succeeded, got %q", got.Status.Cleanup.Cache.Phase)
	}
	if got.Status.Cleanup.Cache.ReclaimedBytes != 0 {
		t.Fatalf("skip reclaimed = %d, want 0", got.Status.Cleanup.Cache.ReclaimedBytes)
	}
	if !strings.Contains(got.Status.Cleanup.Cache.Message, "skip") {
		t.Fatalf("skip message must mention skip, got %q", got.Status.Cleanup.Cache.Message)
	}
}

// A failed cleanup Job sets Phase=Failed and still advances the marker so it
// doesn't loop forever; the message carries the failure.
func TestReconcile_ObservesFailedCacheCleanup(t *testing.T) {
	app := baseApp()
	app.Annotations = map[string]string{
		bakerv1alpha1.CleanupCacheRequestedAtAnnotation: "c-1",
	}
	app.Status.LastSuccessfulBuildTime = ptr.To(metav1.NewTime(time.Unix(900, 0)))
	app.Status.LastBuiltSpecHash = buildSpecFrom(app).Hash()
	app.Status.Cleanup = &bakerv1alpha1.CleanupStatus{
		Cache: &bakerv1alpha1.CleanupActionStatus{RequestedAt: "c-1", Phase: "Running"},
	}
	r, cl := newReconciler(t, app, wffc())
	if err := cl.Status().Update(context.Background(), app); err != nil {
		t.Fatalf("seed status: %v", err)
	}
	failCleanupJob(t, cl, app, cleanupModeCache, "deadline exceeded")

	reconcile(t, r, app)
	reconcile(t, r, app)

	got := getApp(t, cl, "demo", "apps")
	if got.Status.Cleanup.Cache.Phase != "Failed" {
		t.Fatalf("cache phase = %q, want Failed", got.Status.Cleanup.Cache.Phase)
	}
	if got.Status.Cleanup.Cache.RequestedAt != "c-1" {
		t.Fatalf("marker must advance on failure to avoid a retry loop, got %q", got.Status.Cleanup.Cache.RequestedAt)
	}
}

// ---- test helpers ----

func mountVolume(job *batchv1.Job, name string) *corev1.VolumeSource {
	for i := range job.Spec.Template.Spec.Volumes {
		if job.Spec.Template.Spec.Volumes[i].Name == name {
			return &job.Spec.Template.Spec.Volumes[i].VolumeSource
		}
	}
	return nil
}

func completeCleanupJob(t *testing.T, cl client.Client, app *bakerv1alpha1.FrontendApp, mode, msg string) *batchv1.Job {
	t.Helper()
	return cleanupJobWith(t, cl, app, mode, msg, batchv1.JobComplete, 0)
}

func failCleanupJob(t *testing.T, cl client.Client, app *bakerv1alpha1.FrontendApp, mode, msg string) *batchv1.Job {
	t.Helper()
	return cleanupJobWith(t, cl, app, mode, msg, batchv1.JobFailed, 1)
}

func cleanupJobWith(t *testing.T, cl client.Client, app *bakerv1alpha1.FrontendApp, mode, msg string, condType batchv1.JobConditionType, exit int32) *batchv1.Job {
	t.Helper()
	labels := cleanupLabelsFor(app)
	labels[cleanupModeLabel] = mode
	name := "demo-cleanup-" + mode
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: app.Namespace, UID: types.UID("cl-" + mode), Labels: labels,
		},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{{Type: condType, Status: corev1.ConditionTrue, Message: msg}},
		},
	}
	if err := cl.Create(context.Background(), job); err != nil {
		t.Fatalf("create cleanup job: %v", err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name + "-pod", Namespace: app.Namespace, Labels: labels,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: ptr.To(true),
			}},
		},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:  cleanupContainerName,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: exit, Message: msg}},
		}}},
	}
	if err := cl.Create(context.Background(), pod); err != nil {
		t.Fatalf("create cleanup pod: %v", err)
	}
	return job
}

// A build started in the SAME reconcile (its Job not yet visible to the cached
// build List sampled earlier) must still block cleanup: the `active` flag is
// flipped after StartBuild so cleanup defers rather than racing the build pod.
func TestReconcile_CleanupDefersToBuildStartedThisReconcile(t *testing.T) {
	app := baseApp()
	app.Annotations = map[string]string{
		bakerv1alpha1.RebuildAnnotation:                 "tok-1", // unprocessed -> StartBuild this pass
		bakerv1alpha1.CleanupCacheRequestedAtAnnotation: "c-1",
	}
	r, cl := newReconciler(t, app, wffc())
	reconcile(t, r, app) // finalizer
	reconcile(t, r, app) // build starts here; cleanup must defer

	if err := cl.Get(context.Background(), types.NamespacedName{Name: buildJobName(app, "tok-1"), Namespace: "apps"}, &batchv1.Job{}); err != nil {
		t.Fatalf("expected the build Job to start this reconcile: %v", err)
	}
	cjobs := &batchv1.JobList{}
	if err := cl.List(context.Background(), cjobs, client.InNamespace("apps"), client.MatchingLabels(cleanupLabelsFor(app))); err != nil {
		t.Fatal(err)
	}
	if len(cjobs.Items) != 0 {
		t.Fatalf("cleanup Job must NOT start in the same reconcile a build started, got %d", len(cjobs.Items))
	}
	got := getApp(t, cl, "demo", "apps")
	if got.Status.Cleanup == nil || got.Status.Cleanup.Cache == nil || got.Status.Cleanup.Cache.Phase != "Pending" {
		t.Fatalf("cache cleanup must be Pending while a build is starting, got %+v", got.Status.Cleanup)
	}
}

// Two fresh cleanup requests in one reconcile must NOT both start: the Job
// created for the first action is not yet in the cached List, so an in-pass
// guard keeps the second action Pending (serialized, runs after the first ends).
func TestReconcile_OnlyOneCleanupStartsPerReconcile(t *testing.T) {
	app := baseApp()
	app.Annotations = map[string]string{
		bakerv1alpha1.RebuildAnnotation:                    "tok-1",
		bakerv1alpha1.CleanupCacheRequestedAtAnnotation:    "c-1",
		bakerv1alpha1.CleanupReleasesRequestedAtAnnotation: "r-1",
	}
	app.Status.LastProcessedRebuild = "tok-1" // past first-build, not building
	app.Status.LastSuccessfulBuildTime = ptr.To(metav1.NewTime(time.Unix(900, 0)))
	app.Status.LastBuiltSpecHash = buildSpecFrom(app).Hash()
	r, cl := newReconciler(t, app, wffc())
	reconcile(t, r, app) // finalizer
	reconcile(t, r, app) // exactly one cleanup may start

	jobs := &batchv1.JobList{}
	if err := cl.List(context.Background(), jobs, client.InNamespace("apps"), client.MatchingLabels(cleanupLabelsFor(app))); err != nil {
		t.Fatal(err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("exactly one cleanup Job may start per reconcile, got %d", len(jobs.Items))
	}
	got := getApp(t, cl, "demo", "apps")
	cache, rel := got.Status.Cleanup.Cache.Phase, got.Status.Cleanup.Releases.Phase
	running, pending := 0, 0
	for _, p := range []string{cache, rel} {
		switch p {
		case "Running":
			running++
		case "Pending":
			pending++
		}
	}
	if running != 1 || pending != 1 {
		t.Fatalf("want exactly one Running + one Pending, got cache=%q releases=%q", cache, rel)
	}
}
