package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

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
		Build: PhaseSpec{
			Image:   "node:20",
			Command: []string{"yarn", "build"},
			Env:     map[string]string{"NEXT_PUBLIC_API": "https://api", "NEXT_PUBLIC_ENV": "uat"},
		},
		OutputDir: "dist",
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

func TestBuildSpecHash_ChangesWhenNodeVersionChanges(t *testing.T) {
	// nodeVersion is a user-provided spec field, so changing the MAJOR (18 -> 24)
	// changes the toolchain and must change the hash (immediate SpecChange rebuild).
	a := sampleBuildSpec()
	a.NodeVersion = 18
	b := sampleBuildSpec()
	b.NodeVersion = 24
	if a.Hash() == b.Hash() {
		t.Fatalf("changing nodeVersion must change the hash")
	}
}

func TestBuildSpecHash_ZeroNodeVersionOmittedFromPayload(t *testing.T) {
	// Backward compat: an unset nodeVersion (0) must NOT appear in the hashed
	// payload, so apps deployed before the field keep their stored hash across an
	// operator upgrade (no spurious SpecStale). Same package => normalized() is
	// reachable.
	data, _ := json.Marshal(sampleBuildSpec().normalized())
	if strings.Contains(string(data), "nodeVersion") {
		t.Fatalf("unset nodeVersion must be omitted from the hashed payload, got: %s", data)
	}
}

func TestBuildSpecHash_NilAndEmptyCollectionsHashEqual(t *testing.T) {
	// CRD round-tripping normalizes omitted vs empty collections; nil and empty
	// must hash identically or staleness flip-flops forever.
	a := sampleBuildSpec()
	a.Build.Env = nil
	a.SecretRefs = nil
	a.Build.Command = nil
	b := sampleBuildSpec()
	b.Build.Env = map[string]string{}
	b.SecretRefs = []string{}
	b.Build.Command = []string{}
	if a.Hash() != b.Hash() {
		t.Fatalf("nil and empty collections must hash equally (got %s vs %s)", a.Hash(), b.Hash())
	}
}

func TestBuildSpecHash_ChangesWhenSetupEnvChanges(t *testing.T) {
	a := sampleBuildSpec()
	b := sampleBuildSpec()
	b.Setup.Env = map[string]string{"CI": "1"}
	if a.Hash() == b.Hash() {
		t.Fatalf("changing setup.env must change the hash")
	}
}

func TestBuildSpecHash_ChangesWhenFetchEnvChanges(t *testing.T) {
	a := sampleBuildSpec()
	b := sampleBuildSpec()
	b.Fetch.Env = map[string]string{"REGION": "eu"}
	if a.Hash() == b.Hash() {
		t.Fatalf("changing fetch.env must change the hash")
	}
}

func TestBuildSpecHash_ChangesWhenBuildEnvChanges(t *testing.T) {
	a := sampleBuildSpec()
	b := sampleBuildSpec()
	b.Build.Env = map[string]string{"NEXT_PUBLIC_API": "https://other"}
	if a.Hash() == b.Hash() {
		t.Fatalf("changing build.env must change the hash")
	}
}

func TestBuildSpecHash_ChangesWhenBuildEnvMapChanges(t *testing.T) {
	// envMap folds into the same per-phase Env map as the array env, so a change
	// to an envMap entry alters the merged env and must change the hash.
	a := sampleBuildSpec()
	b := sampleBuildSpec()
	b.Build.Env = map[string]string{"NEXT_PUBLIC_API": "https://api", "NEXT_PUBLIC_ENV": "uat", "FEATURE_FLAG": "on"}
	if a.Hash() == b.Hash() {
		t.Fatalf("changing a build.envMap entry must change the hash")
	}
}

func TestBuildSpecHash_UnchangedWhenLiteralMovesBetweenEnvAndEnvMap(t *testing.T) {
	// env and envMap merge into one per-phase Env map before hashing, so moving a
	// literal KEY=v from the array env to envMap (or vice versa) yields the same
	// merged env — same effective build environment, same artifact, same hash.
	// Both branches below represent the SAME app, so both must hash identically.
	viaArray := sampleBuildSpec()
	viaArray.Build.Env = map[string]string{"NEXT_PUBLIC_API": "https://api", "NEXT_PUBLIC_ENV": "uat", "TOKEN": "abc"}
	viaMap := sampleBuildSpec()
	viaMap.Build.Env = map[string]string{"NEXT_PUBLIC_API": "https://api", "NEXT_PUBLIC_ENV": "uat", "TOKEN": "abc"}
	if viaArray.Hash() != viaMap.Hash() {
		t.Fatalf("moving a literal between env and envMap must not change the hash")
	}
}

func TestBuildSpecHash_ChangesWhenOutputDirChanges(t *testing.T) {
	a := sampleBuildSpec()
	b := sampleBuildSpec()
	b.OutputDir = "out"
	if a.Hash() == b.Hash() {
		t.Fatalf("changing outputDir must change the hash")
	}
}

func TestBuildSpecHash_UnchangedWhenSetupHasNoSkip(t *testing.T) {
	// Byte-for-byte backward compat: an app WITHOUT setup.skip must hash exactly
	// as it did before the Skip field was added, so existing apps keep their
	// stored lastBuiltSpecHash across an operator upgrade (no spurious SpecStale).
	// The pinned value was captured from the pre-Skip code on sampleBuildSpec().
	const preSkipHash = "f273885734df98083ea4b45f0fa33c0a88edb35058f9e3602b3ed3cba7e1ad87"
	if got := sampleBuildSpec().Hash(); got != preSkipHash {
		t.Fatalf("setup without skip must keep its pre-Skip hash %s, got %s", preSkipHash, got)
	}
}

func TestBuildSpecHash_ChangesWhenSetupSkipFlips(t *testing.T) {
	// Flipping setup.skip changes the spec-as-written, so it must change the hash.
	a := sampleBuildSpec()
	b := sampleBuildSpec()
	b.Setup = PhaseSpec{Skip: true}
	if a.Hash() == b.Hash() {
		t.Fatalf("flipping setup.skip must change the hash")
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
