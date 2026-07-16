package controller

import (
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
	"github.com/toggle-corp/toggle-web-baker/internal/domain"
)

func reconcilerForPod() *AppReconciler {
	r := &AppReconciler{}
	r.Config.Defaults()
	// Operator resource defaults are normally validated+parsed by LoadConfig;
	// tests populate sensible defaults directly (cpu 0.1/4, memory setup/fetch
	// 512Mi, build 2Gi, deadline 1800).
	r.Config.PhaseResourceDefaults = PhaseResourceDefaults{
		CPURequest:  resource.MustParse("100m"),
		CPULimit:    resource.MustParse("4"),
		MemorySetup: resource.MustParse("512Mi"),
		MemoryFetch: resource.MustParse("512Mi"),
		MemoryBuild: resource.MustParse("2Gi"),
	}
	r.Config.ActiveDeadlineSeconds = 1800
	return r
}

func containerByName(cs []corev1.Container, name string) *corev1.Container {
	for i := range cs {
		if cs[i].Name == name {
			return &cs[i]
		}
	}
	return nil
}

func mountByName(ms []corev1.VolumeMount, name string) *corev1.VolumeMount {
	for i := range ms {
		if ms[i].Name == name {
			return &ms[i]
		}
	}
	return nil
}

// Requirement 4: build container NEVER mounts the output PVC.
func TestBuildJob_BuildNeverMountsOutput(t *testing.T) {
	app := baseApp()
	r := reconcilerForPod()
	job := r.BuildJob(app, "tok", gitCredentialDecision{})
	build := containerByName(job.Spec.Template.Spec.InitContainers, "build")
	if build == nil {
		t.Fatal("no build container")
	}
	if mountByName(build.VolumeMounts, volOutput) != nil {
		t.Fatalf("build container must NOT mount the output PVC")
	}
	copier := containerByName(job.Spec.Template.Spec.Containers, "copier")
	if mountByName(copier.VolumeMounts, volOutput) == nil {
		t.Fatalf("copier must mount the output PVC")
	}
}

// Requirement 4: secrets injected ONLY into the fetch container.
func TestBuildJob_SecretsOnlyInFetch(t *testing.T) {
	app := baseApp()
	app.Spec.Pipeline.Phases.Fetch.Secrets = []bakerv1alpha1.EnvVarWithSecret{{
		Name: "TOKEN",
		ValueFrom: bakerv1alpha1.EnvVarWithSecretSource{
			SecretKeyRef: bakerv1alpha1.SecretKeySelector{Name: "s", Key: "k"},
		},
	}}
	r := reconcilerForPod()
	job := r.BuildJob(app, "tok", gitCredentialDecision{})

	for _, c := range job.Spec.Template.Spec.InitContainers {
		for _, e := range c.Env {
			if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil && c.Name != "fetch" {
				t.Fatalf("secret env leaked into %q container", c.Name)
			}
		}
	}
	fetch := containerByName(job.Spec.Template.Spec.InitContainers, "fetch")
	found := false
	for _, e := range fetch.Env {
		if e.Name == "TOKEN" && e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			found = true
		}
	}
	if !found {
		t.Fatalf("fetch container must receive the secret env")
	}
}

// Requirement 4: yarn vs pnpm volume branching.
func TestBuildJob_YarnVsPnpmVolumeLayout(t *testing.T) {
	r := reconcilerForPod()

	yarn := baseApp()
	yarn.Spec.Pipeline.PackageManager = bakerv1alpha1.PackageManagerYarn
	yjob := r.BuildJob(yarn, "t", gitCredentialDecision{})
	ybuild := containerByName(yjob.Spec.Template.Spec.InitContainers, "build")
	if !hasEnv(ybuild.Env, "YARN_CACHE_FOLDER") {
		t.Fatalf("yarn build must set YARN_CACHE_FOLDER")
	}

	pnpm := baseApp()
	pnpm.Spec.Pipeline.PackageManager = bakerv1alpha1.PackageManagerPnpm
	pjob := r.BuildJob(pnpm, "t", gitCredentialDecision{})
	pbuild := containerByName(pjob.Spec.Template.Spec.InitContainers, "build")
	if !hasEnv(pbuild.Env, "npm_config_store_dir") {
		t.Fatalf("pnpm build must set npm_config_store_dir (content-addressable store on the cache PVC)")
	}
	// node_modules must stay cwd-local (/work), NOT be relocated onto the cache PVC:
	// pnpm exec/run resolve bins from <cwd>/node_modules/.bin, so npm_config_modules_dir
	// would break `pnpm exec` in any phase (regression: graphql-codegen "not found").
	if hasEnv(pbuild.Env, "npm_config_modules_dir") {
		t.Fatalf("pnpm build must NOT set npm_config_modules_dir (breaks cwd-relative pnpm exec/run bin resolution)")
	}
	// pnpm must NOT use the bogus NODE_MODULES_DIR key, and yarn must not set pnpm env.
	if hasEnv(pbuild.Env, "NODE_MODULES_DIR") {
		t.Fatalf("pnpm build must NOT set the bogus NODE_MODULES_DIR key")
	}
	if hasEnv(ybuild.Env, "npm_config_store_dir") || hasEnv(ybuild.Env, "npm_config_modules_dir") {
		t.Fatalf("yarn build must NOT set pnpm store/modules env")
	}
	if hasEnv(pbuild.Env, "YARN_CACHE_FOLDER") {
		t.Fatalf("pnpm build must NOT set YARN_CACHE_FOLDER")
	}
	// Both PMs mount cache RW into build (pnpm: store+node_modules; yarn: cache).
	if mountByName(pbuild.VolumeMounts, volCache) == nil {
		t.Fatalf("pnpm build must mount the cache PVC")
	}
	pcacheMount := mountByName(pbuild.VolumeMounts, volCache)
	if pcacheMount.MountPath != cacheMountPath {
		t.Fatalf("pnpm cache mount must be at %s, got %s", cacheMountPath, pcacheMount.MountPath)
	}
	// pnpm store_dir + modules_dir both resolve onto the cache mount path.
	for _, e := range pbuild.Env {
		if (e.Name == "npm_config_store_dir" || e.Name == "npm_config_modules_dir") &&
			!strings.HasPrefix(e.Value, cacheMountPath+"/") {
			t.Fatalf("pnpm %s must live under the cache mount %s, got %s", e.Name, cacheMountPath, e.Value)
		}
	}
}

