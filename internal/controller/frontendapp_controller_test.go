package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := bakerv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func wffc() *storagev1.StorageClass {
	mode := storagev1.VolumeBindingWaitForFirstConsumer
	return &storagev1.StorageClass{
		ObjectMeta:        metav1.ObjectMeta{Name: "local-path"},
		VolumeBindingMode: &mode,
	}
}

func immediateSC() *storagev1.StorageClass {
	mode := storagev1.VolumeBindingImmediate
	return &storagev1.StorageClass{
		ObjectMeta:        metav1.ObjectMeta{Name: "local-path"},
		VolumeBindingMode: &mode,
	}
}

func baseApp() *bakerv1alpha1.FrontendApp {
	return &bakerv1alpha1.FrontendApp{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "apps", Generation: 1},
		Spec: bakerv1alpha1.FrontendAppSpec{
			Repo:           "https://example.com/repo.git",
			PackageManager: bakerv1alpha1.PackageManagerYarn,
			Ingress:        bakerv1alpha1.IngressConfig{Host: "demo.example.com"},
		},
	}
}

func newReconciler(t *testing.T, objs ...client.Object) (*FrontendAppReconciler, client.Client) {
	t.Helper()
	s := testScheme(t)
	cl := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(&bakerv1alpha1.FrontendApp{}).
		Build()
	r := &FrontendAppReconciler{
		Client:           cl,
		Scheme:           s,
		StorageClassName: "local-path",
		TraefikNamespace: "traefik",
		Clock:            func() time.Time { return time.Unix(1000, 0) },
		Config: OperatorConfig{
			ClusterCIDRs:      []string{"10.0.0.0/8"},
			RegistryAllowlist: []string{"ghcr.io/toggle-corp/"},
			TraefikGroup:      "traefik.io",
		},
	}
	r.Config.Defaults()
	return r, cl
}

func reconcile(t *testing.T, r *FrontendAppReconciler, app *bakerv1alpha1.FrontendApp) {
	t.Helper()
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: app.Name, Namespace: app.Namespace}})
	if err != nil {
		t.Fatalf("reconcile error: %v", err)
	}
}

func getApp(t *testing.T, cl client.Client, name, ns string) *bakerv1alpha1.FrontendApp {
	t.Helper()
	out := &bakerv1alpha1.FrontendApp{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: name, Namespace: ns}, out); err != nil {
		t.Fatalf("get app: %v", err)
	}
	return out
}

// Requirement 2: first-build bootstrap seeds the rebuild annotation.
func TestReconcile_FirstBuildSeedsRebuildAnnotation(t *testing.T) {
	app := baseApp()
	r, cl := newReconciler(t, app, wffc())
	reconcile(t, r, app) // adds finalizer
	reconcile(t, r, app) // seeds annotation

	got := getApp(t, cl, "demo", "apps")
	if got.Annotations[bakerv1alpha1.RebuildAnnotation] == "" {
		t.Fatalf("expected rebuild annotation to be seeded, got none")
	}
}

