package controller

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
	"github.com/toggle-corp/toggle-web-baker/internal/observability"
)

// recordingTransport implements sentry.Transport and records every event sent
// through it so tests can assert on the decoded *sentry.Event. Duplicated from
// internal/observability's transport_test.go (test files aren't importable).
type recordingTransport struct {
	mu     sync.Mutex
	events []*sentry.Event
}

func (tr *recordingTransport) Configure(sentry.ClientOptions) {}

func (tr *recordingTransport) SendEvent(event *sentry.Event) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.events = append(tr.events, event)
}

func (tr *recordingTransport) Flush(time.Duration) bool { return true }

func (tr *recordingTransport) FlushWithContext(context.Context) bool { return true }

func (tr *recordingTransport) Close() {}

func (tr *recordingTransport) Events() []*sentry.Event {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	out := make([]*sentry.Event, len(tr.events))
	copy(out, tr.events)
	return out
}

// newRecordingReporter builds a Reporter around an isolated hub (never
// sentry.CurrentHub()) whose transport records events for assertions.
func newRecordingReporter(t *testing.T) (*observability.Reporter, *recordingTransport) {
	t.Helper()
	transport := &recordingTransport{}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:       "https://key@example.ingest.sentry.io/1",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("sentry.NewClient: %v", err)
	}
	hub := sentry.NewHub(client, sentry.NewScope())
	return observability.NewReporterForTest(hub, func() time.Time { return time.Unix(1000, 0) }), transport
}

// A reconcile-time ConfigError (operator misconfiguration) is a platform fault:
// fail() must emit exactly one Sentry event tagged with the reason and
// fingerprinted [app, reason].
func TestSentryEmission_ConfigErrorEmitsOneEvent(t *testing.T) {
	app := baseApp()
	r, _ := newReconciler(t, app, wffc())
	reporter, transport := newRecordingReporter(t)
	r.Sentry = reporter
	r.Config.ClusterCIDRs = nil // forces Config.Validate() to fail => ConfigError

	reconcile(t, r, app) // finalizer
	reconcile(t, r, app)

	events := transport.Events()
	if len(events) != 1 {
		t.Fatalf("got %d Sentry events, want 1", len(events))
	}
	ev := events[0]
	if ev.Tags["reason"] != bakerv1alpha1.ReasonConfigError {
		t.Fatalf("reason tag = %q, want %q", ev.Tags["reason"], bakerv1alpha1.ReasonConfigError)
	}
	wantFP := []string{"demo", bakerv1alpha1.ReasonConfigError}
	if len(ev.Fingerprint) != 2 || ev.Fingerprint[0] != wantFP[0] || ev.Fingerprint[1] != wantFP[1] {
		t.Fatalf("fingerprint = %v, want %v", ev.Fingerprint, wantFP)
	}
}

// A spec rejection (user's spec names a disallowed image) is a user error:
// fail() runs but must NOT emit to Sentry.
func TestSentryEmission_ImageNotAllowedEmitsNothing(t *testing.T) {
	app := baseApp()
	app.Spec.Pipeline.Phases.Build.Image = "docker.io/evil/builder:latest"
	r, cl := newReconciler(t, app, wffc())
	reporter, transport := newRecordingReporter(t)
	r.Sentry = reporter

	reconcile(t, r, app) // finalizer
	reconcile(t, r, app)

	// The rejection itself must have happened for the assertion to mean anything.
	got := getApp(t, cl, "demo", "apps")
	cond := findCondition(got, bakerv1alpha1.ConditionReady)
	if cond == nil || cond.Reason != bakerv1alpha1.ReasonImageNotAllowed {
		t.Fatalf("expected Ready=False/ImageNotAllowed, got %+v", cond)
	}
	if n := len(transport.Events()); n != 0 {
		t.Fatalf("got %d Sentry events, want 0 for a user spec error", n)
	}
}