// Regression: any phase (fetch especially) must be able to run tools installed
// by setup. node_modules lives on the shared /work emptyDir for BOTH managers,
// so setup's install is visible to fetch/build without relocating node_modules
// onto the cache PVC. Relocating it (npm_config_modules_dir) broke pnpm's
// cwd-relative exec/run bin lookup — a fetch `pnpm exec graphql-codegen` failed
// "command not found". Guards: no phase sets modules_dir, and fetch mounts /work
// (deps) + /data, but needs no cache mount at exec time.
func TestBuildJob_FetchCanRunInstalledDeps(t *testing.T) {
	r := reconcilerForPod()

	pnpm := baseApp()
	pnpm.Spec.Pipeline.PackageManager = bakerv1alpha1.PackageManagerPnpm
	pjob := r.BuildJob(pnpm, "t", gitCredentialDecision{})
	for _, name := range []string{"setup", "fetch", "build"} {
		c := containerByName(pjob.Spec.Template.Spec.InitContainers, name)
		if c == nil {
			continue // setup is conditional; only assert on phases that exist
		}
		if hasEnv(c.Env, "npm_config_modules_dir") {
			t.Fatalf("pnpm %s must NOT relocate node_modules (breaks cwd-relative pnpm exec/run)", name)
		}
	}
	pfetch := containerByName(pjob.Spec.Template.Spec.InitContainers, "fetch")
	if mountByName(pfetch.VolumeMounts, volWork) == nil {
		t.Fatalf("fetch must mount /work (holds node_modules/.bin from setup)")
	}
	if mountByName(pfetch.VolumeMounts, volData) == nil {
		t.Fatalf("fetch must mount the data PVC")
	}
}

// Requirement 4: hardened securityContext + single-pod invariants.
func TestBuildJob_HardenedSecurity(t *testing.T) {
	app := baseApp()
	r := reconcilerForPod()
	job := r.BuildJob(app, "tok", gitCredentialDecision{})
	if *job.Spec.BackoffLimit != 0 {
		t.Fatalf("backoffLimit must be 0")
	}
	if job.Spec.Template.Spec.AutomountServiceAccountToken == nil || *job.Spec.Template.Spec.AutomountServiceAccountToken {
		t.Fatalf("automountServiceAccountToken must be false")
	}
	for _, c := range append(job.Spec.Template.Spec.InitContainers, job.Spec.Template.Spec.Containers...) {
		sc := c.SecurityContext
		if sc == nil || sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
			t.Fatalf("%s must runAsNonRoot", c.Name)
		}
		if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
			t.Fatalf("%s must disable privilege escalation", c.Name)
		}
		if sc.Capabilities == nil || len(sc.Capabilities.Drop) == 0 || sc.Capabilities.Drop[0] != "ALL" {
			t.Fatalf("%s must drop ALL caps", c.Name)
		}
	}
}

// spec.pipeline.phases.build.runAsUser pins the build container's numeric UID (needed for images
// whose USER is a non-numeric name, e.g. cimg/node's `circleci`). Phases without
// runAsUser keep the default hardened context (RunAsUser nil).
// BYO path: a phase with its own image pins its own runAsUser; an image-less
// phase (fetch here, with no nodeVersion) must NOT inherit it.
func TestBuildJob_RunAsUserPinnedPerPhase(t *testing.T) {
	app := baseApp()
	app.Spec.Pipeline.Phases.Build.Image = "docker.io/cimg/node:18.20"
	app.Spec.Pipeline.Phases.Build.RunAsUser = ptr.To(int64(3434))
	r := reconcilerForPod()
	job := r.BuildJob(app, "tok", gitCredentialDecision{})

	build := containerByName(job.Spec.Template.Spec.InitContainers, "build")
	if build.SecurityContext.RunAsUser == nil || *build.SecurityContext.RunAsUser != 3434 {
		t.Fatalf("build runAsUser = %v, want 3434", build.SecurityContext.RunAsUser)
	}
	// runAsNonRoot must still be set alongside the pinned UID.
	if build.SecurityContext.RunAsNonRoot == nil || !*build.SecurityContext.RunAsNonRoot {
		t.Fatalf("build must still runAsNonRoot")
	}
	// An unset phase (fetch) must NOT inherit the build UID.
	fetch := containerByName(job.Spec.Template.Spec.InitContainers, "fetch")
	if fetch.SecurityContext.RunAsUser != nil {
		t.Fatalf("fetch runAsUser = %v, want nil", *fetch.SecurityContext.RunAsUser)
	}
}

// clone runs as the BUILD phase's resolved UID (not the clone image default) so
// the checkout it writes into /work is owned by — and writable by — the build
// phases (in-tree codegen, e.g. graphql-codegen into a gitignored src/generated/).
func TestBuildJob_CloneRunsAsBuildUID(t *testing.T) {
	// managed nodeVersion -> clone inherits the managed image UID.
	t.Run("managed", func(t *testing.T) {
		app := baseApp()
		app.Spec.Pipeline.NodeVersion = 18
		app.Spec.Pipeline.Phases.Build.Command = []string{"yarn", "build"}
		r := reconcilerForPod()
		r.Config.NodeImages = map[string]domain.NodeImage{
			"18": {Image: "ghcr.io/toggle-corp/toggle-web-baker-node18@sha256:aaa", RunAsUser: ptr.To(int64(1000))},
		}
		clone := containerByName(r.BuildJob(app, "tok", gitCredentialDecision{}).Spec.Template.Spec.InitContainers, "clone")
		if clone.SecurityContext.RunAsUser == nil || *clone.SecurityContext.RunAsUser != 1000 {
			t.Fatalf("clone runAsUser = %v, want 1000 (build UID)", clone.SecurityContext.RunAsUser)
		}
		if clone.SecurityContext.RunAsNonRoot == nil || !*clone.SecurityContext.RunAsNonRoot {
			t.Fatalf("clone must still runAsNonRoot")
		}
	})

	// BYO build image + runAsUser -> clone inherits that UID.
	t.Run("byo build runAsUser", func(t *testing.T) {
		app := baseApp()
		app.Spec.Pipeline.Phases.Build.Image = "docker.io/cimg/node:18.20"
		app.Spec.Pipeline.Phases.Build.RunAsUser = ptr.To(int64(3434))
		clone := containerByName(reconcilerForPod().BuildJob(app, "tok", gitCredentialDecision{}).Spec.Template.Spec.InitContainers, "clone")
		if clone.SecurityContext.RunAsUser == nil || *clone.SecurityContext.RunAsUser != 3434 {
			t.Fatalf("clone runAsUser = %v, want 3434 (BYO build UID)", clone.SecurityContext.RunAsUser)
		}
	})

	// BYO build image WITHOUT runAsUser -> no resolved UID, clone keeps its image default (nil).
	t.Run("byo build no runAsUser", func(t *testing.T) {
		app := baseApp()
		app.Spec.Pipeline.Phases.Build.Image = "docker.io/cimg/node:18.20"
		clone := containerByName(reconcilerForPod().BuildJob(app, "tok", gitCredentialDecision{}).Spec.Template.Spec.InitContainers, "clone")
		if clone.SecurityContext.RunAsUser != nil {
			t.Fatalf("clone runAsUser = %v, want nil (image default)", *clone.SecurityContext.RunAsUser)
		}
	})
}

