package domain

import "testing"

// Cleanup actions (cache prune / release prune) trigger ONLY from a fresh
// requested-at annotation token, compared by inequality against the per-action
// processed marker (status.cleanup.<action>.RequestedAt). A cleanup serializes
// against any active build AND any active cleanup Job (build wins ties): if
// either is active, the request stays Pending until the next reconcile.

func TestDecideCleanup_StartsWhenTokenChangedAndIdle(t *testing.T) {
	if got := DecideCleanup("t2", "t1", false, false); got != StartCleanup {
		t.Fatalf("fresh token while idle must StartCleanup, got %v", got)
	}
}

func TestDecideCleanup_NoopWhenTokenAlreadyProcessed(t *testing.T) {
	if got := DecideCleanup("t1", "t1", false, false); got != NoCleanup {
		t.Fatalf("already-processed token must be NoCleanup, got %v", got)
	}
}

func TestDecideCleanup_NoopWhenNoRequest(t *testing.T) {
	if got := DecideCleanup("", "", false, false); got != NoCleanup {
		t.Fatalf("empty request must be NoCleanup, got %v", got)
	}
}

func TestDecideCleanup_NoopWhenRequestEmptyEvenIfMarkerSet(t *testing.T) {
	// A cleared annotation (empty) must never re-fire a previously processed action.
	if got := DecideCleanup("", "t1", false, false); got != NoCleanup {
		t.Fatalf("empty request must be NoCleanup regardless of marker, got %v", got)
	}
}

func TestDecideCleanup_WaitsWhenBuildActive(t *testing.T) {
	// Build wins ties: a fresh cleanup request defers to an in-flight build.
	if got := DecideCleanup("t2", "t1", true, false); got != WaitCleanup {
		t.Fatalf("fresh token with active build must WaitCleanup, got %v", got)
	}
}

func TestDecideCleanup_WaitsWhenCleanupActive(t *testing.T) {
	// Serialize against another cleanup Job (cache vs releases, or a re-fire).
	if got := DecideCleanup("t2", "t1", false, true); got != WaitCleanup {
		t.Fatalf("fresh token with active cleanup must WaitCleanup, got %v", got)
	}
}

func TestDecideCleanup_WaitsWhenBothActive(t *testing.T) {
	if got := DecideCleanup("t2", "t1", true, true); got != WaitCleanup {
		t.Fatalf("fresh token with both active must WaitCleanup, got %v", got)
	}
}

// Activity never matters once the token is already processed: a finished action
// must not re-trigger just because a build/cleanup happens to be running.
func TestDecideCleanup_NoopWhenProcessedEvenIfActive(t *testing.T) {
	if got := DecideCleanup("t1", "t1", true, true); got != NoCleanup {
		t.Fatalf("processed token must be NoCleanup even when active, got %v", got)
	}
}
