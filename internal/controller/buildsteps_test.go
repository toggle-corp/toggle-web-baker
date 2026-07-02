package controller

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
)

// allSucceeded marks every applicable step Succeeded — the fallback for a
// succeeded build whose pod was already reaped.
func TestAllSucceeded(t *testing.T) {
	steps := allSucceeded([]string{"clone", "build", "copier", "release"})
	if len(steps) != 4 {
		t.Fatalf("want 4 steps, got %d", len(steps))
	}
	for _, s := range steps {
		if s.Status != bakerv1alpha1.StepStatusSucceeded {
			t.Errorf("step %q = %s, want Succeeded", s.Name, s.Status)
		}
	}
}

// isBuildPod is the watch predicate: only build-role pods pass.
func TestIsBuildPod(t *testing.T) {
	build := &corev1.Pod{}
	build.Labels = map[string]string{"baker.toggle-corp.com/role": "build"}
	if !isBuildPod(build) {
		t.Error("build-role pod should pass the predicate")
	}
	other := &corev1.Pod{}
	other.Labels = map[string]string{"app": "something-else"}
	if isBuildPod(other) {
		t.Error("non-build pod should be filtered out")
	}
	if isBuildPod(&corev1.Pod{}) {
		t.Error("unlabeled pod should be filtered out")
	}
}

// applicableSteps: clone/build/copier/release always present; setup/fetch only
// when the app configures an Image or Command for that phase. Ordered.
func TestApplicableSteps(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*bakerv1alpha1.FrontendApp)
		want []string
	}{
		{
			name: "minimal: no setup/fetch",
			mut:  func(*bakerv1alpha1.FrontendApp) {},
			want: []string{bakerv1alpha1.StepClone, bakerv1alpha1.StepBuild, bakerv1alpha1.StepCopier, bakerv1alpha1.StepRelease},
		},
		{
			name: "setup via image only",
			mut:  func(a *bakerv1alpha1.FrontendApp) { a.Spec.Pipeline.Phases.Setup.Image = "ghcr.io/toggle-corp/x" },
			want: []string{bakerv1alpha1.StepClone, bakerv1alpha1.StepSetup, bakerv1alpha1.StepBuild, bakerv1alpha1.StepCopier, bakerv1alpha1.StepRelease},
		},
		{
			name: "fetch via command only",
			mut:  func(a *bakerv1alpha1.FrontendApp) { a.Spec.Pipeline.Phases.Fetch.Command = []string{"sh", "-c", "x"} },
			want: []string{bakerv1alpha1.StepClone, bakerv1alpha1.StepFetch, bakerv1alpha1.StepBuild, bakerv1alpha1.StepCopier, bakerv1alpha1.StepRelease},
		},
		{
			name: "both setup and fetch",
			mut: func(a *bakerv1alpha1.FrontendApp) {
				a.Spec.Pipeline.Phases.Setup.Command = []string{"true"}
				a.Spec.Pipeline.Phases.Fetch.Image = "ghcr.io/toggle-corp/y"
			},
			want: []string{bakerv1alpha1.StepClone, bakerv1alpha1.StepSetup, bakerv1alpha1.StepFetch, bakerv1alpha1.StepBuild, bakerv1alpha1.StepCopier, bakerv1alpha1.StepRelease},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := baseApp()
			tc.mut(app)
			got := applicableSteps(app)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("applicableSteps = %v, want %v", got, tc.want)
			}
		})
	}
}

// classifyTrigger: Manual iff the "by" annotation names a non-empty user, else
// Scheduled. SpecChange is reserved and never emitted.
func TestClassifyTrigger(t *testing.T) {
	t.Run("no by annotation => Scheduled", func(t *testing.T) {
		app := baseApp()
		if got := classifyTrigger(app); got != bakerv1alpha1.BuildTriggerScheduled {
			t.Fatalf("got %s, want Scheduled", got)
		}
	})
	t.Run("empty by annotation => Scheduled", func(t *testing.T) {
		app := baseApp()
		app.Annotations = map[string]string{bakerv1alpha1.RebuildByAnnotation: ""}
		if got := classifyTrigger(app); got != bakerv1alpha1.BuildTriggerScheduled {
			t.Fatalf("got %s, want Scheduled", got)
		}
	})
	t.Run("by annotation set => Manual", func(t *testing.T) {
		app := baseApp()
		app.Annotations = map[string]string{bakerv1alpha1.RebuildByAnnotation: "octocat"}
		if got := classifyTrigger(app); got != bakerv1alpha1.BuildTriggerManual {
			t.Fatalf("got %s, want Manual", got)
		}
	})
}

