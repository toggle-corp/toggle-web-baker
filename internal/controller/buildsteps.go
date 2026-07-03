package controller

import (
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
)

// phaseConfigured reports whether an optional phase (setup/fetch) is in play,
// mirroring phaseImages' applicability convention but also counting a Command
// (an app can supply a command on the default clone image without an Image).
func phaseConfigured(p bakerv1alpha1.PhaseSpec) bool {
	return p.Image != "" || len(p.Command) > 0
}

// applicableSteps returns the ordered pipeline step names that actually apply to
// the app: clone/build/copier/release are always present; setup and fetch appear
// only when the app configures that phase (Image or Command). release is the
// SYNTHETIC step (the operator's release-pointer flip after copier succeeds).
func applicableSteps(app *bakerv1alpha1.FrontendApp) []string {
	steps := []string{bakerv1alpha1.StepClone}
	if phaseConfigured(app.Spec.Pipeline.Phases.Setup) {
		steps = append(steps, bakerv1alpha1.StepSetup)
	}
	if phaseConfigured(app.Spec.Pipeline.Phases.Fetch.PhaseSpec) {
		steps = append(steps, bakerv1alpha1.StepFetch)
	}
	steps = append(steps, bakerv1alpha1.StepBuild, bakerv1alpha1.StepCopier, bakerv1alpha1.StepRelease)
	return steps
}

// containerStepStatus maps one container's state to a StepStatus: an absent or
// Waiting container is Pending (not yet reached); Running is Running; a clean
// (exit 0) Terminated is Succeeded; a non-zero exit is Failed.
func containerStepStatus(cs *corev1.ContainerStatus) bakerv1alpha1.StepStatus {
	if cs == nil {
		return bakerv1alpha1.StepStatusPending
	}
	switch {
	case cs.State.Terminated != nil:
		if cs.State.Terminated.ExitCode == 0 {
			return bakerv1alpha1.StepStatusSucceeded
		}
		return bakerv1alpha1.StepStatusFailed
	case cs.State.Running != nil:
		return bakerv1alpha1.StepStatusRunning
	default: // Waiting or no state set
		return bakerv1alpha1.StepStatusPending
	}
}

// allSucceeded marks every applicable step Succeeded. It is the fallback used
// when a SUCCEEDED build's pod is already gone (TTL-reaped or evicted) before
// the terminal observe: the per-step states can't be read, but a successful
// build means every step passed, so the timeline must not contradict the result
// by showing Pending steps.
func allSucceeded(applicable []string) []bakerv1alpha1.BuildStep {
	out := make([]bakerv1alpha1.BuildStep, 0, len(applicable))
	for _, name := range applicable {
		out = append(out, bakerv1alpha1.BuildStep{Name: name, Status: bakerv1alpha1.StepStatusSucceeded})
	}
	return out
}

// findContainerStatus returns the named container status, or nil if absent.
func findContainerStatus(statuses []corev1.ContainerStatus, name string) *corev1.ContainerStatus {
	for i := range statuses {
		if statuses[i].Name == name {
			return &statuses[i]
		}
	}
	return nil
}

// classifyTrigger derives why a build ran from the rebuild annotations: a
// non-empty "by" user means the manual-rebuild UI requested it (Manual);
// otherwise it is the clock tick (Scheduled). SpecChange is reserved and never
// emitted (spec edits are detect-only and never trigger a build).
func classifyTrigger(app *bakerv1alpha1.FrontendApp) bakerv1alpha1.BuildTrigger {
	if app.Annotations[bakerv1alpha1.RebuildByAnnotation] != "" {
		return bakerv1alpha1.BuildTriggerManual
	}
	return bakerv1alpha1.BuildTriggerScheduled
}

// appendBuildHistory records rec into the newest-first history ring buffer. If
// an entry with the same JobName already exists it is REPLACED in place (so
// repeated reconciles of the same build don't duplicate it, and a build observed
// Running-then-terminal updates its single slot); otherwise rec is prepended.
// The result is capped to max entries (oldest dropped).
func appendBuildHistory(history []bakerv1alpha1.BuildStatus, rec bakerv1alpha1.BuildStatus, max int) []bakerv1alpha1.BuildStatus {
	for i := range history {
		if history[i].JobName == rec.JobName {
			out := append([]bakerv1alpha1.BuildStatus(nil), history...)
			out[i] = rec
			return out
		}
	}
	out := append([]bakerv1alpha1.BuildStatus{rec}, history...)
	if max >= 0 && len(out) > max {
		out = out[:max]
	}
	return out
}

