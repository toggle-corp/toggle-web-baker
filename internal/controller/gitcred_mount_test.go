package controller

import (
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
)

// findCloneContainer returns the clone initContainer of a build Job.
func findCloneContainer(t *testing.T, job *batchv1.Job) *corev1.Container {
	t.Helper()
	for i := range job.Spec.Template.Spec.InitContainers {
		if job.Spec.Template.Spec.InitContainers[i].Name == "clone" {
			return &job.Spec.Template.Spec.InitContainers[i]
		}
	}
	t.Fatal("clone initContainer not found")
	return nil
}

func hasGitCredVolume(pod corev1.PodSpec, wantSecret string) bool {
	for _, v := range pod.Volumes {
		if v.Name == volGitCred && v.Secret != nil && v.Secret.SecretName == wantSecret {
			return true
		}
	}
	return false
}

func containerHasGitCredMountAndEnv(c *corev1.Container) bool {
	mountOK := false
	for _, m := range c.VolumeMounts {
		if m.Name == volGitCred && m.MountPath == gitCredMountPath && m.ReadOnly {
			mountOK = true
		}
	}
	envOK := false
	for _, e := range c.Env {
		if e.Name == "GIT_CREDENTIAL_DIR" && e.Value == gitCredMountPath {
			envOK = true
		}
	}
	return mountOK && envOK
}

// Global allowlisted path: the build pod's clone initContainer mounts the synced
// copy Secret at /run/git-credential (RO) with GIT_CREDENTIAL_DIR set.
func TestGitCredMount_BuildCloneMountsSyncedCopy(t *testing.T) {
	app := baseApp()
	app.Spec.Repo = "https://github.com/org/repo.git"
	r, _ := newReconciler(t)
	enableGitAuth(r)

	job := r.BuildJob(app, "tok")
	if !hasGitCredVolume(job.Spec.Template.Spec, gitCredentialSecretName(app)) {
		t.Fatalf("build pod missing git-credential volume for synced copy; volumes=%v", job.Spec.Template.Spec.Volumes)
	}
	if !containerHasGitCredMountAndEnv(findCloneContainer(t, job)) {
		t.Fatal("clone container missing git-credential mount/env")
	}
}

// Override path: the clone container mounts the USER's Secret directly.
func TestGitCredMount_BuildCloneMountsUserSecretForOverride(t *testing.T) {
	app := baseApp()
	app.Spec.Repo = "https://evil.example.com/x.git"
	app.Spec.RepoAuth = &bakerv1alpha1.RepoAuthConfig{SecretRef: bakerv1alpha1.RepoAuthSecretRef{Name: "my-cred"}}
	r, _ := newReconciler(t)
	enableGitAuth(r)

	job := r.BuildJob(app, "tok")
	if !hasGitCredVolume(job.Spec.Template.Spec, "my-cred") {
		t.Fatal("build pod must mount the user's override Secret directly")
	}
	if !containerHasGitCredMountAndEnv(findCloneContainer(t, job)) {
		t.Fatal("clone container missing git-credential mount/env for override")
	}
}

// Anonymous path: no git-credential volume/mount at all.
func TestGitCredMount_AnonymousNoMount(t *testing.T) {
	app := baseApp()
	app.Spec.Repo = "https://gitlab.com/org/repo.git"
	r, _ := newReconciler(t)
	enableGitAuth(r) // enabled but host not allowlisted

	job := r.BuildJob(app, "tok")
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == volGitCred {
			t.Fatal("anonymous build must not carry a git-credential volume")
		}
	}
	clone := findCloneContainer(t, job)
	for _, m := range clone.VolumeMounts {
		if m.Name == volGitCred {
			t.Fatal("anonymous clone must not mount a git-credential volume")
		}
	}
}

// The watch CronJob pod mounts the credential (global allowlisted path).
func TestGitCredMount_WatchCronJobMountsCredential(t *testing.T) {
	app := baseApp()
	app.Spec.Repo = "https://github.com/org/repo.git"
	app.Spec.WatchCommits = &bakerv1alpha1.WatchCommitsSpec{Enabled: true}
	r, _ := newReconciler(t)
	enableGitAuth(r)

	cj, err := r.watchCronJob(app)
	if err != nil {
		t.Fatalf("watchCronJob: %v", err)
	}
	pod := cj.Spec.JobTemplate.Spec.Template.Spec
	if !hasGitCredVolume(pod, gitCredentialSecretName(app)) {
		t.Fatal("watch CronJob missing git-credential volume")
	}
	if len(pod.Containers) != 1 || !containerHasGitCredMountAndEnv(&pod.Containers[0]) {
		t.Fatal("watch container missing git-credential mount/env")
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
