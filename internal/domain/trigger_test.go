package domain

import "testing"

// Build triggers ONLY from the rebuild annotation token (manual UI rebuild +
// scheduled clock tick), compared by inequality against status.lastProcessed.
// Spec changes never trigger a build (they only set staleness). A new token
// arriving while a build is active is deferred; the annotation persists on the
// object, so the next reconcile (on Job completion) re-evaluates and starts it.

func TestDecideBuild_StartsWhenTokenChangedAndIdle(t *testing.T) {
	if got := DecideBuild("t2", "t1", false); got != StartBuild {
		t.Fatalf("new token while idle must StartBuild, got %v", got)
	}
}

func TestDecideBuild_NoneWhenTokenAlreadyProcessed(t *testing.T) {
	if got := DecideBuild("t1", "t1", false); got != NoBuild {
		t.Fatalf("already-processed token must be NoBuild, got %v", got)
	}
}

func TestDecideBuild_DefersWhenTokenChangedButBuildActive(t *testing.T) {
	if got := DecideBuild("t2", "t1", true); got != DeferBuild {
		t.Fatalf("new token with an active build must DeferBuild (single active build), got %v", got)
	}
}
