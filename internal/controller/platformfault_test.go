package controller

import (
	"testing"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
)

// ConfigError is operator/chart misconfiguration — always the platform's fault.
func TestIsPlatformFault_ConfigErrorIsPlatformFault(t *testing.T) {
	if !isPlatformFault(bakerv1alpha1.ReasonConfigError, "") {
		t.Fatal("isPlatformFault(ConfigError) = false, want true")
	}
}

// Spec-validation rejections are user errors (their spec), never platform faults.
func TestIsPlatformFault_SpecValidationReasonsAreUserFault(t *testing.T) {
	for _, reason := range []string{
		bakerv1alpha1.ReasonInvalidSpec,
		bakerv1alpha1.ReasonImageNotAllowed,
		bakerv1alpha1.ReasonUnknownNodeVersion,
		bakerv1alpha1.ReasonInvalidStorage,
		bakerv1alpha1.ReasonInvalidStorageClass,
		bakerv1alpha1.ReasonMissingTLSSecret,
	} {
		if isPlatformFault(reason, "") {
			t.Errorf("isPlatformFault(%s) = true, want false", reason)
		}
	}
}

// BuildFailed in a user-owned pipeline step (their repo, commands, build) is
// a user error.
func TestIsPlatformFault_BuildFailedInUserStepsIsUserFault(t *testing.T) {
	for _, step := range []string{
		bakerv1alpha1.StepClone,
		bakerv1alpha1.StepSetup,
		bakerv1alpha1.StepFetch,
		bakerv1alpha1.StepBuild,
	} {
		if isPlatformFault(bakerv1alpha1.ReasonBuildFailed, step) {
			t.Errorf("isPlatformFault(BuildFailed, %s) = true, want false", step)
		}
	}
}

// The copier is the platform's own image — its failure is a platform fault.
func TestIsPlatformFault_BuildFailedInCopierIsPlatformFault(t *testing.T) {
	if !isPlatformFault(bakerv1alpha1.ReasonBuildFailed, bakerv1alpha1.StepCopier) {
		t.Fatal("isPlatformFault(BuildFailed, copier) = false, want true")
	}
}

// The synthetic release step is the operator's own pointer flip — platform fault.
func TestIsPlatformFault_BuildFailedInReleaseIsPlatformFault(t *testing.T) {
	if !isPlatformFault(bakerv1alpha1.ReasonBuildFailed, bakerv1alpha1.StepRelease) {
		t.Fatal("isPlatformFault(BuildFailed, release) = false, want true")
	}
}

// An unattributed BuildFailed (empty failedStep, e.g. the shim-install init
// container reaped before step attribution) is a platform fault: it failed in
// platform-owned machinery, or the attribution itself is a platform bug.
func TestIsPlatformFault_BuildFailedUnattributedIsPlatformFault(t *testing.T) {
	if !isPlatformFault(bakerv1alpha1.ReasonBuildFailed, "") {
		t.Fatal(`isPlatformFault(BuildFailed, "") = false, want true`)
	}
}

// OOMKilled means the user's build exceeded THEIR memory limit — user fault.
func TestIsPlatformFault_OOMKilledIsUserFault(t *testing.T) {
	if isPlatformFault(bakerv1alpha1.ReasonOOMKilled, bakerv1alpha1.StepBuild) {
		t.Fatal("isPlatformFault(OOMKilled) = true, want false")
	}
}