// Requirement 1: a build Job is created once the rebuild token is present.
func TestReconcile_StartsBuildAndRecordsToken(t *testing.T) {
	app := baseApp()
	app.Annotations = map[string]string{bakerv1alpha1.RebuildAnnotation: "tok-1"}
	r, cl := newReconciler(t, app, wffc())
	reconcile(t, r, app) // finalizer
	reconcile(t, r, app) // start build

	jobs := &batchv1.JobList{}
	if err := cl.List(context.Background(), jobs, client.InNamespace("apps")); err != nil {
		t.Fatal(err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("expected exactly 1 build Job, got %d", len(jobs.Items))
	}
	got := getApp(t, cl, "demo", "apps")
	if got.Status.LastProcessedRebuild != "tok-1" {
		t.Fatalf("expected lastProcessedRebuild=tok-1, got %q", got.Status.LastProcessedRebuild)
	}
	// Build pod = one pod: initContainers [clone setup fetch build] + copier main.
	j := jobs.Items[0]
	if n := len(j.Spec.Template.Spec.InitContainers); n != 4 {
		t.Fatalf("expected 4 initContainers, got %d", n)
	}
	if n := len(j.Spec.Template.Spec.Containers); n != 1 || j.Spec.Template.Spec.Containers[0].Name != "copier" {
		t.Fatalf("expected single copier main container, got %+v", j.Spec.Template.Spec.Containers)
	}
}

// Requirement 1: single-active-build (DeferBuild) — a new token with an active
// Job must NOT create a second Job.
func TestReconcile_SingleActiveBuildDefers(t *testing.T) {
	app := baseApp()
	app.Annotations = map[string]string{bakerv1alpha1.RebuildAnnotation: "tok-1"}
	r, cl := newReconciler(t, app, wffc())
	reconcile(t, r, app) // finalizer
	reconcile(t, r, app) // start build for tok-1

	// New token arrives while tok-1 Job is still active (not finished).
	cur := getApp(t, cl, "demo", "apps")
	cur.Annotations[bakerv1alpha1.RebuildAnnotation] = "tok-2"
	if err := cl.Update(context.Background(), cur); err != nil {
		t.Fatal(err)
	}
	reconcile(t, r, cur)

	jobs := &batchv1.JobList{}
	_ = cl.List(context.Background(), jobs, client.InNamespace("apps"), client.MatchingLabels(buildLabelsFor(app)))
	if len(jobs.Items) != 1 {
		t.Fatalf("expected still exactly 1 build Job (deferred), got %d", len(jobs.Items))
	}
	got := getApp(t, cl, "demo", "apps")
	if got.Status.LastProcessedRebuild != "tok-1" {
		t.Fatalf("token must NOT advance while deferred, got %q", got.Status.LastProcessedRebuild)
	}
}

// Requirement 11: specStale is DETECT-ONLY (no build triggered by spec change).
func TestReconcile_SpecStaleDetectOnly(t *testing.T) {
	app := baseApp()
	// Already processed the only token, and a stale-but-different last hash.
	app.Annotations = map[string]string{bakerv1alpha1.RebuildAnnotation: "tok-1"}
	app.Status.LastProcessedRebuild = "tok-1"
	app.Status.LastBuiltSpecHash = "stale-old-hash"
	app.Status.LastSuccessfulBuildTime = ptr.To(metav1.NewTime(time.Unix(900, 0)))
	r, cl := newReconciler(t, app, wffc())
	reconcile(t, r, app) // finalizer
	reconcile(t, r, app)

	got := getApp(t, cl, "demo", "apps")
	if !got.Status.SpecStale {
		t.Fatalf("expected specStale=true (hash differs)")
	}
	jobs := &batchv1.JobList{}
	_ = cl.List(context.Background(), jobs, client.InNamespace("apps"), client.MatchingLabels(buildLabelsFor(app)))
	if len(jobs.Items) != 0 {
		t.Fatalf("stale spec must NOT trigger a build, got %d jobs", len(jobs.Items))
	}
}

// Requirement 6: InvalidStorageClass rejection (binding mode != WFFC).
func TestReconcile_InvalidStorageClassRejected(t *testing.T) {
	app := baseApp()
	r, cl := newReconciler(t, app, immediateSC())
	reconcile(t, r, app) // finalizer
	reconcile(t, r, app)

	got := getApp(t, cl, "demo", "apps")
	cond := findCondition(got, bakerv1alpha1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != bakerv1alpha1.ReasonInvalidStorageClass {
		t.Fatalf("expected Ready=False/InvalidStorageClass, got %+v", cond)
	}
}

// Requirement 10: ImageNotAllowed rejection at reconcile time.
func TestReconcile_ImageNotAllowedRejected(t *testing.T) {
	app := baseApp()
	app.Spec.Build.Image = "docker.io/evil/builder:latest"
	r, cl := newReconciler(t, app, wffc())
	reconcile(t, r, app) // finalizer
	reconcile(t, r, app)

	got := getApp(t, cl, "demo", "apps")
	cond := findCondition(got, bakerv1alpha1.ConditionReady)
	if cond == nil || cond.Reason != bakerv1alpha1.ReasonImageNotAllowed {
		t.Fatalf("expected Ready=False/ImageNotAllowed, got %+v", cond)
	}
}

// Requirement 7: nginx Deployment/Service/Ingress are NOT created before the
// first successful build.
func TestReconcile_NginxNotCreatedBeforeFirstSuccess(t *testing.T) {
	app := baseApp()
	app.Annotations = map[string]string{bakerv1alpha1.RebuildAnnotation: "tok-1"}
	r, cl := newReconciler(t, app, wffc())
	reconcile(t, r, app) // finalizer
	reconcile(t, r, app) // starts build, no success yet

	dep := &appsv1.Deployment{}
	err := cl.Get(context.Background(), types.NamespacedName{Name: nginxDeployName(app), Namespace: "apps"}, dep)
	if err == nil {
		t.Fatalf("nginx Deployment must NOT exist before first successful build")
	}
	svc := &corev1.Service{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: serviceName(app), Namespace: "apps"}, svc); err == nil {
		t.Fatalf("Service must NOT exist before first successful build")
	}
}

// Requirement 7 (positive): after a successful build the serving stack appears.
func TestReconcile_NginxCreatedAfterFirstSuccess(t *testing.T) {
	app := baseApp()
	app.Annotations = map[string]string{bakerv1alpha1.RebuildAnnotation: "tok-1"}
	app.Status.LastProcessedRebuild = "tok-1"
	app.Status.LastSuccessfulBuildTime = ptr.To(metav1.NewTime(time.Unix(900, 0)))
	app.Status.LastBuiltSpecHash = buildSpecFrom(app).Hash()
	r, cl := newReconciler(t, app, wffc())
	reconcile(t, r, app) // finalizer
	reconcile(t, r, app)

	dep := &appsv1.Deployment{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: nginxDeployName(app), Namespace: "apps"}, dep); err != nil {
		t.Fatalf("expected nginx Deployment after first success: %v", err)
	}
	if dep.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Fatalf("nginx must use Recreate strategy, got %v", dep.Spec.Strategy.Type)
	}
	ro := dep.Spec.Template.Spec.Containers[0].VolumeMounts
	foundRO := false
	for _, m := range ro {
		if m.Name == volOutput && m.ReadOnly {
			foundRO = true
		}
	}
	if !foundRO {
		t.Fatalf("nginx must mount output PVC read-only")
	}
}

