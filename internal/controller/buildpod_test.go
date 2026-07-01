package controller

import (
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
	"github.com/toggle-corp/toggle-web-baker/internal/domain"
)

func reconcilerForPod() *FrontendAppReconciler {
	r := &FrontendAppReconciler{}
	r.Config.Defaults()
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
	app.Spec.Secrets = []bakerv1alpha1.EnvVarWithSecret{{
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
	yarn.Spec.PackageManager = bakerv1alpha1.PackageManagerYarn
	yjob := r.BuildJob(yarn, "t")
	ybuild := containerByName(yjob.Spec.Template.Spec.InitContainers, "build")
	if !hasEnv(ybuild.Env, "YARN_CACHE_FOLDER") {
		t.Fatalf("yarn build must set YARN_CACHE_FOLDER")
	}

	pnpm := baseApp()
	pnpm.Spec.PackageManager = bakerv1alpha1.PackageManagerPnpm
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

// spec.build.runAsUser pins the build container's numeric UID (needed for images
// whose USER is a non-numeric name, e.g. cimg/node's `circleci`). Phases without
// runAsUser keep the default hardened context (RunAsUser nil).
// BYO path: a phase with its own image pins its own runAsUser; an image-less
// phase (fetch here, with no nodeVersion) must NOT inherit it.
func TestBuildJob_RunAsUserPinnedPerPhase(t *testing.T) {
	app := baseApp()
	app.Spec.Build.Image = "docker.io/cimg/node:18.20"
	app.Spec.Build.RunAsUser = ptr.To(int64(3434))
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
	app.Spec.NodeVersion = 18
	app.Spec.Build.Command = []string{"yarn", "build"}
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
	app.Spec.NodeVersion = 18
	app.Spec.Build.Command = []string{"yarn", "build"}
	app.Spec.Fetch.Image = "docker.io/library/python:3.12"
	app.Spec.Fetch.RunAsUser = ptr.To(int64(4242))
	app.Spec.Fetch.Command = []string{"python", "fetch.py"}
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

// Fix 8: a malformed memoryLimit must fall back to the 6Gi default, never emit a
// pod with no memory limit.
func TestBuildJob_MalformedMemoryLimitFallsBackTo6Gi(t *testing.T) {
	app := baseApp()
	app.Spec.Resources.Build.MemoryLimit = "this-is-not-a-quantity"
	r := reconcilerForPod()
	job := r.BuildJob(app, "tok")
	build := containerByName(job.Spec.Template.Spec.InitContainers, "build")
	if build == nil {
		t.Fatal("no build container")
	}
	q, ok := build.Resources.Limits[corev1.ResourceMemory]
	if !ok {
		t.Fatalf("build container must have a memory limit even with malformed input")
	}
	if q.String() != "6Gi" {
		t.Fatalf("expected fallback memory limit 6Gi, got %s", q.String())
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
	outputDir := app.Spec.OutputDir
	if outputDir == "" {
		outputDir = "dist"
	}
	assertEnvVar(t, copier, "WORKSPACE", "/work")
	assertEnvVar(t, copier, "OUTPUT_ROOT", "/output")
	assertEnvVar(t, copier, "OUTPUT_DIR", outputDir)
	assertEnvVar(t, copier, "KEEP_RELEASES", "5")
	assertEnvVar(t, copier, "BUILD_TOKEN", "tok")
}

// Bug fix: optional phases (setup/fetch) with no command no-op via ["true"]
// instead of falling through to the base image's clone entrypoint.
func TestBuildJob_OptionalPhasesNoOpWhenUnset(t *testing.T) {
	app := baseApp()
	r := reconcilerForPod()
	job := r.BuildJob(app, "tok")
	for _, name := range []string{"setup", "fetch"} {
		c := containerByName(job.Spec.Template.Spec.InitContainers, name)
		if c == nil {
			t.Fatalf("no %s container", name)
		}
		if len(c.Command) != 1 || c.Command[0] != "true" {
			t.Fatalf("%s command must be [\"true\"] when unset, got %v", name, c.Command)
		}
	}
}

// When setup/fetch DO specify a command, it is preserved (not replaced by no-op).
func TestBuildJob_OptionalPhasesPreserveCommand(t *testing.T) {
	app := baseApp()
	app.Spec.Setup.Command = []string{"sh", "-c", "yarn install"}
	app.Spec.Fetch.Command = []string{"sh", "-c", "fetch-data"}
	r := reconcilerForPod()
	job := r.BuildJob(app, "tok")
	setup := containerByName(job.Spec.Template.Spec.InitContainers, "setup")
	if !slices.Equal(setup.Command, app.Spec.Setup.Command) {
		t.Fatalf("setup command not preserved: got %v", setup.Command)
	}
	fetch := containerByName(job.Spec.Template.Spec.InitContainers, "fetch")
	if !slices.Equal(fetch.Command, app.Spec.Fetch.Command) {
		t.Fatalf("fetch command not preserved: got %v", fetch.Command)
	}
}

// Build is mandatory: its command must be passed through as-is, never no-oped.
func TestBuildJob_BuildCommandNotNoOped(t *testing.T) {
	app := baseApp()
	app.Spec.Build.Command = []string{"sh", "-c", "yarn build"}
	r := reconcilerForPod()
	job := r.BuildJob(app, "tok")
	build := containerByName(job.Spec.Template.Spec.InitContainers, "build")
	if !slices.Equal(build.Command, app.Spec.Build.Command) {
		t.Fatalf("build command must equal spec build command, got %v", build.Command)
	}
	if len(build.Command) == 1 && build.Command[0] == "true" {
		t.Fatalf("build command must NOT be no-oped to [\"true\"]")
	}
}

// spec.Submodules=true wires SUBMODULES=1 into the clone container so the
// entrypoint opts into submodule recursion.
func TestBuildJob_SubmodulesEnvWhenEnabled(t *testing.T) {
	app := baseApp()
	app.Spec.Submodules = true
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
