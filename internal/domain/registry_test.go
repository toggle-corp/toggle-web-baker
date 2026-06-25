package domain

import "testing"

// Registry allowlist is enforced at reconcile time (no validating webhook).
// The user-overridable phase images (setup/fetch/build) must match one of the
// operator-configured allowlist prefixes; otherwise the operator refuses to
// build and sets Ready=False / ImageNotAllowed.

func TestCheckImagesAllowed_RejectsImageOutsideAllowlist(t *testing.T) {
	allowlist := []string{"ghcr.io/toggle-corp/", "docker.io/library/node:"}
	images := []PhaseImage{
		{Phase: "build", Image: "evil.example.com/miner:latest"},
	}
	err := CheckImagesAllowed(allowlist, images)
	if err == nil {
		t.Fatalf("expected error: build image outside allowlist must be rejected")
	}
	if !contains(err.Error(), "evil.example.com/miner:latest") {
		t.Fatalf("error should name the offending image, got: %v", err)
	}
	if !contains(err.Error(), "build") {
		t.Fatalf("error should name the offending phase, got: %v", err)
	}
}

func TestCheckImagesAllowed_AcceptsImagesMatchingAllowlist(t *testing.T) {
	allowlist := []string{"ghcr.io/toggle-corp/", "docker.io/library/node:"}
	images := []PhaseImage{
		{Phase: "setup", Image: "docker.io/library/node:20-bookworm"},
		{Phase: "fetch", Image: "ghcr.io/toggle-corp/fetcher:1.2.3"},
		{Phase: "build", Image: "docker.io/library/node:20-bookworm"},
	}
	if err := CheckImagesAllowed(allowlist, images); err != nil {
		t.Fatalf("allowlisted images should pass, got: %v", err)
	}
}

func TestCheckImagesAllowed_EmptyAllowlistRejectsEverything(t *testing.T) {
	images := []PhaseImage{{Phase: "build", Image: "docker.io/library/node:20"}}
	if err := CheckImagesAllowed(nil, images); err == nil {
		t.Fatalf("empty allowlist must fail closed (reject all images), not allow all")
	}
}