// Requirement 3 + 13: clock CronJob and its scoped RBAC are created with owner refs.
func TestReconcile_ClockCronJobAndRBACCreated(t *testing.T) {
	app := baseApp()
	r, cl := newReconciler(t, app, wffc())
	reconcile(t, r, app) // finalizer
	reconcile(t, r, app)

	cron := &batchv1.CronJob{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: clockCronJobName(app), Namespace: "apps"}, cron); err != nil {
		t.Fatalf("expected clock CronJob: %v", err)
	}
	if len(cron.OwnerReferences) == 0 {
		t.Fatalf("clock CronJob must have an owner reference for cascade GC")
	}
	role := &rbacv1.Role{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: clockRoleName(app), Namespace: "apps"}, role); err != nil {
		t.Fatalf("expected clock Role: %v", err)
	}
	// Role must be scoped to THIS app only.
	if len(role.Rules) != 1 || len(role.Rules[0].ResourceNames) != 1 || role.Rules[0].ResourceNames[0] != "demo" {
		t.Fatalf("clock Role must be scoped to this app via resourceNames, got %+v", role.Rules)
	}
}

// Requirement 9: build NetworkPolicy excludes cluster CIDRs + metadata IP.
func TestReconcile_BuildNetworkPolicyExcludesClusterAndMetadata(t *testing.T) {
	app := baseApp()
	r, cl := newReconciler(t, app, wffc())
	reconcile(t, r, app)
	reconcile(t, r, app)

	np := &networkingv1.NetworkPolicy{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: buildNetPolName(app), Namespace: "apps"}, np); err != nil {
		t.Fatalf("expected build NetworkPolicy: %v", err)
	}
	var except []string
	for _, e := range np.Spec.Egress {
		for _, to := range e.To {
			if to.IPBlock != nil {
				except = to.IPBlock.Except
			}
		}
	}
	hasMeta, hasCluster := false, false
	for _, c := range except {
		if c == MetadataIP {
			hasMeta = true
		}
		if c == "10.0.0.0/8" {
			hasCluster = true
		}
	}
	if !hasMeta || !hasCluster {
		t.Fatalf("egress except must include cluster CIDR and metadata IP, got %v", except)
	}
}

// Requirement: mandatory cluster CIDRs — missing config => Ready=False.
func TestReconcile_MissingClusterCIDRsRejected(t *testing.T) {
	app := baseApp()
	r, cl := newReconciler(t, app, wffc())
	r.Config.ClusterCIDRs = nil
	reconcile(t, r, app) // finalizer
	reconcile(t, r, app)

	got := getApp(t, cl, "demo", "apps")
	cond := findCondition(got, bakerv1alpha1.ConditionReady)
	if cond == nil || cond.Reason != bakerv1alpha1.ReasonConfigError {
		t.Fatalf("expected Ready=False/ConfigError when cluster CIDRs unset, got %+v", cond)
	}
}