// failedStep returns the name of the first step that ended Failed or Aborted,
// or "" when the build has no failing step (the value surfaced as
// BuildStatus.FailedStep).
func failedStep(steps []bakerv1alpha1.BuildStep) string {
	for _, s := range steps {
		if s.Status == bakerv1alpha1.StepStatusFailed || s.Status == bakerv1alpha1.StepStatusAborted {
			return s.Name
		}
	}
	return ""
}

// deriveBuildSteps maps each applicable step to a StepStatus from the build
// pod's container states. clone/setup/fetch/build are init containers; copier is
// the main container; release is SYNTHETIC — Succeeded iff releaseDone (copier
// succeeded AND the release pointer flipped), otherwise Pending. A step whose
// container never ran is left Pending (we never invent a status for a container
// the kubelet didn't reach). A nil/absent pod yields all-Pending. For shim-
// wrapped steps (setup/fetch/build) the terminated container's message carries
// the phase's peak memory, harvested into PeakMemoryBytes. Every real container
// also records its start/finish timestamps (per-step runtime) and — once it has
// actually started (Running or Terminated) — the memory limit it ran with
// (peak-vs-allocated), both read from the pod. A Pending step carries no limit:
// "allocated" describes a run, not a plan.
func deriveBuildSteps(applicable []string, pod *corev1.Pod, releaseDone bool) []bakerv1alpha1.BuildStep {
	out := make([]bakerv1alpha1.BuildStep, 0, len(applicable))
	for _, name := range applicable {
		step := bakerv1alpha1.BuildStep{Name: name, Status: bakerv1alpha1.StepStatusPending}
		switch name {
		case bakerv1alpha1.StepRelease:
			if releaseDone {
				step.Status = bakerv1alpha1.StepStatusSucceeded
			}
		case bakerv1alpha1.StepCopier:
			if pod != nil {
				cs := findContainerStatus(pod.Status.ContainerStatuses, name)
				step.Status = containerStepStatus(cs)
				stampStepTimes(&step, cs)
				if containerRan(cs) {
					step.MemoryLimit = containerMemoryLimit(pod, name)
				}
			}
		default: // clone / setup / fetch / build are init containers
			if pod != nil {
				cs := findContainerStatus(pod.Status.InitContainerStatuses, name)
				step.Status = containerStepStatus(cs)
				stampStepTimes(&step, cs)
				if containerRan(cs) {
					step.MemoryLimit = containerMemoryLimit(pod, name)
				}
				if cs != nil && cs.State.Terminated != nil {
					step.PeakMemoryBytes = parsePeakMemory(cs.State.Terminated.Message)
				}
			}
		}
		out = append(out, step)
	}
	return out
}

// containerRan reports whether the container actually started, i.e. its status
// exists and is Running or Terminated. Waiting/absent means the kubelet never
// launched it, so callers must not stamp run-derived facts (MemoryLimit) onto a
// still-Pending step.
func containerRan(cs *corev1.ContainerStatus) bool {
	return cs != nil && (cs.State.Running != nil || cs.State.Terminated != nil)
}

// stampStepTimes copies the container's start/finish timestamps onto the step:
// a Running container has only StartedAt; a Terminated one has both. Zero-valued
// kubelet timestamps are skipped so the status never carries a bogus epoch.
func stampStepTimes(step *bakerv1alpha1.BuildStep, cs *corev1.ContainerStatus) {
	if cs == nil {
		return
	}
	switch {
	case cs.State.Terminated != nil:
		if !cs.State.Terminated.StartedAt.IsZero() {
			step.StartedAt = cs.State.Terminated.StartedAt.DeepCopy()
		}
		if !cs.State.Terminated.FinishedAt.IsZero() {
			step.FinishedAt = cs.State.Terminated.FinishedAt.DeepCopy()
		}
	case cs.State.Running != nil:
		if !cs.State.Running.StartedAt.IsZero() {
			step.StartedAt = cs.State.Running.StartedAt.DeepCopy()
		}
	}
}

// peakMemoryKey is the termination-message key the shim wrapper emits (one
// `peakMemoryBytes=<n>` line). See images/shim.
const peakMemoryKey = "peakMemoryBytes="

// parsePeakMemory extracts the shim's peak-memory line from a container
// termination message. Defensive: absent key, garbage, or a negative value
// read as 0 (unmeasured) — the measurement is best-effort and must never fail
// a status write.
func parsePeakMemory(msg string) int64 {
	for _, line := range strings.Split(msg, "\n") {
		val, found := strings.CutPrefix(strings.TrimSpace(line), peakMemoryKey)
		if !found {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
		if err != nil || n < 0 {
			return 0
		}
		return n
	}
	return 0
}
