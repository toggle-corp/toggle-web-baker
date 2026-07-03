package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
)

// enableGitAuth points the reconciler at an operator-namespace source Secret.
func enableGitAuth(r *AppReconciler) {
	r.OperatorNamespace = "baker-system"
	r.Config.GitAuth = GitAuth{SecretName: "baker-git-credential", Hosts: []string{"github.com"}}
}

// sourceGitSecret is the operator-global source Secret fixture, built on the
// shared gitAuthSecret fixture (F8 dedup) at the enableGitAuth ns/name.
func sourceGitSecret() *corev1.Secret {
	return gitAuthSecret("baker-system", "baker-git-credential",
		map[string][]byte{"username": []byte("bot"), "password": []byte("tok-secret")})
}

func getSecret(t *testing.T, r *AppReconciler, name, ns string) (*corev1.Secret, bool) {
	t.Helper()
	s := &corev1.Secret{}
	err := r.Get(context.Background(), types.NamespacedName{Name: name, Namespace: ns}, s)
	if err != nil {
		return nil, false
	}
	return s, true
}

// Global path, allowlisted repo: a synced copy Secret is created in the app ns,
// owned by the app, carrying only username/password copied from the source.
func TestGitCred_GlobalAllowlisted_SyncsOwnedCopy(t *testing.T) {
	app := baseApp()
	app.Spec.Repo = "https://github.com/org/repo.git"
	r, cl := newReconciler(t, app, wffc(), sourceGitSecret())
	enableGitAuth(r)
	reconcile(t, r, app)

	got, ok := getSecret(t, r, gitCredentialSecretName(app), app.Namespace)
	if !ok {
		t.Fatal("synced copy Secret not created")
	}
	if string(got.Data["username"]) != "bot" || string(got.Data["password"]) != "tok-secret" {
		t.Fatalf("synced data mismatch: %v", got.Data)
	}
	if ref := metav1.GetControllerOf(got); ref == nil || ref.Name != app.Name {
		t.Fatalf("synced copy not owned by app, ownerRef=%v", ref)
	}
	if got.Labels["app.kubernetes.io/managed-by"] != managedBy {
		t.Fatalf("synced copy missing standard labels: %v", got.Labels)
	}
	_ = cl
}

// Non-allowlisted repo under an enabled global config: no copy is synced.
func TestGitCred_GlobalNotAllowlisted_NoCopy(t *testing.T) {
	app := baseApp()
	app.Spec.Repo = "https://gitlab.com/org/repo.git"
	r, _ := newReconciler(t, app, wffc(), sourceGitSecret())
	enableGitAuth(r)
	reconcile(t, r, app)

	if _, ok := getSecret(t, r, gitCredentialSecretName(app), app.Namespace); ok {
		t.Fatal("no synced copy expected for a non-allowlisted host")
	}
}

// A previously-synced copy must be REMOVED once the app stops qualifying (repo
// changed to a non-allowlisted host).
func TestGitCred_SyncedCopyRemovedWhenNoLongerEligible(t *testing.T) {
	app := baseApp()
	app.Spec.Repo = "https://github.com/org/repo.git"
	r, cl := newReconciler(t, app, wffc(), sourceGitSecret())
	enableGitAuth(r)
	reconcile(t, r, app)
	if _, ok := getSecret(t, r, gitCredentialSecretName(app), app.Namespace); !ok {
		t.Fatal("precondition: synced copy should exist")
	}

	live := getApp(t, cl, app.Name, app.Namespace)
	live.Spec.Repo = "https://gitlab.com/org/repo.git"
	if err := cl.Update(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	reconcile(t, r, app)

	if _, ok := getSecret(t, r, gitCredentialSecretName(app), app.Namespace); ok {
		t.Fatal("synced copy should be removed when the app no longer qualifies")
	}
}

// Per-app override: the user's Secret is mounted directly; no synced copy is
// created even when the repo host is NOT allowlisted.
func TestGitCred_Override_NoCopyMountsUserSecret(t *testing.T) {
	app := baseApp()
	app.Spec.Repo = "https://evil.example.com/x.git"
	app.Spec.RepoAuth = &bakerv1alpha1.RepoAuthConfig{SecretRef: bakerv1alpha1.RepoAuthSecretRef{Name: "my-cred"}}
	userSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: app.Namespace, Name: "my-cred"},
		Data:       map[string][]byte{"username": []byte("u"), "password": []byte("p")},
	}
	r, _ := newReconciler(t, app, wffc(), userSecret)
	enableGitAuth(r)
	reconcile(t, r, app)

	if _, ok := getSecret(t, r, gitCredentialSecretName(app), app.Namespace); ok {
		t.Fatal("override must not create a synced copy")
	}
}