// Behavior 9: mapBuildPodToApp maps a build pod to its owning FrontendApp via
// the build labels; non-build pods map to no requests.
func TestMapBuildPodToApp(t *testing.T) {
	app := baseApp()
	buildPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-build-xyz", Namespace: "apps", Labels: buildLabelsFor(app)},
	}
	reqs := mapBuildPodToApp(context.Background(), buildPod)
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request for a build pod, got %d", len(reqs))
	}
	if reqs[0].Name != "demo" || reqs[0].Namespace != "apps" {
		t.Fatalf("request = %+v, want demo/apps", reqs[0].NamespacedName)
	}

	// A non-build pod (e.g. nginx, or anything lacking the build role label) maps
	// to nothing.
	nginxPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-nginx-1", Namespace: "apps", Labels: nginxLabelsFor(app)},
	}
	if reqs := mapBuildPodToApp(context.Background(), nginxPod); reqs != nil {
		t.Fatalf("non-build pod must map to nil, got %+v", reqs)
	}

	// A pod with the build role but no instance label maps to nothing (can't
	// resolve an app name).
	orphan := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "apps", Labels: map[string]string{"baker.toggle-corp.com/role": "build"}},
	}
	if reqs := mapBuildPodToApp(context.Background(), orphan); reqs != nil {
		t.Fatalf("build pod without an instance label must map to nil, got %+v", reqs)
	}

	// A non-Pod object maps to nothing.
	if reqs := mapBuildPodToApp(context.Background(), &batchv1.Job{}); reqs != nil {
		t.Fatalf("non-pod object must map to nil, got %+v", reqs)
	}
}

// Behavior 8: each clock tick sets requested-at AND clears the "by" annotation,
// so a stale manual "by" can't mislabel a later scheduled build as Manual.
func TestClockCronJob_ClearsByAnnotation(t *testing.T) {
	app := baseApp()
	r, _ := newReconciler(t, app, wffc())
	cron := r.clockCronJob(app)
	cmd := cron.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Command
	if len(cmd) != 3 {
		t.Fatalf("clock command shape changed: %v", cmd)
	}
	patch := cmd[2]
	if !strings.Contains(patch, bakerv1alpha1.RebuildAnnotation+`=`) {
		t.Fatalf("clock must set requested-at, got %q", patch)
	}
	if !strings.Contains(patch, bakerv1alpha1.RebuildByAnnotation+"-") {
		t.Fatalf("clock must CLEAR the by annotation (%s-), got %q", bakerv1alpha1.RebuildByAnnotation, patch)
	}
}

// Behavior 6: startBuild stamps the trigger (Manual when "by" is set) and seeds
// Steps = all applicable steps as Pending; PodName stays empty until observed.
func TestStartBuild_SeedsTriggerAndSteps(t *testing.T) {
	app := baseApp()
	app.Spec.Fetch.Command = []string{"sh", "-c", "fetch"}
	app.Annotations = map[string]string{
		bakerv1alpha1.RebuildAnnotation:   "tok-1",
		bakerv1alpha1.RebuildByAnnotation: "octocat",
	}
	r, _ := newReconciler(t, app, wffc())

	if err := r.startBuild(context.Background(), app, "tok-1"); err != nil {
		t.Fatalf("startBuild: %v", err)
	}
	if app.Status.Build.Trigger != bakerv1alpha1.BuildTriggerManual {
		t.Fatalf("trigger = %s, want Manual", app.Status.Build.Trigger)
	}
	if app.Status.Build.PodName != "" {
		t.Fatalf("PodName must stay empty until pod observed, got %q", app.Status.Build.PodName)
	}
	want := applicableSteps(app) // clone, fetch, build, copier, release
	if len(app.Status.Build.Steps) != len(want) {
		t.Fatalf("seeded %d steps, want %d (%v)", len(app.Status.Build.Steps), len(want), want)
	}
	for i, s := range app.Status.Build.Steps {
		if s.Name != want[i] {
			t.Fatalf("step[%d] = %s, want %s", i, s.Name, want[i])
		}
		if s.Status != bakerv1alpha1.StepStatusPending {
			t.Fatalf("seeded step %s = %s, want Pending", s.Name, s.Status)
		}
	}
}

// runningJob registers an unfinished build Job (no terminal condition).
func runningJob(t *testing.T, cl client.Client, app *bakerv1alpha1.FrontendApp, name string) *batchv1.Job {
	t.Helper()
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   app.Namespace,
			UID:         types.UID(name + "-uid"),
			Labels:      buildLabelsFor(app),
			Annotations: map[string]string{bakerv1alpha1.SpecHashAnnotation: buildSpecFrom(app).Hash()},
		},
	}
	if err := cl.Create(context.Background(), job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	return job
}

