package domain

// BuildDecision is the outcome of evaluating whether to create a build Job.
type BuildDecision int

const (
	// NoBuild: the requested token has already been processed; nothing to do.
	NoBuild BuildDecision = iota
	// StartBuild: a new rebuild request exists and no build is active.
	StartBuild
	// DeferBuild: a new rebuild request exists but a build is already active;
	// wait for it to finish (the annotation persists, so the Job-completion
	// reconcile will re-evaluate and start the next build).
	DeferBuild
)

// DecideBuild is the single chokepoint for "should the operator create a build
// Job now?". requestedToken is the current rebuild-annotation value;
// lastProcessedToken is status.lastProcessedRebuild (recorded at job creation);
// buildActive is whether a build Job for this app is still running. Enforcing a
// single active build per app here is what makes the operator the sole creator
// and removes the manual-vs-scheduled write race on the shared output PVC.
func DecideBuild(requestedToken, lastProcessedToken string, buildActive bool) BuildDecision {
	if requestedToken == lastProcessedToken {
		return NoBuild
	}
	if buildActive {
		return DeferBuild
	}
	return StartBuild
}