// Override with a missing Secret → Degraded (Ready=False), message names the
// Secret only and no build Job is created.
func TestGitCred_OverrideMissingSecret_Degrades(t *testing.T) {
	app := baseApp()
	app.Annotations = map[string]string{bakerv1alpha1.RebuildAnnotation: "tok-1"}
	app.Spec.RepoAuth = &bakerv1alpha1.RepoAuthConfig{SecretRef: bakerv1alpha1.RepoAuthSecretRef{Name: "absent-cred"}}
	r, cl := newReconciler(t, app, wffc())
	reconcile(t, r, app) // finalizer
	reconcile(t, r, app) // should degrade, not build

	got := getApp(t, cl, app.Name, app.Namespace)
	if got.Status.Phase != bakerv1alpha1.PhaseDegraded {
		t.Fatalf("phase = %s, want Degraded", got.Status.Phase)
	}
	cond := findCondition(got, bakerv1alpha1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != bakerv1alpha1.ReasonInvalidRepoAuth {
		t.Fatalf("Ready cond = %+v, want False/%s", cond, bakerv1alpha1.ReasonInvalidRepoAuth)
	}
	if !strings.Contains(cond.Message, "absent-cred") {
		t.Fatalf("message must name the Secret, got %q", cond.Message)
	}
	// No build Job while degraded.
	if active, _, _ := r.buildActive(context.Background(), got); active {
		t.Fatal("must not start a build while repoAuth is broken")
	}
}

// Override with an incomplete Secret (missing password) → Degraded; message
// never leaks values.
func TestGitCred_OverrideIncompleteSecret_Degrades(t *testing.T) {
	app := baseApp()
	app.Spec.RepoAuth = &bakerv1alpha1.RepoAuthConfig{SecretRef: bakerv1alpha1.RepoAuthSecretRef{Name: "half-cred"}}
	half := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: app.Namespace, Name: "half-cred"},
		Data:       map[string][]byte{"username": []byte("only-user"), "password": []byte("")},
	}
	r, cl := newReconciler(t, app, wffc(), half)
	reconcile(t, r, app)

	got := getApp(t, cl, app.Name, app.Namespace)
	if got.Status.Phase != bakerv1alpha1.PhaseDegraded {
		t.Fatalf("phase = %s, want Degraded", got.Status.Phase)
	}
	cond := findCondition(got, bakerv1alpha1.ConditionReady)
	if cond == nil || cond.Reason != bakerv1alpha1.ReasonInvalidRepoAuth {
		t.Fatalf("Ready cond = %+v, want %s", cond, bakerv1alpha1.ReasonInvalidRepoAuth)
	}
	if strings.Contains(cond.Message, "only-user") {
		t.Fatalf("message must not leak values, got %q", cond.Message)
	}
}