// buildPodForJob registers a build pod owned by job with the given init/main
// container statuses.
func buildPodForJob(t *testing.T, cl client.Client, app *bakerv1alpha1.FrontendApp, job *batchv1.Job, name string, init, main []corev1.ContainerStatus) {
	t.Helper()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: app.Namespace,
			Labels:    buildLabelsFor(app),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID,
				Controller: ptr.To(true),
			}},
		},
		Status: corev1.PodStatus{InitContainerStatuses: init, ContainerStatuses: main},
	}
	if err := cl.Create(context.Background(), pod); err != nil {
		t.Fatalf("create pod: %v", err)
	}
}

// Behavior 7: while the Job runs, observeBuild records PodName + per-step statuses
// from the build pod and keeps Phase=Running.
func TestObserveBuild_RunningRecordsPodAndSteps(t *testing.T) {
	app := baseApp()
	r, cl := newReconciler(t, app, wffc())
	job := runningJob(t, cl, app, "demo-build-run")
	buildPodForJob(t, cl, app, job, "demo-build-run-pod",
		[]corev1.ContainerStatus{
			{Name: "clone", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
			{Name: "build", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
		},
		[]corev1.ContainerStatus{{Name: "copier", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{}}}},
	)
	app.Status.Build = bakerv1alpha1.BuildStatus{Phase: bakerv1alpha1.BuildPhasePending, JobName: "demo-build-run"}

	if err := r.observeBuild(context.Background(), app); err != nil {
		t.Fatalf("observeBuild: %v", err)
	}
	if app.Status.Build.Phase != bakerv1alpha1.BuildPhaseRunning {
		t.Fatalf("phase = %s, want Running", app.Status.Build.Phase)
	}
	if app.Status.Build.PodName != "demo-build-run-pod" {
		t.Fatalf("PodName = %q, want demo-build-run-pod", app.Status.Build.PodName)
	}
	if got := stepStatus(app.Status.Build.Steps, bakerv1alpha1.StepClone); got != bakerv1alpha1.StepStatusSucceeded {
		t.Fatalf("clone = %s, want Succeeded", got)
	}
	if got := stepStatus(app.Status.Build.Steps, bakerv1alpha1.StepBuild); got != bakerv1alpha1.StepStatusRunning {
		t.Fatalf("build = %s, want Running", got)
	}
}

// Behavior 7 (terminal-success): on JobComplete observeBuild finalizes Steps
// (release Succeeded once the copier pod terminated 0 and the pointer flipped)
// and appends a COPY of the finalized record to BuildHistory.
func TestObserveBuild_SuccessFinalizesStepsAndHistory(t *testing.T) {
	app := baseApp()
	specHash := buildSpecFrom(app).Hash()
	r, cl := newReconciler(t, app, wffc())
	job := completeJob(t, cl, app, "demo-build-ok2", specHash)
	// Copier pod that terminated 0 with a release-pointer message so the pointer flips.
	buildPodForJob(t, cl, app, job, "demo-build-ok2-pod",
		[]corev1.ContainerStatus{
			{Name: "clone", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
			{Name: "build", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
		},
		[]corev1.ContainerStatus{{Name: "copier", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			ExitCode: 0,
			Message:  `{"release":{"current":"2026-01-01T00-00-00"}}`,
		}}}},
	)
	app.Status.Build = bakerv1alpha1.BuildStatus{Phase: bakerv1alpha1.BuildPhaseRunning, JobName: "demo-build-ok2"}

	if err := r.observeBuild(context.Background(), app); err != nil {
		t.Fatalf("observeBuild: %v", err)
	}
	if app.Status.Build.Result != bakerv1alpha1.BuildResultSucceeded {
		t.Fatalf("result = %s, want Succeeded", app.Status.Build.Result)
	}
	if got := stepStatus(app.Status.Build.Steps, bakerv1alpha1.StepCopier); got != bakerv1alpha1.StepStatusSucceeded {
		t.Fatalf("copier = %s, want Succeeded", got)
	}
	if got := stepStatus(app.Status.Build.Steps, bakerv1alpha1.StepRelease); got != bakerv1alpha1.StepStatusSucceeded {
		t.Fatalf("release = %s, want Succeeded (pointer flipped)", got)
	}
	if app.Status.Build.FailedStep != "" {
		t.Fatalf("FailedStep = %q, want empty on success", app.Status.Build.FailedStep)
	}
	if len(app.Status.BuildHistory) != 1 || app.Status.BuildHistory[0].JobName != "demo-build-ok2" {
		t.Fatalf("expected 1 history entry for demo-build-ok2, got %+v", jobNames(app.Status.BuildHistory))
	}
	if app.Status.BuildHistory[0].Result != bakerv1alpha1.BuildResultSucceeded {
		t.Fatalf("history entry must be the finalized (Succeeded) record")
	}
}

// Behavior 7 (terminal-failure): on JobFailed observeBuild marks the failed step
// (Failed), leaves release Pending, sets FailedStep, and appends to history.
func TestObserveBuild_FailureSetsFailedStepAndHistory(t *testing.T) {
	app := baseApp()
	r, cl := newReconciler(t, app, wffc())
	// Failed Job.
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: "demo-build-bad", Namespace: app.Namespace, UID: "bad-uid",
			Labels:      buildLabelsFor(app),
			Annotations: map[string]string{bakerv1alpha1.SpecHashAnnotation: buildSpecFrom(app).Hash()},
		},
		Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Message: "boom"}}},
	}
	if err := cl.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	buildPodForJob(t, cl, app, job, "demo-build-bad-pod",
		[]corev1.ContainerStatus{
			{Name: "clone", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
			{Name: "build", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 2}}},
		},
		nil, // copier never ran
	)
	app.Status.Build = bakerv1alpha1.BuildStatus{Phase: bakerv1alpha1.BuildPhaseRunning, JobName: "demo-build-bad"}

	if err := r.observeBuild(context.Background(), app); err != nil {
		t.Fatalf("observeBuild: %v", err)
	}
	if app.Status.Build.Result != bakerv1alpha1.BuildResultFailed {
		t.Fatalf("result = %s, want Failed", app.Status.Build.Result)
	}
	if got := stepStatus(app.Status.Build.Steps, bakerv1alpha1.StepBuild); got != bakerv1alpha1.StepStatusFailed {
		t.Fatalf("build = %s, want Failed", got)
	}
	if got := stepStatus(app.Status.Build.Steps, bakerv1alpha1.StepCopier); got != bakerv1alpha1.StepStatusPending {
		t.Fatalf("copier = %s, want Pending (never ran)", got)
	}
	if got := stepStatus(app.Status.Build.Steps, bakerv1alpha1.StepRelease); got != bakerv1alpha1.StepStatusPending {
		t.Fatalf("release = %s, want Pending on failure", got)
	}
	if app.Status.Build.FailedStep != bakerv1alpha1.StepBuild {
		t.Fatalf("FailedStep = %q, want build", app.Status.Build.FailedStep)
	}
	if len(app.Status.BuildHistory) != 1 || app.Status.BuildHistory[0].JobName != "demo-build-bad" {
		t.Fatalf("expected 1 history entry for demo-build-bad, got %+v", jobNames(app.Status.BuildHistory))
	}
}

