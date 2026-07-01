package controller

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
)

// detectTermination inspects a failed build pod for the container whose abnormal
// termination ended the build and, if found, returns a BuildTermination
// capturing its reason (e.g. "OOMKilled"), exit code, the memory limit it ran
// with, and finish time. It walks the applicable steps in flow order and returns
// the FIRST container that terminated with a non-zero exit — the same "first
// failing step" the timeline's failedStep reports. The memory limit is read from
// the pod SPEC (not the app spec) so it reflects the build that actually ran,
// immune to a mid-build spec edit. release is synthetic (no container) and
// skipped. Returns nil for a nil pod or when nothing terminated abnormally.
//
// It is called once, in observeBuild's terminal failure branch, so the result is
// persisted on status.build and survives the pod being evicted/reaped later.
func detectTermination(pod *corev1.Pod, applicable []string) *bakerv1alpha1.BuildTermination {
	if pod == nil {
		return nil
	}
	for _, name := range applicable {
		if name == bakerv1alpha1.StepRelease {
			continue // synthetic step: the operator's pointer flip, not a container
		}
		statuses := pod.Status.InitContainerStatuses
		if name == bakerv1alpha1.StepCopier {
			statuses = pod.Status.ContainerStatuses
		}
		cs := findContainerStatus(statuses, name)
		if cs == nil || cs.State.Terminated == nil || cs.State.Terminated.ExitCode == 0 {
			continue
		}
		t := cs.State.Terminated
		out := &bakerv1alpha1.BuildTermination{
			Reason:      t.Reason,
			Container:   name,
			ExitCode:    t.ExitCode,
			MemoryLimit: containerMemoryLimit(pod, name),
		}
		if !t.FinishedAt.IsZero() {
			out.FinishedAt = t.FinishedAt.DeepCopy()
		}
		return out
	}
	return nil
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
