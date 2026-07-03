package controller

import (
	"testing"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
)

// The per-app override wins unconditionally: mount the user's Secret directly,
// no synced copy, host allowlist NOT consulted (even a non-allowlisted repo).
func TestGitCredentialDecision_OverrideWins(t *testing.T) {
	app := baseApp()
	app.Spec.Repo = "https://evil.example.com/x.git" // not allowlisted
	app.Spec.RepoAuth = &bakerv1alpha1.RepoAuthConfig{SecretRef: bakerv1alpha1.RepoAuthSecretRef{Name: "my-cred"}}
	cfg := GitAuth{SecretName: "baker-git-credential", Hosts: []string{"github.com"}}

	d := decideGitCredential(app, cfg)
	if !d.mount {
		t.Fatal("override must mount")
	}
	if d.syncCopy {
		t.Fatal("override must NOT sync a copy (mounts the user Secret directly)")
	}
	if d.secretName != "my-cred" {
		t.Fatalf("secretName = %q, want the user's Secret my-cred", d.secretName)
	}
}

// Global path: gitAuth enabled AND repo host allowlisted → sync a per-app copy
// and mount it under the derived name.
func TestGitCredentialDecision_GlobalAllowlisted_SyncsCopy(t *testing.T) {
	app := baseApp()
	app.Spec.Repo = "https://github.com/org/repo.git"
	cfg := GitAuth{SecretName: "baker-git-credential", Hosts: []string{"github.com"}}

	d := decideGitCredential(app, cfg)
	if !d.mount || !d.syncCopy {
		t.Fatalf("allowlisted global must mount+sync, got %+v", d)
	}
	if d.secretName != gitCredentialSecretName(app) {
		t.Fatalf("secretName = %q, want synced-copy name %q", d.secretName, gitCredentialSecretName(app))
	}
}

// Global enabled but repo host NOT allowlisted → anonymous (no mount, no copy).
func TestGitCredentialDecision_GlobalNotAllowlisted_Anonymous(t *testing.T) {
	app := baseApp()
	app.Spec.Repo = "https://gitlab.com/org/repo.git"
	cfg := GitAuth{SecretName: "baker-git-credential", Hosts: []string{"github.com"}}

	d := decideGitCredential(app, cfg)
	if d.mount || d.syncCopy {
		t.Fatalf("non-allowlisted host must be anonymous, got %+v", d)
	}
}

// gitAuth disabled and no override → anonymous.
func TestGitCredentialDecision_DisabledNoOverride_Anonymous(t *testing.T) {
	app := baseApp()
	app.Spec.Repo = "https://github.com/org/repo.git"
	d := decideGitCredential(app, GitAuth{})
	if d.mount || d.syncCopy {
		t.Fatalf("disabled + no override must be anonymous, got %+v", d)
	}
}