// DefaultNodeHome must equal the work emptyDir mount: HOME has to land on a
// writable volume under readOnlyRootFilesystem. Guards the cross-package
// coupling (domain.DefaultNodeHome vs controller workMountPath) against drift.
func TestDefaultNodeHomeIsWritableWorkMount(t *testing.T) {
	if domain.DefaultNodeHome != workMountPath {
		t.Fatalf("DefaultNodeHome %q must equal workMountPath %q (HOME must be a writable mount)", domain.DefaultNodeHome, workMountPath)
	}
}

func envValue(c *corev1.Container, name string) (string, bool) {
	for _, e := range c.Env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

// nodeVersion resolves every image-less phase to the operator's managed image,
// pins its UID, and injects the writable HOME — the app author writes none of it.
func TestBuildJob_NodeVersionResolvesManagedImageUIDAndHome(t *testing.T) {
	app := baseApp()
	app.Spec.Pipeline.NodeVersion = 18
	app.Spec.Pipeline.Phases.Build.Command = []string{"yarn", "build"}
	r := reconcilerForPod()
	r.Config.NodeImages = map[string]domain.NodeImage{
		"18": {Image: "ghcr.io/toggle-corp/toggle-web-baker-node18@sha256:aaa", RunAsUser: ptr.To(int64(1000))},
	}
	job := r.BuildJob(app, "tok", gitCredentialDecision{})

	for _, name := range []string{"setup", "fetch", "build"} {
		c := containerByName(job.Spec.Template.Spec.InitContainers, name)
		if c.Image != "ghcr.io/toggle-corp/toggle-web-baker-node18@sha256:aaa" {
			t.Fatalf("%s image = %q, want managed node18", name, c.Image)
		}
		if c.SecurityContext.RunAsUser == nil || *c.SecurityContext.RunAsUser != 1000 {
			t.Fatalf("%s runAsUser = %v, want 1000", name, c.SecurityContext.RunAsUser)
		}
		if home, ok := envValue(c, "HOME"); !ok || home != "/work" {
			t.Fatalf("%s HOME = %q (present=%v), want /work", name, home, ok)
		}
	}
}

// A per-phase image override opts that phase fully out of the managed toolchain:
// its own image, its own runAsUser, and NO injected managed HOME — while the
// other phases still inherit the managed image.
func TestBuildJob_PhaseImageOverrideOptsOutOfManaged(t *testing.T) {
	app := baseApp()
	app.Spec.Pipeline.NodeVersion = 18
	app.Spec.Pipeline.Phases.Build.Command = []string{"yarn", "build"}
	app.Spec.Pipeline.Phases.Fetch.Image = "docker.io/library/python:3.12"
	app.Spec.Pipeline.Phases.Fetch.RunAsUser = ptr.To(int64(4242))
	app.Spec.Pipeline.Phases.Fetch.Command = []string{"python", "fetch.py"}
	r := reconcilerForPod()
	r.Config.NodeImages = map[string]domain.NodeImage{
		"18": {Image: "ghcr.io/toggle-corp/toggle-web-baker-node18@sha256:aaa", RunAsUser: ptr.To(int64(1000))},
	}
	job := r.BuildJob(app, "tok", gitCredentialDecision{})

	fetch := containerByName(job.Spec.Template.Spec.InitContainers, "fetch")
	if fetch.Image != "docker.io/library/python:3.12" {
		t.Fatalf("fetch image override lost, got %q", fetch.Image)
	}
	if fetch.SecurityContext.RunAsUser == nil || *fetch.SecurityContext.RunAsUser != 4242 {
		t.Fatalf("fetch must keep its own UID 4242, got %v", fetch.SecurityContext.RunAsUser)
	}
	if _, ok := envValue(fetch, "HOME"); ok {
		t.Fatalf("BYO fetch must not get an injected managed HOME")
	}
	// build still inherits the managed image + UID.
	build := containerByName(job.Spec.Template.Spec.InitContainers, "build")
	if build.Image != "ghcr.io/toggle-corp/toggle-web-baker-node18@sha256:aaa" {
		t.Fatalf("build should inherit managed image, got %q", build.Image)
	}
}

// Security invariant: a malformed per-phase user memoryLimit must fall back to
// the operator per-phase DEFAULT (validated at startup, so always parses), never
// emit a pod with no memory limit (which could OOM the node).
func TestBuildJob_MalformedMemoryLimitFallsBackToPhaseDefault(t *testing.T) {
	app := baseApp()
	app.Spec.Pipeline.Phases.Build.MemoryLimit = "this-is-not-a-quantity"
	r := reconcilerForPod()
	job := r.BuildJob(app, "tok", gitCredentialDecision{})
	build := containerByName(job.Spec.Template.Spec.InitContainers, "build")
	if build == nil {
		t.Fatal("no build container")
	}
	want := r.Config.PhaseResourceDefaults.MemoryBuild
	lim, ok := build.Resources.Limits[corev1.ResourceMemory]
	if !ok {
		t.Fatalf("build container must have a memory limit even with malformed input")
	}
	if lim.Cmp(want) != 0 {
		t.Fatalf("expected fallback memory limit %s (build default), got %s", want.String(), lim.String())
	}
	// request must be pinned == limit.
	req := build.Resources.Requests[corev1.ResourceMemory]
	if req.Cmp(lim) != 0 {
		t.Fatalf("memory request %s must equal limit %s", req.String(), lim.String())
	}
}

// Every phase container (setup/fetch/build) now carries resource requirements:
// memory request==limit (node-OOM protection) and cpu request+limit from the
// operator defaults.
func TestBuildJob_AllPhasesCarryResourceRequirements(t *testing.T) {
	app := baseApp()
	// Managed toolchain so the omitted setup phase is injected (all three phase
	// containers exist and must carry resource requirements).
	app.Spec.Pipeline.NodeVersion = 18
	r := managedNodeReconciler()
	job := r.BuildJob(app, "tok", gitCredentialDecision{})
	prd := r.Config.PhaseResourceDefaults
	perPhaseMem := map[string]resource.Quantity{
		"setup": prd.MemorySetup,
		"fetch": prd.MemoryFetch,
		"build": prd.MemoryBuild,
	}
	for _, name := range []string{"setup", "fetch", "build"} {
		c := containerByName(job.Spec.Template.Spec.InitContainers, name)
		if c == nil {
			t.Fatalf("no %s container", name)
		}
		lim := c.Resources.Limits[corev1.ResourceMemory]
		req := c.Resources.Requests[corev1.ResourceMemory]
		wantMem := perPhaseMem[name]
		if lim.Cmp(wantMem) != 0 {
			t.Fatalf("%s memory limit = %s, want %s", name, lim.String(), wantMem.String())
		}
		if req.Cmp(lim) != 0 {
			t.Fatalf("%s memory request %s must equal limit %s", name, req.String(), lim.String())
		}
		cpuReq := c.Resources.Requests[corev1.ResourceCPU]
		cpuLim := c.Resources.Limits[corev1.ResourceCPU]
		if cpuReq.Cmp(prd.CPURequest) != 0 {
			t.Fatalf("%s cpu request = %s, want %s", name, cpuReq.String(), prd.CPURequest.String())
		}
		if cpuLim.Cmp(prd.CPULimit) != 0 {
			t.Fatalf("%s cpu limit = %s, want %s", name, cpuLim.String(), prd.CPULimit.String())
		}
	}
}

// A valid user build.memoryLimit overrides the operator default and pins
// request == limit.
func TestBuildJob_UserMemoryLimitOverridesDefault(t *testing.T) {
	app := baseApp()
	app.Spec.Pipeline.Phases.Build.MemoryLimit = "8Gi"
	r := reconcilerForPod()
	job := r.BuildJob(app, "tok", gitCredentialDecision{})
	build := containerByName(job.Spec.Template.Spec.InitContainers, "build")
	want := resource.MustParse("8Gi")
	lim := build.Resources.Limits[corev1.ResourceMemory]
	req := build.Resources.Requests[corev1.ResourceMemory]
	if lim.Cmp(want) != 0 {
		t.Fatalf("build memory limit = %s, want 8Gi", lim.String())
	}
	if req.Cmp(want) != 0 {
		t.Fatalf("build memory request = %s, want 8Gi (pinned == limit)", req.String())
	}
}

// The Job deadline comes from spec.pipeline.timeout when set, else from the
// operator config default.
func TestBuildJob_ActiveDeadlineFromSpecElseOperatorDefault(t *testing.T) {
	r := reconcilerForPod()

	def := baseApp()
	dj := r.BuildJob(def, "tok", gitCredentialDecision{})
	if dj.Spec.ActiveDeadlineSeconds == nil || *dj.Spec.ActiveDeadlineSeconds != r.Config.ActiveDeadlineSeconds {
		t.Fatalf("unset spec deadline must use operator default %d, got %v", r.Config.ActiveDeadlineSeconds, dj.Spec.ActiveDeadlineSeconds)
	}

	custom := baseApp()
	custom.Spec.Pipeline.Timeout = &metav1.Duration{Duration: 42 * time.Second}
	cj := r.BuildJob(custom, "tok", gitCredentialDecision{})
	if cj.Spec.ActiveDeadlineSeconds == nil || *cj.Spec.ActiveDeadlineSeconds != 42 {
		t.Fatalf("spec deadline 42 must win, got %v", cj.Spec.ActiveDeadlineSeconds)
	}

	// A duration string like "1h30m" must map to whole seconds.
	dur := baseApp()
	dur.Spec.Pipeline.Timeout = &metav1.Duration{Duration: time.Hour + 30*time.Minute}
	durJob := r.BuildJob(dur, "tok", gitCredentialDecision{})
	if durJob.Spec.ActiveDeadlineSeconds == nil || *durJob.Spec.ActiveDeadlineSeconds != 5400 {
		t.Fatalf("timeout 1h30m must map to 5400s, got %v", durJob.Spec.ActiveDeadlineSeconds)
	}

	// Non-positive durations (sub-second truncating to 0, or negative) must fall
	// back to the operator default — never a <0 or absent deadline the apiserver
	// would reject.
	for _, bad := range []time.Duration{500 * time.Millisecond, -time.Hour} {
		np := baseApp()
		np.Spec.Pipeline.Timeout = &metav1.Duration{Duration: bad}
		npJob := r.BuildJob(np, "tok", gitCredentialDecision{})
		if npJob.Spec.ActiveDeadlineSeconds == nil || *npJob.Spec.ActiveDeadlineSeconds != r.Config.ActiveDeadlineSeconds {
			t.Fatalf("timeout %v must fall back to operator default %d, got %v", bad, r.Config.ActiveDeadlineSeconds, npJob.Spec.ActiveDeadlineSeconds)
		}
	}
}

// Fix 1: nginx serving container pins runAsUser/runAsGroup to the unprivileged
// UID so runAsNonRoot admission passes, keeps readOnlyRootFilesystem, and the
// default image is the unprivileged variant.
func TestNginxDeployment_UnprivilegedSecurityContext(t *testing.T) {
	app := baseApp()
	r := reconcilerForPod()
	if r.Config.Images.Nginx != "docker.io/nginxinc/nginx-unprivileged:1.27-alpine" {
		t.Fatalf("expected unprivileged nginx image default, got %s", r.Config.Images.Nginx)
	}
	dep := r.nginxDeployment(app)
	c := dep.Spec.Template.Spec.Containers[0]
	sc := c.SecurityContext
	if sc == nil || sc.RunAsUser == nil || *sc.RunAsUser != nginxUID {
		t.Fatalf("nginx must runAsUser=%d, got %+v", nginxUID, sc)
	}
	if sc.RunAsGroup == nil || *sc.RunAsGroup != nginxUID {
		t.Fatalf("nginx must runAsGroup=%d, got %+v", nginxUID, sc)
	}
	if sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		t.Fatal("nginx must keep readOnlyRootFilesystem")
	}
	// Writable /tmp and /var/cache/nginx emptyDir mounts must exist for RO-rootfs.
	if mountByName(c.VolumeMounts, volTmp) == nil {
		t.Fatal("nginx must mount writable /tmp")
	}
	if mountByName(c.VolumeMounts, "nginx-cache") == nil {
		t.Fatal("nginx must mount writable /var/cache/nginx")
	}
	// containerPort must be 8080 to match the unprivileged listener + Service/netpol.
	if c.Ports[0].ContainerPort != 8080 {
		t.Fatalf("nginx containerPort must be 8080, got %d", c.Ports[0].ContainerPort)
	}
	// output PVC stays read-only.
	out := mountByName(c.VolumeMounts, volOutput)
	if out == nil || !out.ReadOnly {
		t.Fatal("nginx must mount the output PVC read-only")
	}
}

