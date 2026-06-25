package domain

import "testing"

// The operator hashes the build-relevant subset of the spec and stores it in
// status.lastBuiltSpecHash (on copier success). status.specStale is true when
// the current build-relevant spec differs from the last successfully deployed
// one. Non-build-relevant fields (thresholds, ingress, keepReleases) are never
// part of BuildSpec, so they can never set staleness.

func sampleBuildSpec() BuildSpec {
	return BuildSpec{
		Repo:           "https://github.com/mapswipe/mapswipe",
		Ref:            "deploy-prod",
		PackageManager: "yarn",
		Setup:          PhaseSpec{Image: "node:20", Command: []string{"yarn", "install"}},
		Build:          PhaseSpec{Image: "node:20", Command: []string{"yarn", "build"}},
		BuildArgs:      map[string]string{"NEXT_PUBLIC_API": "https://api", "NEXT_PUBLIC_ENV": "uat"},
	}
}

func TestBuildSpecHash_StableForSameContent(t *testing.T) {
	// Two independently-constructed specs with identical content must hash the
	// same (assigned to vars so staticcheck SA4000 doesn't see one expression).
	first := sampleBuildSpec().Hash()
	second := sampleBuildSpec().Hash()
	if first != second {
		t.Fatalf("identical build-relevant spec must hash identically")
	}
}

func TestBuildSpecHash_ChangesWhenRefChanges(t *testing.T) {
	a := sampleBuildSpec()
	b := sampleBuildSpec()
	b.Ref = "main"
	if a.Hash() == b.Hash() {
		t.Fatalf("changing ref must change the hash")
	}
}

func TestBuildSpecHash_NilAndEmptyCollectionsHashEqual(t *testing.T) {
	// CRD round-tripping normalizes omitted vs empty collections; nil and empty
	// must hash identically or staleness flip-flops forever.
	a := sampleBuildSpec()
	a.BuildArgs = nil
	a.SecretRefs = nil
	a.Build.Command = nil
	b := sampleBuildSpec()
	b.BuildArgs = map[string]string{}
	b.SecretRefs = []string{}
	b.Build.Command = []string{}
	if a.Hash() != b.Hash() {
		t.Fatalf("nil and empty collections must hash equally (got %s vs %s)", a.Hash(), b.Hash())
	}
}

func TestIsStale_FalseWhenCurrentMatchesLastDeployed(t *testing.T) {
	cur := sampleBuildSpec()
	if IsStale(cur, cur.Hash()) {
		t.Fatalf("spec matching the last deployed hash must not be stale")
	}
}

func TestIsStale_TrueWhenSpecChanged(t *testing.T) {
	deployed := sampleBuildSpec().Hash()
	cur := sampleBuildSpec()
	cur.Build.Command = []string{"yarn", "generate:type", "&&", "yarn", "build"}
	if !IsStale(cur, deployed) {
		t.Fatalf("changed build-relevant spec must be stale")
	}
}

func TestIsStale_TrueWhenNeverDeployed(t *testing.T) {
	if !IsStale(sampleBuildSpec(), "") {
		t.Fatalf("never-deployed app (empty lastBuiltSpecHash) must read as stale")
	}
}