func bs(job string, result bakerv1alpha1.BuildResult) bakerv1alpha1.BuildStatus {
	return bakerv1alpha1.BuildStatus{JobName: job, Result: result}
}

func jobNames(h []bakerv1alpha1.BuildStatus) []string {
	out := make([]string, len(h))
	for i, e := range h {
		out[i] = e.JobName
	}
	return out
}

// appendBuildHistory prepends newest-first, dedups by JobName (replacing in
// place), and caps to max.
func TestAppendBuildHistory(t *testing.T) {
	t.Run("empty history", func(t *testing.T) {
		got := appendBuildHistory(nil, bs("a", bakerv1alpha1.BuildResultSucceeded), 5)
		if !slices.Equal(jobNames(got), []string{"a"}) {
			t.Fatalf("got %v", jobNames(got))
		}
	})

	t.Run("prepends newest-first", func(t *testing.T) {
		h := []bakerv1alpha1.BuildStatus{bs("a", bakerv1alpha1.BuildResultSucceeded)}
		got := appendBuildHistory(h, bs("b", bakerv1alpha1.BuildResultFailed), 5)
		if !slices.Equal(jobNames(got), []string{"b", "a"}) {
			t.Fatalf("got %v, want [b a]", jobNames(got))
		}
	})

	t.Run("dedups by JobName replacing in place", func(t *testing.T) {
		h := []bakerv1alpha1.BuildStatus{
			bs("b", bakerv1alpha1.BuildResultFailed),
			bs("a", bakerv1alpha1.BuildResultSucceeded),
		}
		// Re-observe "b" now Succeeded: must REPLACE in place, not duplicate or reorder.
		got := appendBuildHistory(h, bs("b", bakerv1alpha1.BuildResultSucceeded), 5)
		if !slices.Equal(jobNames(got), []string{"b", "a"}) {
			t.Fatalf("got %v, want [b a] (no dup)", jobNames(got))
		}
		if got[0].Result != bakerv1alpha1.BuildResultSucceeded {
			t.Fatalf("entry b not updated in place: %+v", got[0])
		}
	})

	t.Run("caps to max newest-first", func(t *testing.T) {
		var h []bakerv1alpha1.BuildStatus
		for _, n := range []string{"e", "d", "c", "b", "a"} {
			h = append(h, bs(n, bakerv1alpha1.BuildResultSucceeded))
		}
		got := appendBuildHistory(h, bs("f", bakerv1alpha1.BuildResultSucceeded), 5)
		if !slices.Equal(jobNames(got), []string{"f", "e", "d", "c", "b"}) {
			t.Fatalf("got %v, want [f e d c b] (cap 5, oldest dropped)", jobNames(got))
		}
	})
}

// failedStep returns the first Failed or Aborted step, else "".
func TestFailedStep(t *testing.T) {
	step := func(n string, s bakerv1alpha1.StepStatus) bakerv1alpha1.BuildStep {
		return bakerv1alpha1.BuildStep{Name: n, Status: s}
	}
	cases := []struct {
		name  string
		steps []bakerv1alpha1.BuildStep
		want  string
	}{
		{"none failed", []bakerv1alpha1.BuildStep{step("clone", bakerv1alpha1.StepStatusSucceeded), step("build", bakerv1alpha1.StepStatusRunning)}, ""},
		{"first failed", []bakerv1alpha1.BuildStep{step("clone", bakerv1alpha1.StepStatusSucceeded), step("build", bakerv1alpha1.StepStatusFailed), step("copier", bakerv1alpha1.StepStatusPending)}, "build"},
		{"aborted counts", []bakerv1alpha1.BuildStep{step("clone", bakerv1alpha1.StepStatusAborted)}, "clone"},
		{"empty", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := failedStep(tc.steps); got != tc.want {
				t.Fatalf("failedStep = %q, want %q", got, tc.want)
			}
		})
	}
}

// helpers for building container statuses
func waiting(name string) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{}}}
}
func running(name string) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
}
func term(name string, exit int32) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: exit}}}
}