// completeJob builds a finished (Complete) build Job with the given spec-hash
// annotation, registered in the fake client.
func completeJob(t *testing.T, cl client.Client, app *bakerv1alpha1.FrontendApp, name, specHash string) *batchv1.Job {
	t.Helper()
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   app.Namespace,
			Labels:      buildLabelsFor(app),
			Annotations: map[string]string{bakerv1alpha1.SpecHashAnnotation: specHash},
		},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
		},
	}
	if err := cl.Create(context.Background(), job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	return job
}

// Fix 2: build started from spec-A, spec edited to spec-B mid-flight; on success
// lastBuiltSpecHash == hash(spec-A) (the STAMPED hash), and specStale stays true
// for the now-current spec-B.
func TestObserveBuild_RecordsStampedHashNotLiveSpec(t *testing.T) {
	app := baseApp()
	app.Spec.Ref = "spec-A"
	specAHash := buildSpecFrom(app).Hash()

	r, cl := newReconciler(t, app, wffc())
	completeJob(t, cl, app, "demo-build-a", specAHash)

	// Build status points at the finished Job (not yet observed terminal).
	app.Status.Build = bakerv1alpha1.BuildStatus{Phase: bakerv1alpha1.BuildPhaseRunning, JobName: "demo-build-a"}

	// Spec edited mid-flight to spec-B.
	app.Spec.Ref = "spec-B"
	specBHash := buildSpecFrom(app).Hash()
	if specAHash == specBHash {
		t.Fatal("test setup: spec-A and spec-B must hash differently")
	}

	if err := r.observeBuild(context.Background(), app); err != nil {
		t.Fatalf("observeBuild: %v", err)
	}
	if app.Status.LastBuiltSpecHash != specAHash {
		t.Fatalf("expected lastBuiltSpecHash == hash(spec-A) %s, got %s", specAHash, app.Status.LastBuiltSpecHash)
	}
	// specStale must remain true: the live spec (spec-B) differs from what built.
	if !app.Status.SpecStale {
		t.Fatal("expected specStale=true for spec-B after a spec-A build")
	}
}