// failedBuildJob registers a JobFailed build Job for app in the fake client.
func failedBuildJob(t *testing.T, cl client.Client, app *bakerv1alpha1.FrontendApp, name string) *batchv1.Job {
	t.Helper()
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: app.Namespace, UID: types.UID(name + "-uid"),
			Labels:      buildLabelsFor(app),
			Annotations: map[string]string{bakerv1alpha1.SpecHashAnnotation: buildSpecFrom(app).Hash()},
		},
		Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Message: "boom"}}},
	}
	if err := cl.Create(context.Background(), job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	return job
}

// A failed copier container is platform-owned machinery: the terminal
// job-failure branch must emit one event tagged step=copier.
func TestSentryEmission_CopierFailureEmitsStepTaggedEvent(t *testing.T) {
	app := baseApp()
	r, cl := newReconciler(t, app, wffc())
	reporter, transport := newRecordingReporter(t)
	r.Sentry = reporter
	job := failedBuildJob(t, cl, app, "demo-build-copierfail")
	buildPodForJob(t, cl, app, job, "demo-build-copierfail-pod",
		[]corev1.ContainerStatus{
			{Name: "clone", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
			{Name: "build", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
		},
		[]corev1.ContainerStatus{
			{Name: "copier", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}}},
		},
	)
	app.Status.Build = bakerv1alpha1.BuildStatus{Phase: bakerv1alpha1.BuildPhaseRunning, JobName: job.Name}

	if err := r.observeBuild(context.Background(), app); err != nil {
		t.Fatalf("observeBuild: %v", err)
	}

	events := transport.Events()
	if len(events) != 1 {
		t.Fatalf("got %d Sentry events, want 1 for a copier failure", len(events))
	}
	ev := events[0]
	if ev.Tags["step"] != bakerv1alpha1.StepCopier {
		t.Fatalf("step tag = %q, want %q", ev.Tags["step"], bakerv1alpha1.StepCopier)
	}
	if ev.Tags["reason"] != bakerv1alpha1.ReasonBuildFailed {
		t.Fatalf("reason tag = %q, want %q", ev.Tags["reason"], bakerv1alpha1.ReasonBuildFailed)
	}
	if ev.Message != "boom" {
		t.Fatalf("message = %q, want the job condition message %q", ev.Message, "boom")
	}
}

// A failed build init container is the user's build breaking — no event.
func TestSentryEmission_UserBuildFailureEmitsNothing(t *testing.T) {
	app := baseApp()
	r, cl := newReconciler(t, app, wffc())
	reporter, transport := newRecordingReporter(t)
	r.Sentry = reporter
	job := failedBuildJob(t, cl, app, "demo-build-userfail")
	buildPodForJob(t, cl, app, job, "demo-build-userfail-pod",
		[]corev1.ContainerStatus{
			{Name: "clone", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
			{Name: "build", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 2}}},
		},
		nil, // copier never ran
	)
	app.Status.Build = bakerv1alpha1.BuildStatus{Phase: bakerv1alpha1.BuildPhaseRunning, JobName: job.Name}

	if err := r.observeBuild(context.Background(), app); err != nil {
		t.Fatalf("observeBuild: %v", err)
	}
	if app.Status.Build.FailedStep != bakerv1alpha1.StepBuild {
		t.Fatalf("FailedStep = %q, want build (test setup must attribute the failure)", app.Status.Build.FailedStep)
	}
	if n := len(transport.Events()); n != 0 {
		t.Fatalf("got %d Sentry events, want 0 for a user build failure", n)
	}
}

