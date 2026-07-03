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
	job := r.BuildJob(app, "tok")
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
	job := r.BuildJob(app, "tok")

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
	yjob := r.BuildJob(yarn, "t")
	ybuild := containerByName(yjob.Spec.Template.Spec.InitContainers, "build")
	if !hasEnv(ybuild.Env, "YARN_CACHE_FOLDER") {
		t.Fatalf("yarn build must set YARN_CACHE_FOLDER")
	}

	pnpm := baseApp()
	pnpm.Spec.Pipeline.PackageManager = bakerv1alpha1.PackageManagerPnpm
	pjob := r.BuildJob(pnpm, "t")
	pbuild := containerByName(pjob.Spec.Template.Spec.InitContainers, "build")
	if !hasEnv(pbuild.Env, "npm_config_store_dir") || !hasEnv(pbuild.Env, "npm_config_modules_dir") {
		t.Fatalf("pnpm build must set npm_config_store_dir and npm_config_modules_dir (store+node_modules on cache PVC)")
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

// Requirement 4: hardened securityContext + single-pod invariants.
func TestBuildJob_HardenedSecurity(t *testing.T) {
	app := baseApp()
	r := reconcilerForPod()
	job := r.BuildJob(app, "tok")
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
	job := r.BuildJob(app, "tok")

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
		clone := containerByName(r.BuildJob(app, "tok").Spec.Template.Spec.InitContainers, "clone")
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
		clone := containerByName(reconcilerForPod().BuildJob(app, "tok").Spec.Template.Spec.InitContainers, "clone")
		if clone.SecurityContext.RunAsUser == nil || *clone.SecurityContext.RunAsUser != 3434 {
			t.Fatalf("clone runAsUser = %v, want 3434 (BYO build UID)", clone.SecurityContext.RunAsUser)
		}
	})

	// BYO build image WITHOUT runAsUser -> no resolved UID, clone keeps its image default (nil).
	t.Run("byo build no runAsUser", func(t *testing.T) {
		app := baseApp()
		app.Spec.Pipeline.Phases.Build.Image = "docker.io/cimg/node:18.20"
		clone := containerByName(reconcilerForPod().BuildJob(app, "tok").Spec.Template.Spec.InitContainers, "clone")
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
	job := r.BuildJob(app, "tok")

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
	job := r.BuildJob(app, "tok")

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
	job := r.BuildJob(app, "tok")
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
	r := reconcilerForPod()
	job := r.BuildJob(app, "tok")
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
	job := r.BuildJob(app, "tok")
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
	dj := r.BuildJob(def, "tok")
	if dj.Spec.ActiveDeadlineSeconds == nil || *dj.Spec.ActiveDeadlineSeconds != r.Config.ActiveDeadlineSeconds {
		t.Fatalf("unset spec deadline must use operator default %d, got %v", r.Config.ActiveDeadlineSeconds, dj.Spec.ActiveDeadlineSeconds)
	}

	custom := baseApp()
	custom.Spec.Pipeline.Timeout = &metav1.Duration{Duration: 42 * time.Second}
	cj := r.BuildJob(custom, "tok")
	if cj.Spec.ActiveDeadlineSeconds == nil || *cj.Spec.ActiveDeadlineSeconds != 42 {
		t.Fatalf("spec deadline 42 must win, got %v", cj.Spec.ActiveDeadlineSeconds)
	}

	// A duration string like "1h30m" must map to whole seconds.
	dur := baseApp()
	dur.Spec.Pipeline.Timeout = &metav1.Duration{Duration: time.Hour + 30*time.Minute}
	durJob := r.BuildJob(dur, "tok")
	if durJob.Spec.ActiveDeadlineSeconds == nil || *durJob.Spec.ActiveDeadlineSeconds != 5400 {
		t.Fatalf("timeout 1h30m must map to 5400s, got %v", durJob.Spec.ActiveDeadlineSeconds)
	}

	// Non-positive durations (sub-second truncating to 0, or negative) must fall
	// back to the operator default — never a <0 or absent deadline the apiserver
	// would reject.
	for _, bad := range []time.Duration{500 * time.Millisecond, -time.Hour} {
		np := baseApp()
		np.Spec.Pipeline.Timeout = &metav1.Duration{Duration: bad}
		npJob := r.BuildJob(np, "tok")
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
	job := r.BuildJob(app, "tok")
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
	job := r.BuildJob(app, "tok")
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
	job := r.BuildJob(app, "tok")
	build := containerByName(job.Spec.Template.Spec.InitContainers, "build")
	if build == nil {
		t.Fatal("no build container")
	}
	assertEnvVar(t, build, "NEXT_PUBLIC_API", "https://api")
}

// pipeline.phases.build.outputDir flows to the copier's OUTPUT_DIR.
func TestBuildJob_BuildOutputDirFlowsToCopier(t *testing.T) {
	app := baseApp()
	app.Spec.Pipeline.Phases.Build.OutputDir = "out"
	r := reconcilerForPod()
	job := r.BuildJob(app, "tok")
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

// Bug fix: optional phases (setup/fetch) with no command no-op via ["true"]
// instead of falling through to the base image's clone entrypoint. The no-op,
// like every user phase command, runs behind the peak-memory shim.
func TestBuildJob_OptionalPhasesNoOpWhenUnset(t *testing.T) {
	app := baseApp()
	r := reconcilerForPod()
	job := r.BuildJob(app, "tok")
	for _, name := range []string{"setup", "fetch"} {
		c := containerByName(job.Spec.Template.Spec.InitContainers, name)
		if c == nil {
			t.Fatalf("no %s container", name)
		}
		if !slices.Equal(c.Command, shimmed("true")) {
			t.Fatalf("%s command must be the shimmed no-op %v when unset, got %v", name, shimmed("true"), c.Command)
		}
	}
}

// When setup/fetch DO specify a command, it is preserved verbatim after the
// shim prefix (the shim execs it with identical argv).
func TestBuildJob_OptionalPhasesPreserveCommand(t *testing.T) {
	app := baseApp()
	app.Spec.Pipeline.Phases.Setup.Command = []string{"sh", "-c", "yarn install"}
	app.Spec.Pipeline.Phases.Fetch.Command = []string{"sh", "-c", "fetch-data"}
	r := reconcilerForPod()
	job := r.BuildJob(app, "tok")
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
	job := r.BuildJob(app, "tok")
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
	r := reconcilerForPod()
	job := r.BuildJob(app, "tok")
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
	job := r.BuildJob(app, "tok")
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
	job := r.BuildJob(app, "tok")
	clone := containerByName(job.Spec.Template.Spec.InitContainers, "clone")
	if clone == nil {
		t.Fatal("no clone container")
	}
	if hasEnv(clone.Env, "SUBMODULES") {
		t.Fatalf("clone container must NOT set SUBMODULES when spec.Submodules is false")
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