// Fix 3: a failed build then a successful one leaves Degraded=False and phase Ready.
func TestObserveBuild_SuccessClearsDegraded(t *testing.T) {
	app := baseApp()
	specHash := buildSpecFrom(app).Hash()
	r, cl := newReconciler(t, app, wffc())

	// Simulate a prior failure: Degraded=True is set.
	r.setCondition(app, bakerv1alpha1.ConditionDegraded, metav1.ConditionTrue, "BuildFailed", "boom")
	app.Status.Build = bakerv1alpha1.BuildStatus{Phase: bakerv1alpha1.BuildPhaseRunning, JobName: "demo-build-ok"}
	completeJob(t, cl, app, "demo-build-ok", specHash)

	if err := r.observeBuild(context.Background(), app); err != nil {
		t.Fatalf("observeBuild: %v", err)
	}
	deg := findCondition(app, bakerv1alpha1.ConditionDegraded)
	if deg == nil || deg.Status != metav1.ConditionFalse {
		t.Fatalf("expected Degraded=False after success, got %+v", deg)
	}
	bs := findCondition(app, bakerv1alpha1.ConditionBuildSucceeded)
	if bs == nil || bs.Status != metav1.ConditionTrue {
		t.Fatalf("expected BuildSucceeded=True, got %+v", bs)
	}
	// hasSucceededOnce is now true; phaseOf must yield Ready (not Degraded).
	r.refreshPhase(app)
	if app.Status.Phase != bakerv1alpha1.PhaseReady {
		t.Fatalf("expected phase Ready after fail->succeed, got %s", app.Status.Phase)
	}
}

// Fix 2: a finished Job already observed to a terminal result is not reprocessed.
func TestObserveBuild_TerminalShortCircuit(t *testing.T) {
	app := baseApp()
	specHash := buildSpecFrom(app).Hash()
	r, cl := newReconciler(t, app, wffc())
	completeJob(t, cl, app, "demo-build-x", specHash)

	app.Status.Build = bakerv1alpha1.BuildStatus{
		Phase:   bakerv1alpha1.BuildPhaseComplete,
		Result:  bakerv1alpha1.BuildResultSucceeded,
		JobName: "demo-build-x",
	}
	// Pre-set a sentinel completion time; the short-circuit must not overwrite it.
	sentinel := ptr.To(metav1.NewTime(time.Unix(42, 0)))
	app.Status.Build.CompletionTime = sentinel

	if err := r.observeBuild(context.Background(), app); err != nil {
		t.Fatalf("observeBuild: %v", err)
	}
	if app.Status.Build.CompletionTime == nil || !app.Status.Build.CompletionTime.Equal(sentinel) {
		t.Fatalf("terminal outcome must not be reprocessed; completionTime changed to %v", app.Status.Build.CompletionTime)
	}
}

// Fix 7: build-pod DNS egress is scoped to the cluster resolver, not :53 to all.
func TestBuildNetworkPolicy_DNSScopedToResolver(t *testing.T) {
	app := baseApp()
	r, _ := newReconciler(t, app, wffc())
	np := r.buildNetworkPolicy(app)

	var dnsRule *networkingv1.NetworkPolicyEgressRule
	for i := range np.Spec.Egress {
		e := &np.Spec.Egress[i]
		for _, p := range e.Ports {
			if p.Port != nil && p.Port.IntValue() == 53 {
				dnsRule = e
			}
		}
	}
	if dnsRule == nil {
		t.Fatal("no DNS (:53) egress rule found")
	}
	if len(dnsRule.To) == 0 {
		t.Fatal("DNS egress rule must have a To peer (scoped to resolver), not open :53 to all")
	}
	peer := dnsRule.To[0]
	if peer.NamespaceSelector == nil || peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "kube-system" {
		t.Fatalf("DNS egress must target kube-system namespace, got %+v", peer.NamespaceSelector)
	}
	if peer.PodSelector == nil || peer.PodSelector.MatchLabels["k8s-app"] != "kube-dns" {
		t.Fatalf("DNS egress must target k8s-app=kube-dns pods, got %+v", peer.PodSelector)
	}
}