// Source Secret deleted at runtime (Q9-3, fail-static): an existing synced copy
// is NOT deleted or blanked, and the app is NOT degraded.
func TestGitCred_SourceDeletedAtRuntime_FailStatic(t *testing.T) {
	app := baseApp()
	app.Spec.Repo = "https://github.com/org/repo.git"
	r, cl := newReconciler(t, app, wffc(), sourceGitSecret())
	enableGitAuth(r)
	reconcile(t, r, app)
	copyBefore, ok := getSecret(t, r, gitCredentialSecretName(app), app.Namespace)
	if !ok {
		t.Fatal("precondition: synced copy should exist")
	}

	// Delete the source Secret in the operator namespace.
	src := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "baker-system", Name: "baker-git-credential"}}
	if err := cl.Delete(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	// Reconcile must not error, must not degrade, must keep the copy intact.
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: app.Name, Namespace: app.Namespace}}); err != nil {
		t.Fatalf("reconcile must not error on source deletion (fail-static): %v", err)
	}
	got := getApp(t, cl, app.Name, app.Namespace)
	if got.Status.Phase == bakerv1alpha1.PhaseDegraded {
		t.Fatal("app must NOT degrade when the source Secret is deleted at runtime")
	}
	copyAfter, ok := getSecret(t, r, gitCredentialSecretName(app), app.Namespace)
	if !ok {
		t.Fatal("synced copy must survive source deletion (fail-static)")
	}
	if string(copyAfter.Data["password"]) != string(copyBefore.Data["password"]) {
		t.Fatal("synced copy must not be blanked on source deletion")
	}
}

// F1 regression: gitAuth enabled + source Secret deleted + NO existing copy
// (brand-new App). Reconcile must decide anonymous (fail-static, no copy to
// preserve), and BOTH the build pod AND the watch CronJob rendered from that ONE
// threaded decision must carry NO credential volume — not the naive
// decideGitCredential result (which would mount a nonexistent <app>-git-credential
// and wedge the pods in ContainerCreating forever).
func TestGitCred_SourceMissingNoCopy_MountWiringAnonymous(t *testing.T) {
	app := baseApp()
	app.Spec.Repo = "https://github.com/org/repo.git"
	app.Spec.WatchCommits = &bakerv1alpha1.WatchCommitsSpec{Enabled: true}
	// gitAuth enabled + host allowlisted, but the source Secret is ABSENT and no
	// prior copy exists.
	r, _ := newReconciler(t, app, wffc())
	enableGitAuth(r)

	gitCred, err := r.reconcileGitCredential(context.Background(), app)
	if err != nil {
		t.Fatalf("reconcileGitCredential must not error (fail-static): %v", err)
	}
	if gitCred.mounts() || gitCred.syncCopy {
		t.Fatalf("no source + no copy must resolve to anonymous, got %+v", gitCred)
	}

	// Build pod: no credential volume.
	job := r.BuildJob(app, "tok", gitCred)
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == volGitCred {
			t.Fatal("build pod must NOT mount a credential when source is absent and no copy exists")
		}
	}
	// Watch CronJob: no credential volume.
	cj, err := r.watchCronJob(app, gitCred)
	if err != nil {
		t.Fatalf("watchCronJob: %v", err)
	}
	for _, v := range cj.Spec.JobTemplate.Spec.Template.Spec.Volumes {
		if v.Name == volGitCred {
			t.Fatal("watch CronJob must NOT mount a credential when source is absent and no copy exists")
		}
	}
}