func stepStatus(steps []bakerv1alpha1.BuildStep, name string) bakerv1alpha1.StepStatus {
	for _, s := range steps {
		if s.Name == name {
			return s.Status
		}
	}
	return ""
}

// deriveBuildSteps: nil pod => every applicable step Pending.
func TestDeriveBuildSteps_NilPod(t *testing.T) {
	app := baseApp()
	app.Spec.Pipeline.Phases.Setup.Command = []string{"true"}
	steps := deriveBuildSteps(applicableSteps(app), nil, false)
	for _, s := range steps {
		if s.Status != bakerv1alpha1.StepStatusPending {
			t.Fatalf("step %s = %s, want Pending (nil pod)", s.Name, s.Status)
		}
	}
}

// deriveBuildSteps: maps init container (clone/setup/fetch/build) + main
// container (copier) states to step statuses; release follows releaseDone.
func TestDeriveBuildSteps_MapsContainerStates(t *testing.T) {
	app := baseApp()
	app.Spec.Pipeline.Phases.Setup.Command = []string{"true"}
	app.Spec.Pipeline.Phases.Fetch.Command = []string{"true"}

	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			InitContainerStatuses: []corev1.ContainerStatus{
				term("clone", 0),
				term("setup", 0),
				running("fetch"),
				waiting("build"),
			},
			ContainerStatuses: []corev1.ContainerStatus{waiting("copier")},
		},
	}
	steps := deriveBuildSteps(applicableSteps(app), pod, false)
	want := map[string]bakerv1alpha1.StepStatus{
		bakerv1alpha1.StepClone:   bakerv1alpha1.StepStatusSucceeded,
		bakerv1alpha1.StepSetup:   bakerv1alpha1.StepStatusSucceeded,
		bakerv1alpha1.StepFetch:   bakerv1alpha1.StepStatusRunning,
		bakerv1alpha1.StepBuild:   bakerv1alpha1.StepStatusPending,
		bakerv1alpha1.StepCopier:  bakerv1alpha1.StepStatusPending,
		bakerv1alpha1.StepRelease: bakerv1alpha1.StepStatusPending,
	}
	for name, w := range want {
		if got := stepStatus(steps, name); got != w {
			t.Fatalf("step %s = %s, want %s", name, got, w)
		}
	}
}

// deriveBuildSteps: a failed step leaves downstream containers (which never ran)
// as Pending, not invented statuses.
func TestDeriveBuildSteps_FailureLeavesDownstreamPending(t *testing.T) {
	app := baseApp()
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			InitContainerStatuses: []corev1.ContainerStatus{
				term("clone", 0),
				term("build", 17), // build failed (no setup/fetch in this app)
			},
			// copier never started => no container status at all.
			ContainerStatuses: nil,
		},
	}
	steps := deriveBuildSteps(applicableSteps(app), pod, false)
	if got := stepStatus(steps, bakerv1alpha1.StepBuild); got != bakerv1alpha1.StepStatusFailed {
		t.Fatalf("build = %s, want Failed", got)
	}
	if got := stepStatus(steps, bakerv1alpha1.StepCopier); got != bakerv1alpha1.StepStatusPending {
		t.Fatalf("copier = %s, want Pending (never ran)", got)
	}
	if got := stepStatus(steps, bakerv1alpha1.StepRelease); got != bakerv1alpha1.StepStatusPending {
		t.Fatalf("release = %s, want Pending", got)
	}
}

// deriveBuildSteps: release is Succeeded only when releaseDone (copier done +
// pointer flipped).
func TestDeriveBuildSteps_ReleaseDone(t *testing.T) {
	app := baseApp()
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			InitContainerStatuses: []corev1.ContainerStatus{term("clone", 0), term("build", 0)},
			ContainerStatuses:     []corev1.ContainerStatus{term("copier", 0)},
		},
	}
	steps := deriveBuildSteps(applicableSteps(app), pod, true)
	if got := stepStatus(steps, bakerv1alpha1.StepCopier); got != bakerv1alpha1.StepStatusSucceeded {
		t.Fatalf("copier = %s, want Succeeded", got)
	}
	if got := stepStatus(steps, bakerv1alpha1.StepRelease); got != bakerv1alpha1.StepStatusSucceeded {
		t.Fatalf("release = %s, want Succeeded when releaseDone", got)
	}
}
