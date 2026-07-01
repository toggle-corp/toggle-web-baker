package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
)

// termReason builds a terminated container status with an explicit reason
// (e.g. "OOMKilled"), extending the exit-only term() helper in buildsteps_test.go.
func termReason(name string, exit int32, reason string) corev1.ContainerStatus {
	cs := term(name, exit)
	cs.State.Terminated.Reason = reason
	return cs
}

// initC builds a spec init container with a memory limit, so detectTermination
// can read the limit the build actually ran with off the pod spec.
func initC(name, memLimit string) corev1.Container {
	return corev1.Container{
		Name: name,
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(memLimit)},
		},
	}
}

// detectTermination: an OOMKilled build init container yields a BuildTermination
// naming the step, its exit code, and the memory limit it ran with (from spec).
func TestDetectTermination_OOMKilledBuild(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{initC("clone", "256Mi"), initC("build", "256Mi")},
		},
		Status: corev1.PodStatus{
			InitContainerStatuses: []corev1.ContainerStatus{
				term("clone", 0),
				termReason("build", 137, "OOMKilled"),
			},
		},
	}
	got := detectTermination(pod, bakerv1alpha1.StepBuild)
	if got == nil {
		t.Fatal("detectTermination = nil, want a termination for the OOMKilled build step")
	}
	if got.Reason != "OOMKilled" {
		t.Errorf("Reason = %q, want OOMKilled", got.Reason)
	}
	if got.Container != bakerv1alpha1.StepBuild {
		t.Errorf("Container = %q, want build", got.Container)
	}
	if got.ExitCode != 137 {
		t.Errorf("ExitCode = %d, want 137", got.ExitCode)
	}
	if got.MemoryLimit != "256Mi" {
		t.Errorf("MemoryLimit = %q, want 256Mi", got.MemoryLimit)
	}
	if got.FinishedAt != nil {
		t.Errorf("FinishedAt = %v, want nil when the container has no finish time", got.FinishedAt)
	}
}

// detectTermination: a nil pod (already reaped) yields nil — nothing to report.
func TestDetectTermination_NilPod(t *testing.T) {
	if got := detectTermination(nil, bakerv1alpha1.StepBuild); got != nil {
		t.Fatalf("detectTermination(nil) = %+v, want nil", got)
	}
}

// detectTermination: an empty failed step (build succeeded, or pod gone so
// failedStep is "") and the synthetic release step both yield nil — there is no
// container to inspect.
func TestDetectTermination_NoInspectableStep(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			InitContainerStatuses: []corev1.ContainerStatus{term("clone", 0), term("build", 0)},
			ContainerStatuses:     []corev1.ContainerStatus{term("copier", 0)},
		},
	}
	if got := detectTermination(pod, ""); got != nil {
		t.Fatalf("empty step: got %+v, want nil", got)
	}
	if got := detectTermination(pod, bakerv1alpha1.StepRelease); got != nil {
		t.Fatalf("release step: got %+v, want nil (synthetic, no container)", got)
	}
}

// detectTermination: the named step's container is present but not terminated
// (e.g. still Running) — nil, never a bogus termination.
func TestDetectTermination_StepNotTerminated(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{running("build")}},
	}
	if got := detectTermination(pod, bakerv1alpha1.StepBuild); got != nil {
		t.Fatalf("got %+v, want nil (build not terminated)", got)
	}
}

// detectTermination: a plain non-zero exit (not OOM) is still reported, carrying
// whatever container reason Kubernetes set (e.g. "Error") — the field
// generalizes beyond OOM.
func TestDetectTermination_NonOOMFailure(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{InitContainers: []corev1.Container{initC("build", "512Mi")}},
		Status: corev1.PodStatus{
			InitContainerStatuses: []corev1.ContainerStatus{termReason("build", 1, "Error")},
		},
	}
	got := detectTermination(pod, bakerv1alpha1.StepBuild)
	if got == nil {
		t.Fatal("detectTermination = nil, want a termination for the failed build step")
	}
	if got.Reason != "Error" || got.ExitCode != 1 || got.Container != bakerv1alpha1.StepBuild {
		t.Fatalf("got %+v, want {Reason:Error ExitCode:1 Container:build}", got)
	}
}

// detectTermination: the copier (the main container, not an init container) can
// OOM too — it is read from ContainerStatuses / spec.Containers.
func TestDetectTermination_CopierOOM(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{initC("copier", "128Mi")},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{termReason("copier", 137, "OOMKilled")},
		},
	}
	got := detectTermination(pod, bakerv1alpha1.StepCopier)
	if got == nil || got.Container != bakerv1alpha1.StepCopier || got.Reason != "OOMKilled" || got.MemoryLimit != "128Mi" {
		t.Fatalf("got %+v, want copier OOMKilled @128Mi", got)
	}
}

// detectTermination: a failed container with no memory limit on its spec yields
// an empty MemoryLimit (no bar / limit line), never a panic.
func TestDetectTermination_NoMemoryLimit(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{InitContainers: []corev1.Container{{Name: "build"}}},
		Status: corev1.PodStatus{
			InitContainerStatuses: []corev1.ContainerStatus{termReason("build", 137, "OOMKilled")},
		},
	}
	got := detectTermination(pod, bakerv1alpha1.StepBuild)
	if got == nil || got.MemoryLimit != "" {
		t.Fatalf("got %+v, want empty MemoryLimit", got)
	}
}

func TestTerminationStepMessage(t *testing.T) {
	cases := []struct {
		name string
		in   *bakerv1alpha1.BuildTermination
		want string
	}{
		{"nil", nil, ""},
		{"no reason", &bakerv1alpha1.BuildTermination{}, ""},
		{"oom with limit", &bakerv1alpha1.BuildTermination{Reason: "OOMKilled", MemoryLimit: "256Mi"}, "OOMKilled (limit 256Mi)"},
		{"oom no limit", &bakerv1alpha1.BuildTermination{Reason: "OOMKilled"}, "OOMKilled"},
		{"non-oom", &bakerv1alpha1.BuildTermination{Reason: "Error"}, "Error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := terminationStepMessage(tc.in); got != tc.want {
				t.Fatalf("terminationStepMessage = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOOMConditionMessage(t *testing.T) {
	if got := oomConditionMessage(&bakerv1alpha1.BuildTermination{Container: "build", MemoryLimit: "256Mi"}); got != "the build step exceeded its 256Mi memory limit" {
		t.Fatalf("with limit: got %q", got)
	}
	if got := oomConditionMessage(&bakerv1alpha1.BuildTermination{Container: "build"}); got != "the build step was OOMKilled" {
		t.Fatalf("no limit: got %q", got)
	}
}
