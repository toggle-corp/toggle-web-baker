package controller

import (
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
	if !hasEnv(pbuild.Env, "PNPM_STORE_DIR") || !hasEnv(pbuild.Env, "NODE_MODULES_DIR") {
		t.Fatalf("pnpm build must set PNPM_STORE_DIR and NODE_MODULES_DIR (store+node_modules on cache PVC)")
	}
	// Both PMs mount cache RW into build.
	if mountByName(pbuild.VolumeMounts, volCache) == nil {
		t.Fatalf("pnpm build must mount the cache PVC")
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

func hasEnv(env []corev1.EnvVar, name string) bool {
	for _, e := range env {
		if e.Name == name {
			return true
		}
	}
	return false
}