func assertEnvVar(t *testing.T, c *corev1.Container, name, want string) {
	t.Helper()
	for _, e := range c.Env {
		if e.Name == name {
			if e.Value != want {
				t.Fatalf("%s env %s = %q, want %q", c.Name, name, e.Value, want)
			}
			return
		}
	}
	t.Fatalf("%s container missing env %s", c.Name, name)
}

// Bug fix: clone speaks the clone image's env-var contract (REPO/REF/SRC_DIR),
// not CLI flags, so the entrypoint actually sees the repo.
func TestBuildJob_CloneUsesEnvNotArgs(t *testing.T) {
	app := baseApp()
	r := reconcilerForPod()
	job := r.BuildJob(app, "tok", gitCredentialDecision{})
	clone := containerByName(job.Spec.Template.Spec.InitContainers, "clone")
	if clone == nil {
		t.Fatal("no clone container")
	}
	if len(clone.Args) != 0 {
		t.Fatalf("clone must not use Args, got %v", clone.Args)
	}
	assertEnvVar(t, clone, "REPO", app.Spec.Repo)
	assertEnvVar(t, clone, "REF", app.Spec.Ref)
	assertEnvVar(t, clone, "SRC_DIR", "/work")
}

// Bug fix: copier speaks the copier image's env-var contract, not CLI flags.
func TestBuildJob_CopierUsesEnvNotArgs(t *testing.T) {
	app := baseApp()
	app.Spec.KeepReleases = 5
	r := reconcilerForPod()
	job := r.BuildJob(app, "tok", gitCredentialDecision{})
	copier := containerByName(job.Spec.Template.Spec.Containers, "copier")
	if copier == nil {
		t.Fatal("no copier container")
	}
	if len(copier.Args) != 0 {
		t.Fatalf("copier must not use Args, got %v", copier.Args)
	}
	outputDir := app.Spec.Pipeline.Phases.Build.OutputDir
	if outputDir == "" {
		outputDir = "dist"
	}
	assertEnvVar(t, copier, "WORKSPACE", "/work")
	assertEnvVar(t, copier, "OUTPUT_ROOT", "/output")
	assertEnvVar(t, copier, "OUTPUT_DIR", outputDir)
	assertEnvVar(t, copier, "KEEP_RELEASES", "5")
	assertEnvVar(t, copier, "BUILD_TOKEN", "tok")
}

