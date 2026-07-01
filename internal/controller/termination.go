package controller

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
)

// detectTermination reports how the failed step's container terminated: its
// reason (e.g. "OOMKilled"), exit code, the memory limit it ran with, and finish
// time. failedStepName is the step failedStep already selected, so the
// attribution is single-sourced with status.build.failedStep rather than
// re-deriving "which step failed" from a second signal. The memory limit is read
// from the pod SPEC (not the app spec) so it reflects the build that actually
// ran, immune to a mid-build spec edit. Returns nil for a nil pod, an empty or
// synthetic (release) step, or a container that is absent / not terminated.
//
// It is called once, in observeBuild's terminal failure branch, so the result is
// persisted on status.build and survives the pod being evicted/reaped later.
func detectTermination(pod *corev1.Pod, failedStepName string) *bakerv1alpha1.BuildTermination {
	if pod == nil || failedStepName == "" || failedStepName == bakerv1alpha1.StepRelease {
		return nil // no pod, no failed step, or the synthetic release step (no container)
	}
	statuses := pod.Status.InitContainerStatuses
	if failedStepName == bakerv1alpha1.StepCopier {
		statuses = pod.Status.ContainerStatuses // copier is the main container
	}
	cs := findContainerStatus(statuses, failedStepName)
	if cs == nil || cs.State.Terminated == nil {
		return nil
	}
	t := cs.State.Terminated
	out := &bakerv1alpha1.BuildTermination{
		Reason:      t.Reason,
		Container:   failedStepName,
		ExitCode:    t.ExitCode,
		MemoryLimit: containerMemoryLimit(pod, failedStepName),
	}
	if !t.FinishedAt.IsZero() {
		out.FinishedAt = t.FinishedAt.DeepCopy()
	}
	return out
}

// stampStepMessage sets msg on the named step in place (no-op if absent), used
// to annotate the failed step with how its container terminated.
func stampStepMessage(steps []bakerv1alpha1.BuildStep, name, msg string) {
	for i := range steps {
		if steps[i].Name == name {
			steps[i].Message = msg
			return
		}
	}
}

// terminationStepMessage is the short annotation stamped on the failed
// BuildStep (surfaced in the flowstrip tooltip). It folds in the memory limit
// for the common OOM case ("OOMKilled (limit 256Mi)"), or just the reason when
// no limit is known. Empty when there is no reason to report.
func terminationStepMessage(t *bakerv1alpha1.BuildTermination) string {
	if t == nil || t.Reason == "" {
		return ""
	}
	if t.MemoryLimit != "" {
		return fmt.Sprintf("%s (limit %s)", t.Reason, t.MemoryLimit)
	}
	return t.Reason
}

// oomConditionMessage is the human message set on the BuildSucceeded/Degraded
// conditions when a build was OOMKilled, naming the step and the limit it hit so
// `kubectl describe` points straight at the fix.
func oomConditionMessage(t *bakerv1alpha1.BuildTermination) string {
	if t.MemoryLimit != "" {
		return fmt.Sprintf("the %s step exceeded its %s memory limit", t.Container, t.MemoryLimit)
	}
	return fmt.Sprintf("the %s step was OOMKilled", t.Container)
}

// containerMemoryLimit returns the memory limit configured on the named pod-spec
// container as its Kubernetes quantity string (e.g. "256Mi"), or "" when the
// container is absent or has no memory limit. It searches init containers first
// (clone/setup/fetch/build) then the main containers (copier).
func containerMemoryLimit(pod *corev1.Pod, name string) string {
	for _, list := range [][]corev1.Container{pod.Spec.InitContainers, pod.Spec.Containers} {
		for i := range list {
			if list[i].Name != name {
				continue
			}
			if q, ok := list[i].Resources.Limits[corev1.ResourceMemory]; ok {
				return q.String()
			}
			return ""
		}
	}
	return ""
}