// F2 regression: rotating the source Secret to INVALID keys (empty password) is
// treated identically to a missing source (fail-static): the existing synced
// copy is left UNCHANGED — no garbage propagated.
func TestGitCred_SourceRotatedToBadKeys_CopyUnchanged(t *testing.T) {
	app := baseApp()
	app.Spec.Repo = "https://github.com/org/repo.git"
	r, cl := newReconciler(t, app, wffc(), sourceGitSecret())
	enableGitAuth(r)
	reconcile(t, r, app)
	before, ok := getSecret(t, r, gitCredentialSecretName(app), app.Namespace)
	if !ok {
		t.Fatal("precondition: synced copy should exist")
	}

	// Rotate the source to an invalid (empty-password) state.
	src, _ := getSecret(t, r, "baker-git-credential", "baker-system")
	src.Data["password"] = []byte("")
	if err := cl.Update(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	reconcile(t, r, app)

	after, ok := getSecret(t, r, gitCredentialSecretName(app), app.Namespace)
	if !ok {
		t.Fatal("synced copy must survive a bad-keys rotation (fail-static)")
	}
	if string(after.Data["username"]) != string(before.Data["username"]) ||
		string(after.Data["password"]) != string(before.Data["password"]) {
		t.Fatalf("synced copy must not change on a bad-keys rotation: before=%v after=%v", before.Data, after.Data)
	}
}

// F3 regression: switching from the global credential to a TYPO'd override must
// reclaim the now-orphaned global copy in the app namespace — the copy sweep runs
// BEFORE the repoAuth fail(). Previously the broken-override Degraded
// short-circuit ran first and leaked the global copy indefinitely.
func TestGitCred_SwitchGlobalToBrokenOverride_ReclaimsCopy(t *testing.T) {
	app := baseApp()
	app.Spec.Repo = "https://github.com/org/repo.git"
	r, cl := newReconciler(t, app, wffc(), sourceGitSecret())
	enableGitAuth(r)
	reconcile(t, r, app)
	if _, ok := getSecret(t, r, gitCredentialSecretName(app), app.Namespace); !ok {
		t.Fatal("precondition: global synced copy should exist")
	}

	// Add a repoAuth override pointing at a MISSING Secret (a typo).
	live := getApp(t, cl, app.Name, app.Namespace)
	live.Spec.RepoAuth = &bakerv1alpha1.RepoAuthConfig{SecretRef: bakerv1alpha1.RepoAuthSecretRef{Name: "typo-cred"}}
	if err := cl.Update(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	// Reconcile: the app degrades on the broken override, but the sweep (which runs
	// first) must already have reclaimed the orphaned global copy.
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: app.Name, Namespace: app.Namespace}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := getApp(t, cl, app.Name, app.Namespace)
	if got.Status.Phase != bakerv1alpha1.PhaseDegraded {
		t.Fatalf("phase = %s, want Degraded (broken override)", got.Status.Phase)
	}
	if _, ok := getSecret(t, r, gitCredentialSecretName(app), app.Namespace); ok {
		t.Fatal("orphaned global copy must be reclaimed even when the new override is broken")
	}
}

// F5: the synced copy carries a content-hash annotation, and a reconcile with an
// unchanged source does NOT bump the copy's resourceVersion (no-op SSA skip).
func TestGitCred_UnchangedSource_SkipsNoopPatch(t *testing.T) {
	app := baseApp()
	app.Spec.Repo = "https://github.com/org/repo.git"
	r, _ := newReconciler(t, app, wffc(), sourceGitSecret())
	enableGitAuth(r)
	reconcile(t, r, app)
	first, ok := getSecret(t, r, gitCredentialSecretName(app), app.Namespace)
	if !ok {
		t.Fatal("precondition: synced copy should exist")
	}
	if first.Annotations[gitCredHashAnnotation] == "" {
		t.Fatal("synced copy must carry the content-hash annotation (F5)")
	}
	rv := first.ResourceVersion

	// A second reconcile with the SAME source must not rewrite the copy.
	reconcile(t, r, app)
	second, _ := getSecret(t, r, gitCredentialSecretName(app), app.Namespace)
	if second.ResourceVersion != rv {
		t.Fatalf("unchanged source must skip the SSA patch: rv %s -> %s", rv, second.ResourceVersion)
	}
}

// F6: the fail-static source-missing Warning Event fires ONCE per outage
// (missing→present transition), not once per reconcile — no Event storm.
func TestGitCred_SourceMissing_EventFiresOncePerOutage(t *testing.T) {
	app := baseApp()
	app.Spec.Repo = "https://github.com/org/repo.git"
	r, _ := newReconciler(t, app, wffc(), sourceGitSecret())
	enableGitAuth(r)
	rec := record.NewFakeRecorder(16)
	r.Recorder = rec
	reconcile(t, r, app) // creates the copy; source present ⇒ no Event

	// Delete the source, then reconcile several times.
	src := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "baker-system", Name: "baker-git-credential"}}
	if err := r.Delete(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		reconcile(t, r, app)
	}
	if got := len(rec.Events); got != 1 {
		t.Fatalf("source-missing Event must fire once per outage, got %d events", got)
	}
}