// build.env is the single public build-env channel (buildArgs is gone): a var
// set there must reach the build container.
func TestBuildJob_BuildEnvReachesBuildContainer(t *testing.T) {
	app := baseApp()
	app.Spec.Pipeline.Phases.Build.Env = []bakerv1alpha1.EnvVar{{Name: "NEXT_PUBLIC_API", Value: "https://api"}}
	r := reconcilerForPod()
	job := r.BuildJob(app, "tok", gitCredentialDecision{})
	build := containerByName(job.Spec.Template.Spec.InitContainers, "build")
	if build == nil {
		t.Fatal("no build container")
	}
	assertEnvVar(t, build, "NEXT_PUBLIC_API", "https://api")
}

// envMap is the literal-only companion to env: its entries must reach the phase
// container AFTER the array env and SORTED BY KEY, so the container's env
// ordering is deterministic. Verified here on the build phase.
func TestBuildJob_BuildEnvMapReachesBuildContainerSortedAfterEnv(t *testing.T) {
	app := baseApp()
	app.Spec.Pipeline.Phases.Build.Env = []bakerv1alpha1.EnvVar{{Name: "NEXT_PUBLIC_API", Value: "https://api"}}
	app.Spec.Pipeline.Phases.Build.EnvMap = map[string]string{"ZED": "z", "ALPHA": "a", "MID": "m"}
	r := reconcilerForPod()
	job := r.BuildJob(app, "tok", gitCredentialDecision{})
	build := containerByName(job.Spec.Template.Spec.InitContainers, "build")
	if build == nil {
		t.Fatal("no build container")
	}
	assertEnvVar(t, build, "NEXT_PUBLIC_API", "https://api")
	assertEnvVar(t, build, "ALPHA", "a")
	assertEnvVar(t, build, "MID", "m")
	assertEnvVar(t, build, "ZED", "z")

	// The array env entry must precede all envMap entries, and the envMap entries
	// must appear in sorted-key order (ALPHA < MID < ZED).
	iEnv := envIndex(build, "NEXT_PUBLIC_API")
	iAlpha := envIndex(build, "ALPHA")
	iMid := envIndex(build, "MID")
	iZed := envIndex(build, "ZED")
	if iEnv >= iAlpha || iAlpha >= iMid || iMid >= iZed {
		t.Fatalf("envMap must follow array env sorted by key; got positions env=%d ALPHA=%d MID=%d ZED=%d", iEnv, iAlpha, iMid, iZed)
	}
}

// envMap applies uniformly across phases: its entries must reach the SETUP
// container too, after the phase's array env and the operator's prepended env
// (pmEnv/HOME).
func TestBuildJob_SetupEnvMapReachesSetupContainer(t *testing.T) {
	app := baseApp()
	app.Spec.Pipeline.Phases.Setup.Command = []string{"sh", "-c", "true"}
	app.Spec.Pipeline.Phases.Setup.Env = []bakerv1alpha1.EnvVar{{Name: "SETUP_ARR", Value: "1"}}
	app.Spec.Pipeline.Phases.Setup.EnvMap = map[string]string{"BETA": "b", "ALPHA": "a"}
	r := reconcilerForPod()
	job := r.BuildJob(app, "tok", gitCredentialDecision{})
	setup := containerByName(job.Spec.Template.Spec.InitContainers, "setup")
	if setup == nil {
		t.Fatal("no setup container")
	}
	assertEnvVar(t, setup, "ALPHA", "a")
	assertEnvVar(t, setup, "BETA", "b")
	iArr, iAlpha, iBeta := envIndex(setup, "SETUP_ARR"), envIndex(setup, "ALPHA"), envIndex(setup, "BETA")
	if iArr >= iAlpha || iAlpha >= iBeta {
		t.Fatalf("setup envMap must follow array env sorted by key; got SETUP_ARR=%d ALPHA=%d BETA=%d", iArr, iAlpha, iBeta)
	}
}

