package controller

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
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

func hasEnv(env []corev1.EnvVar, name string) bool {
	for _, e := range env {
		if e.Name == name {
			return true
		}
	}
	return false
}
