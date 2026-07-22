package controller

import (
	"context"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
		mut  func(*bakerv1alpha1.App)
		want []string
	}{
		{
			name: "minimal: no setup/fetch",
			mut:  func(*bakerv1alpha1.App) {},
			want: []string{bakerv1alpha1.StepClone, bakerv1alpha1.StepBuild, bakerv1alpha1.StepCopier, bakerv1alpha1.StepRelease},
		},
		{
			name: "setup via image only",
			mut:  func(a *bakerv1alpha1.App) { a.Spec.Pipeline.Phases.Setup.Image = "ghcr.io/toggle-corp/x" },
			want: []string{bakerv1alpha1.StepClone, bakerv1alpha1.StepSetup, bakerv1alpha1.StepBuild, bakerv1alpha1.StepCopier, bakerv1alpha1.StepRelease},
		},
		{
			name: "fetch via command only",
			mut:  func(a *bakerv1alpha1.App) { a.Spec.Pipeline.Phases.Fetch.Command = []string{"sh", "-c", "x"} },
			want: []string{bakerv1alpha1.StepClone, bakerv1alpha1.StepFetch, bakerv1alpha1.StepBuild, bakerv1alpha1.StepCopier, bakerv1alpha1.StepRelease},
		},
		{
			name: "both setup and fetch",
			mut: func(a *bakerv1alpha1.App) {
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
	t.Run("commit annotation set => Commit", func(t *testing.T) {
		app := baseApp()
		app.Annotations = map[string]string{bakerv1alpha1.RebuildCommitAnnotation: "abc1234"}
		if got := classifyTrigger(app); got != bakerv1alpha1.BuildTriggerCommit {
			t.Fatalf("got %s, want Commit", got)
		}
	})
	t.Run("by wins over commit (each source clears the other, but never trust it)", func(t *testing.T) {
		app := baseApp()
		app.Annotations = map[string]string{
			bakerv1alpha1.RebuildByAnnotation:     "octocat",
			bakerv1alpha1.RebuildCommitAnnotation: "abc1234",
		}
		if got := classifyTrigger(app); got != bakerv1alpha1.BuildTriggerManual {
			t.Fatalf("got %s, want Manual", got)
		}
	})
}

// A commit-triggered build records the SHA into status.build.commit; other
// triggers leave it empty even when a stale commit annotation lingers.
func TestStartBuild_RecordsCommitSHA(t *testing.T) {
	app := baseApp()
	app.Annotations = map[string]string{
		bakerv1alpha1.RebuildAnnotation:       "tok-9",
		bakerv1alpha1.RebuildCommitAnnotation: "cafebabe123",
	}
	r, _ := newReconciler(t, app, wffc())
	if err := r.startBuild(context.Background(), app, "tok-9", gitCredentialDecision{}); err != nil {
		t.Fatalf("startBuild: %v", err)
	}
	if app.Status.Build.Trigger != bakerv1alpha1.BuildTriggerCommit {
		t.Fatalf("trigger = %s, want Commit", app.Status.Build.Trigger)
	}
	if app.Status.Build.Commit != "cafebabe123" {
		t.Fatalf("commit = %q, want cafebabe123", app.Status.Build.Commit)
	}
}

func TestStartBuild_ManualDoesNotRecordStaleCommit(t *testing.T) {
	app := baseApp()
	app.Annotations = map[string]string{
		bakerv1alpha1.RebuildAnnotation:       "tok-9",
		bakerv1alpha1.RebuildByAnnotation:     "octocat",
		bakerv1alpha1.RebuildCommitAnnotation: "cafebabe123", // stale leftover
	}
	r, _ := newReconciler(t, app, wffc())
	if err := r.startBuild(context.Background(), app, "tok-9", gitCredentialDecision{}); err != nil {
		t.Fatalf("startBuild: %v", err)
	}
	if app.Status.Build.Trigger != bakerv1alpha1.BuildTriggerManual {
		t.Fatalf("trigger = %s, want Manual", app.Status.Build.Trigger)
	}
	if app.Status.Build.Commit != "" {
		t.Fatalf("commit = %q, want empty for a Manual build", app.Status.Build.Commit)
	}
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

// failedRec builds a Failed BuildStatus with a multi-step timeline whose named
// step failed, plus a termination + memory limit, for the trim tests.
func failedRec(job, failedStepName string) bakerv1alpha1.BuildStatus {
	return bakerv1alpha1.BuildStatus{
		JobName:    job,
		Result:     bakerv1alpha1.BuildResultFailed,
		FailedStep: failedStepName,
		Steps: []bakerv1alpha1.BuildStep{
			{Name: bakerv1alpha1.StepClone, Status: bakerv1alpha1.StepStatusSucceeded},
			{Name: bakerv1alpha1.StepSetup, Status: bakerv1alpha1.StepStatusSucceeded},
			{Name: failedStepName, Status: bakerv1alpha1.StepStatusFailed, Message: "boom", MemoryLimit: "2Gi"},
		},
		Termination: &bakerv1alpha1.BuildTermination{Reason: "OOMKilled", Container: failedStepName},
	}
}

// appendFailedBuildHistory retains only FAILED builds, newest-first, deduped by
// JobName, capped to max, and each entry trimmed to only the failed step.
func TestAppendFailedBuildHistory(t *testing.T) {
	t.Run("ignores non-failed builds", func(t *testing.T) {
		got := appendFailedBuildHistory(nil, bs("ok", bakerv1alpha1.BuildResultSucceeded), 10)
		if len(got) != 0 {
			t.Fatalf("succeeded build must not be retained, got %v", jobNames(got))
		}
		got = appendFailedBuildHistory(nil, bs("ab", bakerv1alpha1.BuildResultAborted), 10)
		if len(got) != 0 {
			t.Fatalf("aborted build must not be retained, got %v", jobNames(got))
		}
	})

	t.Run("retains failures across a success burst", func(t *testing.T) {
		h := appendFailedBuildHistory(nil, failedRec("f1", "build"), 10)
		// A burst of successes must NOT evict the failure (successes are ignored).
		for _, n := range []string{"s1", "s2", "s3", "s4", "s5"} {
			h = appendFailedBuildHistory(h, bs(n, bakerv1alpha1.BuildResultSucceeded), 10)
		}
		if !slices.Equal(jobNames(h), []string{"f1"}) {
			t.Fatalf("failure lost across success burst, got %v", jobNames(h))
		}
	})

	t.Run("prepends newest-first and dedups by JobName", func(t *testing.T) {
		h := appendFailedBuildHistory(nil, failedRec("f1", "build"), 10)
		h = appendFailedBuildHistory(h, failedRec("f2", "fetch"), 10)
		if !slices.Equal(jobNames(h), []string{"f2", "f1"}) {
			t.Fatalf("got %v, want [f2 f1]", jobNames(h))
		}
		// Re-observe f1: replace in place, no dup / reorder.
		h = appendFailedBuildHistory(h, failedRec("f1", "build"), 10)
		if !slices.Equal(jobNames(h), []string{"f2", "f1"}) {
			t.Fatalf("dedup failed, got %v", jobNames(h))
		}
	})

	t.Run("caps to max newest-first", func(t *testing.T) {
		var h []bakerv1alpha1.BuildStatus
		for _, n := range []string{"f1", "f2", "f3"} {
			h = appendFailedBuildHistory(h, failedRec(n, "build"), 2)
		}
		if !slices.Equal(jobNames(h), []string{"f3", "f2"}) {
			t.Fatalf("got %v, want [f3 f2] (cap 2)", jobNames(h))
		}
	})

	t.Run("trims entry to only the failed step", func(t *testing.T) {
		h := appendFailedBuildHistory(nil, failedRec("f1", "build"), 10)
		if len(h) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(h))
		}
		e := h[0]
		if len(e.Steps) != 1 || e.Steps[0].Name != "build" || e.Steps[0].Status != bakerv1alpha1.StepStatusFailed {
			t.Fatalf("entry not trimmed to the failed step: %+v", e.Steps)
		}
		if e.Steps[0].MemoryLimit != "2Gi" {
			t.Fatalf("failed step memory limit dropped: %+v", e.Steps[0])
		}
		if e.Termination == nil || e.Termination.Reason != "OOMKilled" {
			t.Fatalf("termination dropped: %+v", e.Termination)
		}
	})
}

// effectiveHistoryLimits: spec.history overrides the operator-config default
// per-field; an absent spec.history (or a zero sub-field) falls back.
func TestEffectiveHistoryLimits(t *testing.T) {
	r := reconcilerForPod() // Defaults(): keepRecent 5, keepFailed 10

	t.Run("defaults when spec.history absent", func(t *testing.T) {
		kr, kf := r.effectiveHistoryLimits(baseApp())
		if kr != 5 || kf != 10 {
			t.Fatalf("got %d/%d, want 5/10 (operator defaults)", kr, kf)
		}
	})

	t.Run("per-app override beats default", func(t *testing.T) {
		app := baseApp()
		app.Spec.History = &bakerv1alpha1.HistorySpec{KeepRecent: 3, KeepFailed: 20}
		kr, kf := r.effectiveHistoryLimits(app)
		if kr != 3 || kf != 20 {
			t.Fatalf("got %d/%d, want 3/20 (per-app override)", kr, kf)
		}
	})

	t.Run("zero sub-field falls back to default", func(t *testing.T) {
		app := baseApp()
		app.Spec.History = &bakerv1alpha1.HistorySpec{KeepRecent: 7} // KeepFailed omitted
		kr, kf := r.effectiveHistoryLimits(app)
		if kr != 7 || kf != 10 {
			t.Fatalf("got %d/%d, want 7/10 (keepFailed falls back)", kr, kf)
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

// deriveBuildSteps: harvests each real container's start/finish timestamps
// (per-step runtime) and the memory limit it ran with (peak-vs-allocated) from
// the pod; a Running container carries only StartedAt; a Pending step (its
// container never started) carries no limit even when the pod spec sets one;
// the synthetic release step carries neither.
func TestDeriveBuildSteps_TimesAndMemoryLimit(t *testing.T) {
	app := baseApp()

	t0 := metav1.NewTime(time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC))
	t1 := metav1.NewTime(t0.Add(90 * time.Second))
	cloneCS := term("clone", 0)
	cloneCS.State.Terminated.StartedAt = t0
	cloneCS.State.Terminated.FinishedAt = t1
	buildCS := running("build")
	buildCS.State.Running.StartedAt = t1

	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{
				{Name: "clone"}, // platform container: no memory limit set
				{Name: "build", Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")},
				}},
			},
			// copier has a spec limit but no container status yet (init phase
			// still running): the Pending step must NOT pick the limit up.
			Containers: []corev1.Container{
				{Name: "copier", Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
				}},
			},
		},
		Status: corev1.PodStatus{
			InitContainerStatuses: []corev1.ContainerStatus{cloneCS, buildCS},
		},
	}
	steps := deriveBuildSteps(applicableSteps(app), pod, false)
	byName := map[string]bakerv1alpha1.BuildStep{}
	for _, s := range steps {
		byName[s.Name] = s
	}

	clone := byName[bakerv1alpha1.StepClone]
	if clone.StartedAt == nil || !clone.StartedAt.Equal(&t0) {
		t.Errorf("clone.StartedAt = %v, want %v", clone.StartedAt, t0)
	}
	if clone.FinishedAt == nil || !clone.FinishedAt.Equal(&t1) {
		t.Errorf("clone.FinishedAt = %v, want %v", clone.FinishedAt, t1)
	}
	if clone.MemoryLimit != "" {
		t.Errorf("clone.MemoryLimit = %q, want empty (no limit set)", clone.MemoryLimit)
	}

	build := byName[bakerv1alpha1.StepBuild]
	if build.StartedAt == nil || !build.StartedAt.Equal(&t1) {
		t.Errorf("build.StartedAt = %v, want %v", build.StartedAt, t1)
	}
	if build.FinishedAt != nil {
		t.Errorf("build.FinishedAt = %v, want nil while Running", build.FinishedAt)
	}
	if build.MemoryLimit != "2Gi" {
		t.Errorf("build.MemoryLimit = %q, want 2Gi", build.MemoryLimit)
	}

	copier := byName[bakerv1alpha1.StepCopier]
	if copier.Status != bakerv1alpha1.StepStatusPending {
		t.Errorf("copier.Status = %s, want Pending (no container status)", copier.Status)
	}
	if copier.MemoryLimit != "" {
		t.Errorf("copier.MemoryLimit = %q, want empty for a Pending step", copier.MemoryLimit)
	}

	release := byName[bakerv1alpha1.StepRelease]
	if release.StartedAt != nil || release.FinishedAt != nil || release.MemoryLimit != "" {
		t.Errorf("release step must carry no times/limit: %+v", release)
	}
}