// In the fetch container envMap entries land after the array env and BEFORE the
// secret env, so a secret name colliding with an envMap key resolves to the
// secret (kubelet last-entry-wins) — the safe direction.
func TestBuildJob_FetchEnvMapReachesFetchBeforeSecrets(t *testing.T) {
	app := baseApp()
	app.Spec.Pipeline.Phases.Fetch.Env = []bakerv1alpha1.EnvVar{{Name: "FETCH_ARR", Value: "1"}}
	app.Spec.Pipeline.Phases.Fetch.EnvMap = map[string]string{"MAPPED": "m"}
	app.Spec.Pipeline.Phases.Fetch.Secrets = []bakerv1alpha1.EnvVarWithSecret{{
		Name:      "TOKEN",
		ValueFrom: bakerv1alpha1.EnvVarWithSecretSource{SecretKeyRef: bakerv1alpha1.SecretKeySelector{Name: "s", Key: "k"}},
	}}
	r := reconcilerForPod()
	job := r.BuildJob(app, "tok", gitCredentialDecision{})
	fetch := containerByName(job.Spec.Template.Spec.InitContainers, "fetch")
	if fetch == nil {
		t.Fatal("no fetch container")
	}
	assertEnvVar(t, fetch, "MAPPED", "m")
	iArr, iMapped, iSecret := envIndex(fetch, "FETCH_ARR"), envIndex(fetch, "MAPPED"), envIndex(fetch, "TOKEN")
	if iArr >= iMapped || iMapped >= iSecret {
		t.Fatalf("fetch order must be env < envMap < secrets; got FETCH_ARR=%d MAPPED=%d TOKEN=%d", iArr, iMapped, iSecret)
	}
}

// envIndex returns the position of the named env var in the container's Env
// slice, or -1 when absent.
func envIndex(c *corev1.Container, name string) int {
	for i, e := range c.Env {
		if e.Name == name {
			return i
		}
	}
	return -1
}

// pipeline.phases.build.outputDir flows to the copier's OUTPUT_DIR.
func TestBuildJob_BuildOutputDirFlowsToCopier(t *testing.T) {
	app := baseApp()
	app.Spec.Pipeline.Phases.Build.OutputDir = "out"
	r := reconcilerForPod()
	job := r.BuildJob(app, "tok", gitCredentialDecision{})
	copier := containerByName(job.Spec.Template.Spec.Containers, "copier")
	if copier == nil {
		t.Fatal("no copier container")
	}
	assertEnvVar(t, copier, "OUTPUT_DIR", "out")
}

// shimmed is the expected on-pod command for a phase: the peak-memory shim
// prefix, then the user command verbatim (the shim execs it unchanged).
func shimmed(cmd ...string) []string {
	return append([]string{"/baker/shim", "--"}, cmd...)
}

// Bug fix: a fetch phase with no command no-ops via ["true"] instead of falling
// through to the base image's clone entrypoint. The no-op, like every user phase
// command, runs behind the peak-memory shim. (setup is handled by effectiveSetup:
// an omitted setup is either injected or dropped, never a bare no-op container.)
func TestBuildJob_OptionalPhasesNoOpWhenUnset(t *testing.T) {
	app := baseApp()
	// A configured setup command keeps the setup container present so this test
	// exercises fetch's unset no-op without depending on setup semantics.
	app.Spec.Pipeline.Phases.Setup.Command = []string{"sh", "-c", "install"}
	r := reconcilerForPod()
	job := r.BuildJob(app, "tok", gitCredentialDecision{})
	fetch := containerByName(job.Spec.Template.Spec.InitContainers, "fetch")
	if fetch == nil {
		t.Fatal("no fetch container")
	}
	if !slices.Equal(fetch.Command, shimmed("true")) {
		t.Fatalf("fetch command must be the shimmed no-op %v when unset, got %v", shimmed("true"), fetch.Command)
	}
}

// When setup/fetch DO specify a command, it is preserved verbatim after the
// shim prefix (the shim execs it with identical argv).
func TestBuildJob_OptionalPhasesPreserveCommand(t *testing.T) {
	app := baseApp()
	app.Spec.Pipeline.Phases.Setup.Command = []string{"sh", "-c", "yarn install"}
	app.Spec.Pipeline.Phases.Fetch.Command = []string{"sh", "-c", "fetch-data"}
	r := reconcilerForPod()
	job := r.BuildJob(app, "tok", gitCredentialDecision{})
	setup := containerByName(job.Spec.Template.Spec.InitContainers, "setup")
	if !slices.Equal(setup.Command, shimmed(app.Spec.Pipeline.Phases.Setup.Command...)) {
		t.Fatalf("setup command not preserved behind the shim: got %v", setup.Command)
	}
	fetch := containerByName(job.Spec.Template.Spec.InitContainers, "fetch")
	if !slices.Equal(fetch.Command, shimmed(app.Spec.Pipeline.Phases.Fetch.Command...)) {
		t.Fatalf("fetch command not preserved behind the shim: got %v", fetch.Command)
	}
}

// Build is mandatory: its command must be passed through verbatim behind the
// shim, never no-oped.
func TestBuildJob_BuildCommandNotNoOped(t *testing.T) {
	app := baseApp()
	app.Spec.Pipeline.Phases.Build.Command = []string{"sh", "-c", "yarn build"}
	r := reconcilerForPod()
	job := r.BuildJob(app, "tok", gitCredentialDecision{})
	build := containerByName(job.Spec.Template.Spec.InitContainers, "build")
	if !slices.Equal(build.Command, shimmed(app.Spec.Pipeline.Phases.Build.Command...)) {
		t.Fatalf("build command must be the shimmed spec build command, got %v", build.Command)
	}
	if slices.Contains(build.Command, "true") {
		t.Fatalf("build command must NOT be no-oped to [\"true\"]")
	}
}

// The shim-install init container runs FIRST (so the binary exists before any
// user phase) and user phases mount the shim volume read-only.
func TestBuildJob_ShimInstallFirstAndMountsReadOnly(t *testing.T) {
	app := baseApp()
	// Managed toolchain so the injected setup container exists and is checked too.
	app.Spec.Pipeline.NodeVersion = 18
	r := managedNodeReconciler()
	job := r.BuildJob(app, "tok", gitCredentialDecision{})
	inits := job.Spec.Template.Spec.InitContainers
	if len(inits) == 0 || inits[0].Name != "shim-install" {
		names := make([]string, 0, len(inits))
		for _, c := range inits {
			names = append(names, c.Name)
		}
		t.Fatalf("shim-install must be the FIRST init container, got order %v", names)
	}
	for _, name := range []string{"setup", "fetch", "build"} {
		c := containerByName(inits, name)
		found := false
		for _, m := range c.VolumeMounts {
			if m.Name == "shim" {
				found = true
				if !m.ReadOnly {
					t.Errorf("%s shim mount must be read-only", name)
				}
			}
		}
		if !found {
			t.Errorf("%s must mount the shim volume", name)
		}
	}
	// clone and copier are platform entrypoints: no shim mount, no wrapping.
	for _, c := range []*corev1.Container{
		containerByName(inits, "clone"),
		containerByName(job.Spec.Template.Spec.Containers, "copier"),
	} {
		for _, m := range c.VolumeMounts {
			if m.Name == "shim" {
				t.Errorf("%s must NOT mount the shim volume", c.Name)
			}
		}
	}
}

