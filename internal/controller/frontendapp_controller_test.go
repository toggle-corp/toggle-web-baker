package controller

import (
	"context"
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