// termMsg is term() with a termination message (what the shim wrapper writes).
func termMsg(name string, exit int32, msg string) corev1.ContainerStatus {
	cs := term(name, exit)
	cs.State.Terminated.Message = msg
	return cs
}

// parsePeakMemory: extracts the shim's line, defensively zeroing everything else.
func TestParsePeakMemory(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want int64
	}{
		{"plain", "peakMemoryBytes=634040320\n", 634040320},
		{"among other lines", "something else\npeakMemoryBytes=42\n", 42},
		{"absent", "no key here", 0},
		{"empty", "", 0},
		{"garbage value", "peakMemoryBytes=abc", 0},
		{"negative", "peakMemoryBytes=-5", 0},
		{"whitespace tolerated", "  peakMemoryBytes=7  \n", 7},
	}
	for _, c := range cases {
		if got := parsePeakMemory(c.msg); got != c.want {
			t.Errorf("%s: parsePeakMemory(%q) = %d, want %d", c.name, c.msg, got, c.want)
		}
	}
}

// deriveBuildSteps: a terminated shim-wrapped step's termination message yields
// PeakMemoryBytes — on success AND on failure (the shim reports before exiting
// either way); running/pending steps and the copier read 0.
func TestDeriveBuildSteps_HarvestsPeakMemory(t *testing.T) {
	app := baseApp()
	app.Spec.Pipeline.Phases.Setup.Command = []string{"true"}
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			InitContainerStatuses: []corev1.ContainerStatus{
				term("clone", 0),
				termMsg("setup", 0, "peakMemoryBytes=111\n"),
				termMsg("build", 1, "peakMemoryBytes=3555555555\n"),
			},
			ContainerStatuses: []corev1.ContainerStatus{waiting("copier")},
		},
	}
	steps := deriveBuildSteps(applicableSteps(app), pod, false)
	peaks := map[string]int64{}
	for _, s := range steps {
		peaks[s.Name] = s.PeakMemoryBytes
	}
	if peaks[bakerv1alpha1.StepSetup] != 111 {
		t.Errorf("setup peak = %d, want 111", peaks[bakerv1alpha1.StepSetup])
	}
	if peaks[bakerv1alpha1.StepBuild] != 3555555555 {
		t.Errorf("FAILED build step must still record its peak, got %d", peaks[bakerv1alpha1.StepBuild])
	}
	for _, name := range []string{bakerv1alpha1.StepClone, bakerv1alpha1.StepCopier, bakerv1alpha1.StepRelease} {
		if peaks[name] != 0 {
			t.Errorf("%s peak = %d, want 0 (unmeasured)", name, peaks[name])
		}
	}
}