// spec.Submodules=true wires SUBMODULES=1 into the clone container so the
// entrypoint opts into submodule recursion.
func TestBuildJob_SubmodulesEnvWhenEnabled(t *testing.T) {
	app := baseApp()
	app.Spec.Pipeline.Submodules = true
	r := reconcilerForPod()
	job := r.BuildJob(app, "tok", gitCredentialDecision{})
	clone := containerByName(job.Spec.Template.Spec.InitContainers, "clone")
	if clone == nil {
		t.Fatal("no clone container")
	}
	assertEnvVar(t, clone, "SUBMODULES", "1")
}

// Default (spec.Submodules=false): no SUBMODULES env, so the entrypoint skips
// submodule recursion.
func TestBuildJob_NoSubmodulesEnvByDefault(t *testing.T) {
	app := baseApp()
	r := reconcilerForPod()
	job := r.BuildJob(app, "tok", gitCredentialDecision{})
	clone := containerByName(job.Spec.Template.Spec.InitContainers, "clone")
	if clone == nil {
		t.Fatal("no clone container")
	}
	if hasEnv(clone.Env, "SUBMODULES") {
		t.Fatalf("clone container must NOT set SUBMODULES when spec.Submodules is false")
	}
}

// managedNodeReconciler is reconcilerForPod with a node18 managed image and a
// NON-DEFAULT setup command per package manager, so a test asserting the
// injected command proves it came from config (not a hardcoded default).
func managedNodeReconciler() *AppReconciler {
	r := reconcilerForPod()
	r.Config.NodeImages = map[string]domain.NodeImage{
		"18": {Image: "ghcr.io/toggle-corp/toggle-web-baker-node18@sha256:aaa", RunAsUser: ptr.To(int64(1000))},
	}
	r.Config.DefaultSetupCommands = map[string][]string{
		"yarn": {"yarn", "install", "--immutable"},
		"pnpm": {"pnpm", "install", "--offline"},
	}
	return r
}

// Managed toolchain + setup wholly omitted: the setup container RUNS with the
// operator's configured default install command for the package manager,
// wrapped by the shim like every other user phase.
func TestBuildJob_ManagedOmittedSetupInjectsDefaultCommand(t *testing.T) {
	app := baseApp()
	app.Spec.Pipeline.NodeVersion = 18
	app.Spec.Pipeline.Phases.Build.Command = []string{"yarn", "build"}
	r := managedNodeReconciler()

	job := r.BuildJob(app, "tok", gitCredentialDecision{})
	setup := containerByName(job.Spec.Template.Spec.InitContainers, "setup")
	if setup == nil {
		t.Fatal("managed + omitted setup must still produce a setup container")
	}
	want := shimWrap([]string{"yarn", "install", "--immutable"})
	if !slices.Equal(setup.Command, want) {
		t.Fatalf("setup command = %v, want config default wrapped by shim %v", setup.Command, want)
	}
}

// pnpm app under the managed toolchain gets the pnpm default, proving the
// injected command branches on packageManager.
func TestBuildJob_ManagedOmittedSetupPnpmDefault(t *testing.T) {
	app := baseApp()
	app.Spec.Pipeline.PackageManager = bakerv1alpha1.PackageManagerPnpm
	app.Spec.Pipeline.NodeVersion = 18
	app.Spec.Pipeline.Phases.Build.Command = []string{"pnpm", "build"}
	r := managedNodeReconciler()

	job := r.BuildJob(app, "tok", gitCredentialDecision{})
	setup := containerByName(job.Spec.Template.Spec.InitContainers, "setup")
	if setup == nil {
		t.Fatal("managed + omitted pnpm setup must produce a setup container")
	}
	want := shimWrap([]string{"pnpm", "install", "--offline"})
	if !slices.Equal(setup.Command, want) {
		t.Fatalf("setup command = %v, want pnpm config default %v", setup.Command, want)
	}
}

// setup.skip:true under the managed toolchain: NO setup container at all.
func TestBuildJob_ManagedSkipNoSetupContainer(t *testing.T) {
	app := baseApp()
	app.Spec.Pipeline.NodeVersion = 18
	app.Spec.Pipeline.Phases.Setup.Skip = true
	app.Spec.Pipeline.Phases.Build.Command = []string{"yarn", "build"}
	r := managedNodeReconciler()

	job := r.BuildJob(app, "tok", gitCredentialDecision{})
	if setup := containerByName(job.Spec.Template.Spec.InitContainers, "setup"); setup != nil {
		t.Fatalf("setup.skip must drop the setup container, got %+v", setup.Command)
	}
	// The rest of the pipeline is intact and ordered.
	var names []string
	for _, c := range job.Spec.Template.Spec.InitContainers {
		names = append(names, c.Name)
	}
	want := []string{"shim-install", "clone", "fetch", "build"}
	if !slices.Equal(names, want) {
		t.Fatalf("init containers = %v, want %v", names, want)
	}
}

// An explicitly configured setup command is run verbatim (no injection).
func TestBuildJob_ManagedExplicitSetupUntouched(t *testing.T) {
	app := baseApp()
	app.Spec.Pipeline.NodeVersion = 18
	app.Spec.Pipeline.Phases.Setup.Command = []string{"yarn", "install", "--custom"}
	app.Spec.Pipeline.Phases.Build.Command = []string{"yarn", "build"}
	r := managedNodeReconciler()

	job := r.BuildJob(app, "tok", gitCredentialDecision{})
	setup := containerByName(job.Spec.Template.Spec.InitContainers, "setup")
	if setup == nil {
		t.Fatal("explicit setup must produce a setup container")
	}
	want := shimWrap([]string{"yarn", "install", "--custom"})
	if !slices.Equal(setup.Command, want) {
		t.Fatalf("explicit setup command = %v, want %v (untouched)", setup.Command, want)
	}
}