// An OOMKilled build container must emit nothing: classification runs AFTER
// the OOM promotion, so the final reason (OOMKilled, user's memory limit) is
// what gets classified — not the interim generic BuildFailed.
func TestSentryEmission_OOMKilledBuildEmitsNothing(t *testing.T) {
	app := baseApp()
	r, cl := newReconciler(t, app, wffc())
	reporter, transport := newRecordingReporter(t)
	r.Sentry = reporter
	job := failedBuildJob(t, cl, app, "demo-build-oomquiet")
	buildPodForJob(t, cl, app, job, "demo-build-oomquiet-pod",
		[]corev1.ContainerStatus{
			{Name: "clone", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
			{Name: "build", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137, Reason: "OOMKilled"}}},
		},
		nil,
	)
	app.Status.Build = bakerv1alpha1.BuildStatus{Phase: bakerv1alpha1.BuildPhaseRunning, JobName: job.Name}

	if err := r.observeBuild(context.Background(), app); err != nil {
		t.Fatalf("observeBuild: %v", err)
	}
	// The promotion must have happened for the assertion to prove ordering.
	deg := findCondition(app, bakerv1alpha1.ConditionDegraded)
	if deg == nil || deg.Reason != bakerv1alpha1.ReasonOOMKilled {
		t.Fatalf("Degraded = %+v, want reason OOMKilled (promotion is this test's premise)", deg)
	}
	if n := len(transport.Events()); n != 0 {
		t.Fatalf("got %d Sentry events, want 0 for an OOMKilled build", n)
	}
}

// Ordering proof: an OOMKilled COPIER discriminates the two orderings. If the
// classifier ran before the OOM promotion it would see BuildFailed×copier
// (platform fault, event); after promotion it sees OOMKilled (user fault, no
// event).
func TestSentryEmission_OOMKilledCopierEmitsNothing(t *testing.T) {
	app := baseApp()
	r, cl := newReconciler(t, app, wffc())
	reporter, transport := newRecordingReporter(t)
	r.Sentry = reporter
	job := failedBuildJob(t, cl, app, "demo-build-copieroom")
	buildPodForJob(t, cl, app, job, "demo-build-copieroom-pod",
		[]corev1.ContainerStatus{
			{Name: "clone", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
			{Name: "build", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
		},
		[]corev1.ContainerStatus{
			{Name: "copier", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137, Reason: "OOMKilled"}}},
		},
	)
	app.Status.Build = bakerv1alpha1.BuildStatus{Phase: bakerv1alpha1.BuildPhaseRunning, JobName: job.Name}

	if err := r.observeBuild(context.Background(), app); err != nil {
		t.Fatalf("observeBuild: %v", err)
	}
	deg := findCondition(app, bakerv1alpha1.ConditionDegraded)
	if deg == nil || deg.Reason != bakerv1alpha1.ReasonOOMKilled {
		t.Fatalf("Degraded = %+v, want reason OOMKilled (promotion is this test's premise)", deg)
	}
	if n := len(transport.Events()); n != 0 {
		t.Fatalf("got %d Sentry events, want 0: classifier must see the promoted OOMKilled reason, not BuildFailed x copier", n)
	}
}

// Sentry disabled (nil Reporter, the production empty-DSN case): a platform
// fault that WOULD emit must not panic; the reconciler proceeds normally.
func TestSentryEmission_NilReporterCopierFailureIsSafe(t *testing.T) {
	app := baseApp()
	r, cl := newReconciler(t, app, wffc()) // r.Sentry stays nil
	job := failedBuildJob(t, cl, app, "demo-build-nilrep")
	buildPodForJob(t, cl, app, job, "demo-build-nilrep-pod",
		[]corev1.ContainerStatus{
			{Name: "clone", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
			{Name: "build", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
		},
		[]corev1.ContainerStatus{
			{Name: "copier", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}}},
		},
	)
	app.Status.Build = bakerv1alpha1.BuildStatus{Phase: bakerv1alpha1.BuildPhaseRunning, JobName: job.Name}

	if err := r.observeBuild(context.Background(), app); err != nil {
		t.Fatalf("observeBuild with nil reporter: %v", err)
	}
	if app.Status.Build.Result != bakerv1alpha1.BuildResultFailed {
		t.Fatalf("result = %s, want Failed (observe must complete normally)", app.Status.Build.Result)
	}
}
