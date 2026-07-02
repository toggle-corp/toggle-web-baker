package domain

import (
	"testing"

	"k8s.io/utils/ptr"
)

// The operator maps a user-selected node MAJOR (spec.pipeline.nodeVersion) to a managed,
// digest-pinned image plus the numeric UID and writable HOME that image needs.
// ResolvePhase is the single chokepoint the build pod consults per phase; it
// encodes the composition rule: an explicit per-phase image opts fully out of
// the managed image/UID/HOME, while an omitted image inherits all three.

func sampleNodeImages() map[string]NodeImage {
	return map[string]NodeImage{
		"18": {Image: "ghcr.io/toggle-corp/toggle-web-baker-node18@sha256:aaa", RunAsUser: ptr.To(int64(1000))},
		"24": {Image: "ghcr.io/toggle-corp/toggle-web-baker-node24@sha256:bbb", RunAsUser: ptr.To(int64(1000))},
	}
}

func TestResolvePhase_ManagedInheritsImageUIDAndHome(t *testing.T) {
	// A phase with no explicit image + a mapped nodeVersion resolves to the
	// mapped image, its UID, and the default writable HOME.
	got := ResolvePhase("", nil, 18, sampleNodeImages(), "clone-fallback")
	if got.Image != "ghcr.io/toggle-corp/toggle-web-baker-node18@sha256:aaa" {
		t.Fatalf("expected managed node18 image, got %q", got.Image)
	}
	if got.RunAsUser == nil || *got.RunAsUser != 1000 {
		t.Fatalf("expected managed UID 1000, got %v", got.RunAsUser)
	}
	if got.Home != DefaultNodeHome {
		t.Fatalf("expected default HOME %q, got %q", DefaultNodeHome, got.Home)
	}
}

func TestResolvePhase_ExplicitImageOptsOutOfManaged(t *testing.T) {
	// A phase that sets its own image is fully BYO even when nodeVersion is set:
	// its own runAsUser, and NO managed HOME (the managed UID/HOME are for a
	// different image and would be wrong to apply).
	got := ResolvePhase("docker.io/python:3.12", ptr.To(int64(4242)), 18, sampleNodeImages(), "clone-fallback")
	if got.Image != "docker.io/python:3.12" {
		t.Fatalf("BYO image must win over nodeVersion, got %q", got.Image)
	}
	if got.RunAsUser == nil || *got.RunAsUser != 4242 {
		t.Fatalf("BYO phase keeps its own UID, got %v", got.RunAsUser)
	}
	if got.Home != "" {
		t.Fatalf("BYO phase must not inherit managed HOME, got %q", got.Home)
	}
}

func TestResolvePhase_PerEntryHomeOverride(t *testing.T) {
	imgs := map[string]NodeImage{
		"18": {Image: "img@sha256:aaa", RunAsUser: ptr.To(int64(1000)), Home: "/home/node"},
	}
	got := ResolvePhase("", nil, 18, imgs, "clone-fallback")
	if got.Home != "/home/node" {
		t.Fatalf("expected per-entry HOME override, got %q", got.Home)
	}
}

func TestResolvePhase_FallsBackToCloneWhenNoImageAndNoNodeVersion(t *testing.T) {
	// setup/fetch with neither an image nor a nodeVersion keep the historical
	// clone-image fallback, with no HOME injection.
	got := ResolvePhase("", nil, 0, sampleNodeImages(), "clone-fallback")
	if got.Image != "clone-fallback" {
		t.Fatalf("expected clone fallback, got %q", got.Image)
	}
	if got.RunAsUser != nil || got.Home != "" {
		t.Fatalf("clone fallback injects no UID/HOME, got uid=%v home=%q", got.RunAsUser, got.Home)
	}
}

func TestLookupNodeImage_UnknownVersionAndZero(t *testing.T) {
	if _, ok := LookupNodeImage(sampleNodeImages(), 19); ok {
		t.Fatalf("unmapped version must not resolve")
	}
	if _, ok := LookupNodeImage(sampleNodeImages(), 0); ok {
		t.Fatalf("unset version (0) must not resolve")
	}
}
