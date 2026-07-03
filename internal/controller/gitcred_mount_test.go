package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
)

func hasGitCredVolume(pod corev1.PodSpec, wantSecret string) bool {
	for _, v := range pod.Volumes {
		if v.Name == volGitCred && v.Secret != nil && v.Secret.SecretName == wantSecret {
			return true
		}
	}
	return false
}

// containerHasGitCredMountAndEnv checks the RO mount + GIT_CREDENTIAL_DIR AND the
// host-scoping GIT_CREDENTIAL_HOST env (F7: value must be the lowercase repo host).
func containerHasGitCredMountAndEnv(c *corev1.Container, wantHost string) bool {
	if mountByName(c.VolumeMounts, volGitCred) == nil {
		return false
	}
	m := mountByName(c.VolumeMounts, volGitCred)
	if m.MountPath != gitCredMountPath || !m.ReadOnly {
		return false
	}
	dirOK, hostOK := false, false
	for _, e := range c.Env {
		if e.Name == "GIT_CREDENTIAL_DIR" && e.Value == gitCredMountPath {
			dirOK = true
		}
		if e.Name == gitCredHostEnv && e.Value == wantHost {
			hostOK = true
		}
	}
	return dirOK && hostOK
}

// Global allowlisted path: the build pod's clone initContainer mounts the synced
// copy Secret at /run/git-credential (RO) with GIT_CREDENTIAL_DIR + host env set.
func TestGitCredMount_BuildCloneMountsSyncedCopy(t *testing.T) {
	app := baseApp()
	app.Spec.Repo = "https://github.com/org/repo.git"
	r, _ := newReconciler(t)
	enableGitAuth(r)

	d := decideGitCredential(app, r.Config.GitAuth)
	job := r.BuildJob(app, "tok", d)
	if !hasGitCredVolume(job.Spec.Template.Spec, gitCredentialSecretName(app)) {
		t.Fatalf("build pod missing git-credential volume for synced copy; volumes=%v", job.Spec.Template.Spec.Volumes)
	}
	clone := containerByName(job.Spec.Template.Spec.InitContainers, "clone")
	if clone == nil || !containerHasGitCredMountAndEnv(clone, "github.com") {
		t.Fatal("clone container missing git-credential mount/env or host scope")
	}
}

// Override path: the clone container mounts the USER's Secret directly, scoped to
// the (non-allowlisted) repo's own host.
func TestGitCredMount_BuildCloneMountsUserSecretForOverride(t *testing.T) {
	app := baseApp()
	app.Spec.Repo = "https://evil.example.com/x.git"
	app.Spec.RepoAuth = &bakerv1alpha1.RepoAuthConfig{SecretRef: bakerv1alpha1.RepoAuthSecretRef{Name: "my-cred"}}
	r, _ := newReconciler(t)
	enableGitAuth(r)

	d := decideGitCredential(app, r.Config.GitAuth)
	job := r.BuildJob(app, "tok", d)
	if !hasGitCredVolume(job.Spec.Template.Spec, "my-cred") {
		t.Fatal("build pod must mount the user's override Secret directly")
	}
	clone := containerByName(job.Spec.Template.Spec.InitContainers, "clone")
	if clone == nil || !containerHasGitCredMountAndEnv(clone, "evil.example.com") {
		t.Fatal("clone container missing git-credential mount/env for override")
	}
}

// F7 fail-closed: an override whose repo URL is unparseable must NOT mount a
// credential (no host to scope it to ⇒ anonymous), even though the decision
// resolves a secret name.
func TestGitCredMount_OverrideUnparseableRepo_NoMount(t *testing.T) {
	app := baseApp()
	app.Spec.Repo = "::::not a url::::"
	app.Spec.RepoAuth = &bakerv1alpha1.RepoAuthConfig{SecretRef: bakerv1alpha1.RepoAuthSecretRef{Name: "my-cred"}}
	r, _ := newReconciler(t)
	enableGitAuth(r)

	d := decideGitCredential(app, r.Config.GitAuth)
	job := r.BuildJob(app, "tok", d)
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == volGitCred {
			t.Fatal("must not mount a credential when the repo host is unparseable (fail-closed)")
		}
	}
}

// Anonymous path: no git-credential volume/mount at all.
func TestGitCredMount_AnonymousNoMount(t *testing.T) {
	app := baseApp()
	app.Spec.Repo = "https://gitlab.com/org/repo.git"
	r, _ := newReconciler(t)
	enableGitAuth(r) // enabled but host not allowlisted

	d := decideGitCredential(app, r.Config.GitAuth)
	job := r.BuildJob(app, "tok", d)
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == volGitCred {
			t.Fatal("anonymous build must not carry a git-credential volume")
		}
	}
	clone := containerByName(job.Spec.Template.Spec.InitContainers, "clone")
	if mountByName(clone.VolumeMounts, volGitCred) != nil {
		t.Fatal("anonymous clone must not mount a git-credential volume")
	}
}

// The watch CronJob pod mounts the credential (global allowlisted path), scoped.
func TestGitCredMount_WatchCronJobMountsCredential(t *testing.T) {
	app := baseApp()
	app.Spec.Repo = "https://github.com/org/repo.git"
	app.Spec.WatchCommits = &bakerv1alpha1.WatchCommitsSpec{Enabled: true}
	r, _ := newReconciler(t)
	enableGitAuth(r)

	d := decideGitCredential(app, r.Config.GitAuth)
	cj, err := r.watchCronJob(app, d)
	if err != nil {
		t.Fatalf("watchCronJob: %v", err)
	}
	pod := cj.Spec.JobTemplate.Spec.Template.Spec
	if !hasGitCredVolume(pod, gitCredentialSecretName(app)) {
		t.Fatal("watch CronJob missing git-credential volume")
	}
	if len(pod.Containers) != 1 || !containerHasGitCredMountAndEnv(&pod.Containers[0], "github.com") {
		t.Fatal("watch container missing git-credential mount/env or host scope")
	}
}

// The scheduled-builds CLOCK CronJob must NOT mount the credential (it only
// patches the rebuild annotation; it never touches the repo).
func TestGitCredMount_ClockCronJobDoesNotMount(t *testing.T) {
	app := baseApp()
	app.Spec.Repo = "https://github.com/org/repo.git"
	app.Spec.ScheduledBuilds = &bakerv1alpha1.ScheduledBuildsSpec{Enabled: true}
	r, _ := newReconciler(t)
	enableGitAuth(r)

	cj := r.clockCronJob(app)
	pod := cj.Spec.JobTemplate.Spec.Template.Spec
	for _, v := range pod.Volumes {
		if v.Name == volGitCred {
			t.Fatal("clock CronJob must not mount the git credential")
		}
	}
}