// Fix: PVCs are created with an owner reference (cascade GC) via ensureExists.
func TestReconcile_PVCsCreatedWithOwnerRef(t *testing.T) {
	app := baseApp()
	r, cl := newReconciler(t, app, wffc())
	reconcile(t, r, app) // finalizer
	reconcile(t, r, app) // steady reconcile

	pvc := &corev1.PersistentVolumeClaim{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: cacheePVCName(app), Namespace: "apps"}, pvc); err != nil {
		t.Fatalf("expected cache PVC to exist: %v", err)
	}
	if len(pvc.OwnerReferences) == 0 {
		t.Fatalf("cache PVC must have an owner reference for cascade GC")
	}
}

// Fix: a pre-existing bound PVC's server-populated immutable VolumeName must be
// preserved across reconciles (blind Update would wipe it to "").
func TestReconcile_PVCVolumeNamePreserved(t *testing.T) {
	app := baseApp()
	existing := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: cacheePVCName(app), Namespace: "apps", Labels: labelsFor(app)},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: ptr.To("local-path"),
			VolumeName:       "pvc-existing-123",
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: mustQuantity("1Gi")},
			},
		},
	}
	r, cl := newReconciler(t, app, wffc(), existing)
	reconcile(t, r, app) // finalizer
	reconcile(t, r, app) // steady reconcile

	pvc := &corev1.PersistentVolumeClaim{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: cacheePVCName(app), Namespace: "apps"}, pvc); err != nil {
		t.Fatalf("get pvc: %v", err)
	}
	if pvc.Spec.VolumeName != "pvc-existing-123" {
		t.Fatalf("expected VolumeName preserved (pvc-existing-123), got %q", pvc.Spec.VolumeName)
	}
}

// Fix: a pre-existing Service's server-populated immutable ClusterIP must be
// preserved across reconciles (blind Update would wipe it to "").
func TestReconcile_ServiceClusterIPPreserved(t *testing.T) {
	app := baseApp()
	app.Annotations = map[string]string{bakerv1alpha1.RebuildAnnotation: "tok-1"}
	app.Status.LastProcessedRebuild = "tok-1"
	app.Status.LastSuccessfulBuildTime = ptr.To(metav1.NewTime(time.Unix(900, 0)))
	app.Status.LastBuiltSpecHash = buildSpecFrom(app).Hash()
	existing := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: serviceName(app), Namespace: "apps", Labels: labelsFor(app)},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.0.0.50",
			Selector:  nginxLabelsFor(app),
			Ports:     []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt32(8080)}},
		},
	}
	r, cl := newReconciler(t, app, wffc(), existing)
	reconcile(t, r, app) // finalizer
	reconcile(t, r, app) // steady reconcile (reaches ensureServing)

	svc := &corev1.Service{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: serviceName(app), Namespace: "apps"}, svc); err != nil {
		t.Fatalf("get service: %v", err)
	}
	if svc.Spec.ClusterIP != "10.0.0.50" {
		t.Fatalf("expected ClusterIP preserved (10.0.0.50), got %q", svc.Spec.ClusterIP)
	}
}

// Fix 9: the Traefik Middleware is upserted before the Ingress that references it.
func TestEnsureServing_MiddlewareCreatedWithIngress(t *testing.T) {
	app := baseApp()
	app.Spec.Auth = &bakerv1alpha1.AuthConfig{PasswordHash: ptr.To("hash")}
	app.Status.LastSuccessfulBuildTime = ptr.To(metav1.NewTime(time.Unix(900, 0)))
	app.Status.LastBuiltSpecHash = buildSpecFrom(app).Hash()
	r, cl := newReconciler(t, app, wffc())

	if err := r.ensureServing(context.Background(), app); err != nil {
		t.Fatalf("ensureServing: %v", err)
	}
	// Ingress references the middleware; both must exist after ensureServing.
	ing := &networkingv1.Ingress{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: ingressName(app), Namespace: "apps"}, ing); err != nil {
		t.Fatalf("ingress: %v", err)
	}
	mwAnno := ing.Annotations["traefik.ingress.kubernetes.io/router.middlewares"]
	if mwAnno == "" {
		t.Fatal("ingress must carry the router-middlewares annotation when auth is set")
	}
}
