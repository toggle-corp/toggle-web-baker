package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlreconcile "sigs.k8s.io/controller-runtime/pkg/reconcile"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
)

func appIn(ns, name, repo string, repoAuth *bakerv1alpha1.RepoAuthConfig) *bakerv1alpha1.App {
	a := &bakerv1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       bakerv1alpha1.AppSpec{Repo: repo, RepoAuth: repoAuth},
	}
	return a
}

func reqSet(reqs []ctrlreconcile.Request) map[string]bool {
	m := map[string]bool{}
	for _, r := range reqs {
		m[r.Namespace+"/"+r.Name] = true
	}
	return m
}

// A change to the GLOBAL source Secret (operator namespace + configured name)
// enqueues ALL Apps — the reconcile per-app re-derives whether it actually uses
// the global credential, so enqueuing all is correct and simplest.
func TestGitCredInformer_SourceSecretChange_EnqueuesAllApps(t *testing.T) {
	a1 := appIn("apps", "one", "https://github.com/o/r.git", nil)
	a2 := appIn("other", "two", "https://gitlab.com/o/r.git", nil)
	r, _ := newReconciler(t, a1, a2)
	enableGitAuth(r)

	src := gitAuthSecret("baker-system", "baker-git-credential", nil)
	got := reqSet(r.mapSecretToApps(context.Background(), src))
	if !got["apps/one"] || !got["other/two"] {
		t.Fatalf("source-secret change must enqueue all apps, got %v", got)
	}
}

// A change to a Secret in an app namespace that an App's repoAuth references
// enqueues that App (drift-correction + Degraded-recovery reactivity).
func TestGitCredInformer_RepoAuthSecretChange_EnqueuesReferencingApp(t *testing.T) {
	a1 := appIn("apps", "one", "https://x/r.git", &bakerv1alpha1.RepoAuthConfig{SecretRef: bakerv1alpha1.RepoAuthSecretRef{Name: "user-cred"}})
	a2 := appIn("apps", "two", "https://x/r.git", nil)
	r, _ := newReconciler(t, a1, a2)
	enableGitAuth(r)

	sec := gitAuthSecret("apps", "user-cred", nil)
	got := reqSet(r.mapSecretToApps(context.Background(), sec))
	if !got["apps/one"] {
		t.Fatalf("repoAuth-referenced secret change must enqueue the referencing app, got %v", got)
	}
	if got["apps/two"] {
		t.Fatalf("must NOT enqueue an app that does not reference the secret, got %v", got)
	}
}

// A change to a synced-copy child Secret enqueues its owning App (drift-correct).
func TestGitCredInformer_SyncedCopyChange_EnqueuesOwningApp(t *testing.T) {
	a1 := appIn("apps", "one", "https://github.com/o/r.git", nil)
	r, _ := newReconciler(t, a1)
	enableGitAuth(r)

	// Synced-copy Secret follows the <app>-git-credential naming in the app ns.
	sec := gitAuthSecret("apps", gitCredentialSecretName(a1), nil)
	got := reqSet(r.mapSecretToApps(context.Background(), sec))
	if !got["apps/one"] {
		t.Fatalf("synced-copy change must enqueue owning app, got %v", got)
	}
}

// An unrelated Secret enqueues nothing.
func TestGitCredInformer_UnrelatedSecret_EnqueuesNothing(t *testing.T) {
	a1 := appIn("apps", "one", "https://github.com/o/r.git", nil)
	r, _ := newReconciler(t, a1)
	enableGitAuth(r)

	sec := gitAuthSecret("apps", "random-tls", nil)
	if got := r.mapSecretToApps(context.Background(), sec); len(got) != 0 {
		t.Fatalf("unrelated secret must enqueue nothing, got %v", got)
	}
}