// BYO toolchain (nodeVersion unset): omitted setup injects nothing (no setup
// container), and skip:true is a harmless no-op (also no setup container).
func TestBuildJob_BYONoSetupInjection(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*bakerv1alpha1.App)
	}{
		{"omitted", func(*bakerv1alpha1.App) {}},
		{"skip", func(a *bakerv1alpha1.App) { a.Spec.Pipeline.Phases.Setup.Skip = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := baseApp() // no nodeVersion => BYO
			app.Spec.Pipeline.Phases.Build.Image = "docker.io/library/node:18"
			app.Spec.Pipeline.Phases.Build.Command = []string{"yarn", "build"}
			tc.mut(app)
			r := managedNodeReconciler()

			job := r.BuildJob(app, "tok", gitCredentialDecision{})
			if setup := containerByName(job.Spec.Template.Spec.InitContainers, "setup"); setup != nil {
				t.Fatalf("BYO %s setup must not inject a setup container, got %+v", tc.name, setup.Command)
			}
		})
	}
}

// The injected default setup command must NOT influence the staleness hash:
// buildSpecFrom reads the spec as-written, so an app with omitted setup hashes
// the same regardless of the operator's injection config.
func TestBuildSpecHash_OmittedSetupUnaffectedByInjectionConfig(t *testing.T) {
	app := baseApp()
	app.Spec.Pipeline.NodeVersion = 18 // managed => injection WOULD apply in the pod
	app.Spec.Pipeline.Phases.Build.Command = []string{"yarn", "build"}

	baseline := buildSpecFrom(app).Hash()

	// Two reconcilers with DIFFERENT injected setup defaults must both leave the
	// spec (and thus the hash) untouched — the injection lives only in the pod.
	rA := managedNodeReconciler()
	rA.Config.DefaultSetupCommands = map[string][]string{"yarn": {"yarn", "install", "--immutable"}, "pnpm": {"pnpm", "install"}}
	rB := managedNodeReconciler()
	rB.Config.DefaultSetupCommands = map[string][]string{"yarn": {"yarn", "install", "--production=false"}, "pnpm": {"pnpm", "install"}}

	jobA := rA.BuildJob(app, "tok", gitCredentialDecision{})
	jobB := rB.BuildJob(app, "tok", gitCredentialDecision{})

	// The two pods genuinely differ (different injected setup commands)...
	setupA := containerByName(jobA.Spec.Template.Spec.InitContainers, "setup")
	setupB := containerByName(jobB.Spec.Template.Spec.InitContainers, "setup")
	if slices.Equal(setupA.Command, setupB.Command) {
		t.Fatalf("fixture broken: both configs injected the same setup command %v", setupA.Command)
	}
	// ...yet the spec hash is unchanged in both cases (injection never leaks).
	hashA := jobA.Annotations[bakerv1alpha1.SpecHashAnnotation]
	hashB := jobB.Annotations[bakerv1alpha1.SpecHashAnnotation]
	if hashA != baseline || hashB != baseline {
		t.Fatalf("injected setup command leaked into hash: baseline=%q A=%q B=%q", baseline, hashA, hashB)
	}
	// And the spec itself still carries no setup command.
	if cmd := app.Spec.Pipeline.Phases.Setup.Command; len(cmd) != 0 {
		t.Fatalf("omitted setup unexpectedly carries a command in the spec: %v", cmd)
	}
}

// Flipping setup.skip is a real pipeline change (the setup phase disappears),
// so it MUST flow through buildSpecFrom into the staleness hash — this guards
// the controller-level mapping, not just the domain field (which the domain
// tests exercise directly).
func TestBuildSpecFrom_SetupSkipFlipsHash(t *testing.T) {
	app := baseApp()
	app.Spec.Pipeline.NodeVersion = 18
	app.Spec.Pipeline.Phases.Build.Command = []string{"yarn", "build"}

	baseline := buildSpecFrom(app).Hash()
	app.Spec.Pipeline.Phases.Setup.Skip = true
	if got := buildSpecFrom(app).Hash(); got == baseline {
		t.Fatalf("setup.skip flip did not change the build-spec hash (%q)", got)
	}
}

// A build.envMap entry is a real build-env change (it can alter the artifact),
// so it MUST flow through buildSpecFrom/mergeEnv into the staleness hash. This
// exercises mergeEnv's envMap fold — the domain layer can't, since it only sees
// the already-merged Env map.
func TestBuildSpecFrom_EnvMapFlipsHash(t *testing.T) {
	app := baseApp()
	app.Spec.Pipeline.NodeVersion = 18
	app.Spec.Pipeline.Phases.Build.Command = []string{"yarn", "build"}

	baseline := buildSpecFrom(app).Hash()
	app.Spec.Pipeline.Phases.Build.EnvMap = map[string]string{"NEXT_PUBLIC_ENV": "uat"}
	if got := buildSpecFrom(app).Hash(); got == baseline {
		t.Fatalf("adding a build.envMap entry did not change the build-spec hash (%q)", got)
	}
}

// env and envMap merge into one per-phase Env map before hashing, so moving a
// literal KEY=v from the array env to envMap yields the same effective build
// environment — same artifact, same hash — and must NOT trigger a rebuild.
func TestBuildSpecFrom_LiteralMoveBetweenEnvAndEnvMapSameHash(t *testing.T) {
	viaArray := baseApp()
	viaArray.Spec.Pipeline.NodeVersion = 18
	viaArray.Spec.Pipeline.Phases.Build.Command = []string{"yarn", "build"}
	viaArray.Spec.Pipeline.Phases.Build.Env = []bakerv1alpha1.EnvVar{{Name: "TOKEN", Value: "abc"}}

	viaMap := baseApp()
	viaMap.Spec.Pipeline.NodeVersion = 18
	viaMap.Spec.Pipeline.Phases.Build.Command = []string{"yarn", "build"}
	viaMap.Spec.Pipeline.Phases.Build.EnvMap = map[string]string{"TOKEN": "abc"}

	if viaArray.Spec.Pipeline.Phases.Build.EnvMap != nil || viaMap.Spec.Pipeline.Phases.Build.Env != nil {
		t.Fatal("fixture broken: the two apps must express TOKEN through different channels")
	}
	if a, b := buildSpecFrom(viaArray).Hash(), buildSpecFrom(viaMap).Hash(); a != b {
		t.Fatalf("moving a literal between env and envMap changed the hash: array=%q map=%q", a, b)
	}
}

func hasEnv(env []corev1.EnvVar, name string) bool {
	for _, e := range env {
		if e.Name == name {
			return true
		}
	}
	return false
}
