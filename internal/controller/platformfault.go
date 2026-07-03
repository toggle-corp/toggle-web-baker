package controller

import (
	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
)

// isPlatformFault reports whether a terminal failure is the platform's fault
// as opposed to a user error (their build, spec, repo, or memory limit).
// Must be called AFTER OOM promotion so reason is final.
func isPlatformFault(reason, failedStep string) bool {
	if reason == bakerv1alpha1.ReasonConfigError {
		return true
	}
	if reason != bakerv1alpha1.ReasonBuildFailed {
		return false
	}
	switch failedStep {
	case bakerv1alpha1.StepClone, bakerv1alpha1.StepSetup, bakerv1alpha1.StepFetch, bakerv1alpha1.StepBuild:
		// User-owned pipeline steps: their repo, their commands, their build.
		return false
	}
	// copier, release, or "" (unattributed, e.g. the shim-install init container
	// reaped before attribution): platform-owned machinery.
	return true
}
