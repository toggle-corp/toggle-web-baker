package domain

// CleanupDecision is the outcome of evaluating whether to start an on-demand
// cleanup Job (cache prune or release prune) right now.
type CleanupDecision int

const (
	// NoCleanup: the requested token has already been processed (or there is no
	// request); nothing to do.
	NoCleanup CleanupDecision = iota
	// StartCleanup: a fresh cleanup request exists and neither a build nor another
	// cleanup Job is active.
	StartCleanup
	// WaitCleanup: a fresh cleanup request exists but a build OR another cleanup
	// Job is active; stay Pending. The annotation persists on the object, so the
	// next reconcile (on the blocking Job's completion) re-evaluates and starts it.
	WaitCleanup
)

// DecideCleanup is the single chokepoint for "should the operator start this
// cleanup action now?". requestedToken is the action's requested-at annotation
// value; processedToken is the per-action processed marker
// (status.cleanup.<action>.RequestedAt). buildActive reports whether a build Job
// for the app is still running; cleanupActive reports whether ANY cleanup Job
// for the app is still running (cache and releases serialize against each other,
// and against a re-fire of the same action).
//
// Serialization rules: a cleanup never runs concurrently with a build, and never
// concurrently with another cleanup. Build wins ties — if a build is active the
// cleanup waits (Pending) while the build proceeds. A cleared/empty request can
// never re-fire a previously processed action.
func DecideCleanup(requestedToken, processedToken string, buildActive, cleanupActive bool) CleanupDecision {
	if requestedToken == "" || requestedToken == processedToken {
		return NoCleanup
	}
	if buildActive || cleanupActive {
		return WaitCleanup
	}
	return StartCleanup
}
