package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/toggle-corp/toggle-web-baker/console/internal/k8s"
	"github.com/toggle-corp/toggle-web-baker/console/internal/view"

	corev1 "k8s.io/api/core/v1"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// fakePodReader fakes the PodReader interface so log handler tests run without a
// cluster. getErr makes GetPod fail (pod gone); logLines/logErr drive PodLogTail.
type fakePodReader struct {
	getErr   error
	pod      *corev1.Pod
	logLines []string
	logErr   error

	lastLogContainer string
	lastLogPod       string
}

func (f *fakePodReader) GetPod(_ context.Context, _, name string) (*corev1.Pod, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.pod != nil {
		return f.pod, nil
	}
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name}}, nil
}

func (f *fakePodReader) PodLogTail(_ context.Context, _, pod, container string, _ int64) ([]string, error) {
	f.lastLogPod = pod
	f.lastLogContainer = container
	return f.logLines, f.logErr
}

// fakeMetricser fakes the k8s.PodMetricser interface. usage is returned for
// PodMetrics; err makes it fail; block (when honored) sleeps until the context
// deadline so the bounded-timeout path can be exercised. calls counts invocations.
type fakeMetricser struct {
	usage map[string]k8s.ContainerUsage
	err   error
	block bool

	calls int
}

func (f *fakeMetricser) PodMetrics(ctx context.Context, _, _ string) (map[string]k8s.ContainerUsage, error) {
	f.calls++
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.usage, nil
}

// fakeLokiTailer fakes the LokiTailer interface.
type fakeLokiTailer struct {
	configured bool
	lines      []string
	err        error
}

func (f *fakeLokiTailer) Configured() bool { return f.configured }
func (f *fakeLokiTailer) Tail(_ context.Context, _, _, _ string, _, _ time.Time, _ int) ([]string, error) {
	return f.lines, f.err
}

// newTestServer wires the real Client over a fake dynamic client seeded with
// one FrontendApp, so the handlers exercise the actual list/get/patch paths.
// The pod reader and loki tailer default to harmless fakes; tests that need
// specific log behaviour build a server directly with New.
func newTestServer(t *testing.T) (*Server, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	dyn := seededDyn(t, nil)
	return New(k8s.NewWithDynamic(dyn), &fakePodReader{}, &fakeLokiTailer{}, nil), dyn
}

// seededDyn builds a fake dynamic client seeded with one FrontendApp. statusExtra
// is merged into the default status when non-nil (lets log tests inject a build).
func seededDyn(t *testing.T, status map[string]any) *dynamicfake.FakeDynamicClient {
	t.Helper()
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		k8s.GVR: "FrontendAppList",
	}
	if status == nil {
		status = map[string]any{"phase": "Ready"}
	}
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "baker.toggle-corp.com/v1alpha1",
		"kind":       "FrontendApp",
		"metadata": map[string]any{
			"namespace": "mapswipe",
			"name":      "mapswipe-uat",
		},
		"status": status,
	}}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, app)
}

func TestRebuild_PatchesAnnotationsWithHeaderUser(t *testing.T) {
	// Freeze the clock so the requested-at value is assertable.
	frozen := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	orig := view.Now
	view.Now = func() time.Time { return frozen }
	defer func() { view.Now = orig }()

	srv, dyn := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/ns/mapswipe/app/mapswipe-uat/rebuild", nil)
	req.SetPathValue("namespace", "mapswipe")
	req.SetPathValue("name", "mapswipe-uat")
	req.Header.Set("X-Auth-Request-User", "octocat")
	rec := httptest.NewRecorder()

	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/ns/mapswipe/app/mapswipe-uat") {
		t.Errorf("redirect Location = %q", loc)
	}

	got, err := dyn.Resource(k8s.GVR).Namespace("mapswipe").Get(context.Background(), "mapswipe-uat", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get after patch: %v", err)
	}
	ann := got.GetAnnotations()
	if ann[view.AnnotationRebuildBy] != "octocat" {
		t.Errorf("by annotation = %q, want octocat", ann[view.AnnotationRebuildBy])
	}
	if ann[view.AnnotationRebuildRequestedAt] != frozen.Format(time.RFC3339) {
		t.Errorf("requested-at = %q, want %q", ann[view.AnnotationRebuildRequestedAt], frozen.Format(time.RFC3339))
	}
}

func TestRebuild_FallsBackToForwardedUserHeader(t *testing.T) {
	srv, dyn := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/ns/mapswipe/app/mapswipe-uat/rebuild", nil)
	req.SetPathValue("namespace", "mapswipe")
	req.SetPathValue("name", "mapswipe-uat")
	req.Header.Set("X-Forwarded-User", "hubber")
	rec := httptest.NewRecorder()

	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	got, _ := dyn.Resource(k8s.GVR).Namespace("mapswipe").Get(context.Background(), "mapswipe-uat", metav1.GetOptions{})
	if got.GetAnnotations()[view.AnnotationRebuildBy] != "hubber" {
		t.Errorf("by annotation = %q, want hubber", got.GetAnnotations()[view.AnnotationRebuildBy])
	}
}

func TestRebuild_RejectsWhenNoUserHeader(t *testing.T) {
	srv, dyn := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/ns/mapswipe/app/mapswipe-uat/rebuild", nil)
	req.SetPathValue("namespace", "mapswipe")
	req.SetPathValue("name", "mapswipe-uat")
	rec := httptest.NewRecorder()

	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	// No annotations must have been written when the user header is absent.
	got, _ := dyn.Resource(k8s.GVR).Namespace("mapswipe").Get(context.Background(), "mapswipe-uat", metav1.GetOptions{})
	if _, ok := got.GetAnnotations()[view.AnnotationRebuildRequestedAt]; ok {
		t.Error("rebuild annotation must not be set when no user header present")
	}
}

func TestCleanupCache_PatchesAnnotationsAndRedirects(t *testing.T) {
	frozen := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	orig := view.Now
	view.Now = func() time.Time { return frozen }
	defer func() { view.Now = orig }()

	srv, dyn := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/ns/mapswipe/app/mapswipe-uat/cleanup-cache", nil)
	req.SetPathValue("namespace", "mapswipe")
	req.SetPathValue("name", "mapswipe-uat")
	req.Header.Set("X-Auth-Request-User", "octocat")
	rec := httptest.NewRecorder()

	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/ns/mapswipe/app/mapswipe-uat?cleanup=cache" {
		t.Errorf("redirect Location = %q", loc)
	}
	got, err := dyn.Resource(k8s.GVR).Namespace("mapswipe").Get(context.Background(), "mapswipe-uat", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get after patch: %v", err)
	}
	ann := got.GetAnnotations()
	if ann[view.AnnotationCleanupCacheBy] != "octocat" {
		t.Errorf("cache by = %q, want octocat", ann[view.AnnotationCleanupCacheBy])
	}
	if ann[view.AnnotationCleanupCacheRequestedAt] != frozen.Format(time.RFC3339) {
		t.Errorf("cache requested-at = %q", ann[view.AnnotationCleanupCacheRequestedAt])
	}
}

func TestCleanupCache_RejectsWhenNoUserHeader(t *testing.T) {
	srv, dyn := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/ns/mapswipe/app/mapswipe-uat/cleanup-cache", nil)
	req.SetPathValue("namespace", "mapswipe")
	req.SetPathValue("name", "mapswipe-uat")
	rec := httptest.NewRecorder()

	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	got, _ := dyn.Resource(k8s.GVR).Namespace("mapswipe").Get(context.Background(), "mapswipe-uat", metav1.GetOptions{})
	if _, ok := got.GetAnnotations()[view.AnnotationCleanupCacheRequestedAt]; ok {
		t.Error("cleanup-cache annotation must not be set when no user header present")
	}
}

func TestCleanupReleases_PatchesAnnotationsAndRedirects(t *testing.T) {
	frozen := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	orig := view.Now
	view.Now = func() time.Time { return frozen }
	defer func() { view.Now = orig }()

	srv, dyn := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/ns/mapswipe/app/mapswipe-uat/cleanup-releases", nil)
	req.SetPathValue("namespace", "mapswipe")
	req.SetPathValue("name", "mapswipe-uat")
	req.Header.Set("X-Forwarded-User", "hubber")
	rec := httptest.NewRecorder()

	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/ns/mapswipe/app/mapswipe-uat?cleanup=releases" {
		t.Errorf("redirect Location = %q", loc)
	}
	got, _ := dyn.Resource(k8s.GVR).Namespace("mapswipe").Get(context.Background(), "mapswipe-uat", metav1.GetOptions{})
	ann := got.GetAnnotations()
	if ann[view.AnnotationCleanupReleasesBy] != "hubber" {
		t.Errorf("releases by = %q, want hubber", ann[view.AnnotationCleanupReleasesBy])
	}
	if ann[view.AnnotationCleanupReleasesRequestedAt] != frozen.Format(time.RFC3339) {
		t.Errorf("releases requested-at = %q", ann[view.AnnotationCleanupReleasesRequestedAt])
	}
}

// cleanupBusyStatus is a current build mid-flight, so CleanupBusy() is true and
// the cleanup buttons must render disabled.
func cleanupBusyStatus() map[string]any {
	return map[string]any{
		"phase": "Building",
		"build": map[string]any{"phase": "Running", "jobName": "b-1"},
		"cleanup": map[string]any{
			"cache": map[string]any{
				"phase": "Succeeded", "reclaimedBytes": int64(1048576),
				"lastCompleted": "2026-06-25T09:05:00Z", "message": "pruned layers",
			},
		},
	}
}

func TestStorageCard_RendersCleanupButtonsDisabledWhenBusy(t *testing.T) {
	dyn := seededDyn(t, cleanupBusyStatus())
	srv := New(k8s.NewWithDynamic(dyn), &fakePodReader{}, &fakeLokiTailer{}, nil)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/partial", "mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/cleanup-cache") || !strings.Contains(body, "/cleanup-releases") {
		t.Errorf("storage card should render both cleanup forms; body=%s", body)
	}
	if !strings.Contains(body, "disabled") {
		t.Errorf("cleanup buttons should be disabled while busy; body=%s", body)
	}
	// Cleanup status (reclaimed/message/phase) should surface.
	if !strings.Contains(body, "pruned layers") {
		t.Errorf("should render the cleanup message; body=%s", body)
	}
	if !strings.Contains(body, view.HumanizeBytes(1048576)) {
		t.Errorf("should render reclaimed bytes humanized; body=%s", body)
	}
}

func TestStorageCard_CleanupButtonsEnabledWhenIdle(t *testing.T) {
	dyn := seededDyn(t, completedBuildStatus())
	srv := New(k8s.NewWithDynamic(dyn), &fakePodReader{}, &fakeLokiTailer{}, nil)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/partial", "mapswipe", "mapswipe-uat")
	body := rec.Body.String()
	if !strings.Contains(body, "/cleanup-cache") {
		t.Errorf("idle storage card should still render cleanup forms; body=%s", body)
	}
	if strings.Contains(body, "disabled") {
		t.Errorf("cleanup buttons must NOT be disabled when idle; body=%s", body)
	}
}

func TestList_RendersSeededApp(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Auth-Request-User", "octocat")
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "mapswipe-uat") {
		t.Error("list page should contain the seeded app name")
	}
	if !strings.Contains(body, "octocat") {
		t.Error("list page should show the signed-in user")
	}
	if !strings.Contains(body, `href="/oauth2/sign_out?rd=/signed-out"`) {
		t.Error("authenticated page should show the logout link")
	}
}

func TestThemeControls(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Auth-Request-User", "octocat")
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// Anti-FOUC inline script markers: must read localStorage and apply data-theme.
	for _, marker := range []string{"localStorage", "data-theme", "baker-theme"} {
		if !strings.Contains(body, marker) {
			t.Errorf("page should contain anti-FOUC marker %q", marker)
		}
	}

	// System default must follow the OS via the media query.
	if !strings.Contains(body, "prefers-color-scheme") {
		t.Error("page should contain a prefers-color-scheme media query")
	}

	// Three-state theme control.
	if !strings.Contains(body, "<select") {
		t.Error("page should contain a theme <select> control")
	}
	for _, opt := range []string{"System", "Light", "Dark"} {
		if !strings.Contains(body, opt) {
			t.Errorf("theme control should offer the %q option", opt)
		}
	}
}

func TestSignedOut(t *testing.T) {
	srv, _ := newTestServer(t)
	// Public page: no X-Auth-Request-User header.
	req := httptest.NewRequest(http.MethodGet, "/signed-out", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(strings.ToLower(body), "signed out") {
		t.Error("signed-out page should contain a 'signed out' message")
	}
	// Honest GitHub-SSO caveat: clearing the cookie does not end the GitHub
	// session; user must remove the OAuth app to fully revoke access.
	if !strings.Contains(body, "GitHub") || !strings.Contains(strings.ToLower(body), "revoke") {
		t.Error("signed-out page should explain the GitHub revocation caveat")
	}
	// "Sign in again" link bounces through oauth2-proxy at the root.
	if !strings.Contains(body, `href="/"`) {
		t.Error("signed-out page should link to / to sign in again")
	}
	// Logged-out page must not offer a logout link.
	if strings.Contains(body, "/oauth2/sign_out") {
		t.Error("signed-out page must not contain a logout link")
	}
}

func TestDetail_RendersStatus(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/ns/mapswipe/app/mapswipe-uat", nil)
	req.SetPathValue("namespace", "mapswipe")
	req.SetPathValue("name", "mapswipe-uat")
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Request rebuild") {
		t.Error("detail page should render the rebuild button")
	}
}

func TestDetail_RendersManualTriggerAuthor(t *testing.T) {
	// A current manual build attributed to octocat, plus a manual history row,
	// must surface the author on both the current-build and recent-builds cards.
	status := map[string]any{
		"phase": "Ready",
		"build": map[string]any{
			"phase":       "Complete",
			"result":      "Succeeded",
			"jobName":     "mapswipe-uat-build-7",
			"trigger":     "Manual",
			"triggeredBy": "octocat",
		},
		"buildHistory": []any{
			map[string]any{"jobName": "mapswipe-uat-build-7", "result": "Succeeded", "trigger": "Manual", "triggeredBy": "octocat"},
		},
	}
	srv := New(k8s.NewWithDynamic(seededDyn(t, status)), &fakePodReader{}, &fakeLokiTailer{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/ns/mapswipe/app/mapswipe-uat", nil)
	req.SetPathValue("namespace", "mapswipe")
	req.SetPathValue("name", "mapswipe-uat")
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Manual · octocat") {
		t.Errorf("detail page should surface the manual trigger author; body=%s", body)
	}
	if strings.Count(body, "· octocat") < 2 {
		t.Errorf("author should appear on both current-build and recent-builds cards; body=%s", body)
	}
}

// runningBuildStatus is a current build mid-flight (no completionTime → live pod).
func runningBuildStatus() map[string]any {
	return map[string]any{
		"phase": "Building",
		"build": map[string]any{
			"phase":     "Running",
			"jobName":   "mapswipe-uat-build-8",
			"podName":   "mapswipe-uat-build-8-xyz",
			"startTime": "2026-06-25T10:00:00Z",
			"steps": []any{
				map[string]any{"name": "clone", "status": "Succeeded"},
				map[string]any{"name": "build", "status": "Running"},
			},
		},
	}
}

// completedBuildStatus is a finished build with history (completionTime set).
func completedBuildStatus() map[string]any {
	return map[string]any{
		"phase": "Ready",
		"build": map[string]any{
			"phase":          "Complete",
			"result":         "Succeeded",
			"jobName":        "mapswipe-uat-build-9",
			"podName":        "mapswipe-uat-build-9-abc",
			"startTime":      "2026-06-25T09:00:00Z",
			"completionTime": "2026-06-25T09:05:00Z",
			"steps": []any{
				map[string]any{"name": "clone", "status": "Succeeded"},
				map[string]any{"name": "build", "status": "Succeeded"},
			},
		},
		"buildHistory": []any{
			map[string]any{
				"jobName": "mapswipe-uat-build-9", "result": "Succeeded", "trigger": "Scheduled",
				"podName": "mapswipe-uat-build-9-abc", "completionTime": "2026-06-25T09:05:00Z",
			},
			map[string]any{
				"jobName": "mapswipe-uat-build-8", "result": "Failed", "trigger": "Manual",
				"completionTime": "2026-06-24T09:05:00Z", "failedStep": "build",
			},
		},
	}
}

// oomBuildStatus is a failed build the operator terminated via the OOM killer,
// with a matching history row, so the OOM callout + history badge can be tested.
func oomBuildStatus() map[string]any {
	return map[string]any{
		"phase": "Degraded",
		"build": map[string]any{
			"phase":          "Failed",
			"result":         "Error",
			"jobName":        "mapswipe-uat-build-9",
			"failedStep":     "build",
			"completionTime": "2026-06-25T09:05:00Z",
			"termination": map[string]any{
				"reason":      "OOMKilled",
				"container":   "build",
				"exitCode":    int64(137),
				"memoryLimit": "256Mi",
				"finishedAt":  "2026-06-25T09:05:00Z",
			},
		},
		"buildHistory": []any{
			map[string]any{
				"jobName": "mapswipe-uat-build-9", "result": "Failed", "trigger": "Scheduled",
				"completionTime": "2026-06-25T09:05:00Z",
				"termination":    map[string]any{"reason": "OOMKilled", "container": "build"},
			},
		},
	}
}

func TestBuildCard_RendersOOMCalloutAndHistoryBadge(t *testing.T) {
	dyn := seededDyn(t, oomBuildStatus())
	srv := New(k8s.NewWithDynamic(dyn), &fakePodReader{}, &fakeLokiTailer{}, nil)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/partial", "mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// The loud callout carries the summary + the remediation hint.
	if !strings.Contains(body, "OOM Killed — the build step exceeded its 256Mi memory limit.") {
		t.Errorf("should render the OOM callout summary; body=%s", body)
	}
	if !strings.Contains(body, "spec.build.memoryLimit") {
		t.Errorf("should render the memoryLimit remediation hint; body=%s", body)
	}
	if !strings.Contains(body, "banner-degraded") {
		t.Errorf("OOM callout should reuse the banner-degraded styling; body=%s", body)
	}
	// The compact OOM badge appears on the OOM history row.
	if !strings.Contains(body, ">OOM<") {
		t.Errorf("history row should carry a compact OOM badge; body=%s", body)
	}
}

func TestBuildCard_NonOOMFailureHasNoOOMCallout(t *testing.T) {
	dyn := seededDyn(t, completedBuildStatus()) // history has a plain Failed row, no termination
	srv := New(k8s.NewWithDynamic(dyn), &fakePodReader{}, &fakeLokiTailer{}, nil)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/partial", "mapswipe", "mapswipe-uat")
	body := rec.Body.String()
	if strings.Contains(body, "OOM Killed") {
		t.Errorf("non-OOM build must not render the OOM callout; body=%s", body)
	}
	if strings.Contains(body, ">OOM<") {
		t.Errorf("non-OOM history rows must not carry an OOM badge; body=%s", body)
	}
}

func doGet(srv *Server, path, ns, name string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.SetPathValue("namespace", ns)
	req.SetPathValue("name", name)
	req.Header.Set("X-Auth-Request-User", "octocat")
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	return rec
}

// The container picker and ?container= param accept only real pod containers —
// the synthetic "release" step has no container, and an arbitrary value must
// not flow into a log query.
func TestContainerSelection_ExcludesReleaseAndRejectsUnknown(t *testing.T) {
	rec := view.Build{Steps: []view.Step{
		{Name: "clone", Status: "Succeeded"},
		{Name: "build", Status: "Failed"},
		{Name: "copier", Status: "Pending"},
		{Name: "release", Status: "Pending"},
	}}
	steps := containerSteps(rec)
	for _, s := range steps {
		if s.Name == "release" {
			t.Fatalf("release must be excluded from container steps: %+v", steps)
		}
	}
	if validContainer(steps, "release") {
		t.Error("release must not be a valid container")
	}
	if validContainer(steps, "bogus\"}") {
		t.Error("arbitrary value must be rejected")
	}
	if !validContainer(steps, "build") {
		t.Error("build is a real container and should be valid")
	}
	// Default favors the failed step.
	if got := defaultContainer(steps); got != "build" {
		t.Errorf("defaultContainer = %q, want build (the failed step)", got)
	}
}

func TestLogs_LivePodForRunningBuild(t *testing.T) {
	dyn := seededDyn(t, runningBuildStatus())
	pods := &fakePodReader{logLines: []string{"cloning repo", "yarn build"}}
	loki := &fakeLokiTailer{configured: true} // configured, but live pod wins
	srv := New(k8s.NewWithDynamic(dyn), pods, loki, nil)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/logs", "mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "yarn build") {
		t.Errorf("log pane should contain the live pod lines; body=%s", body)
	}
	if !strings.Contains(strings.ToLower(body), "live pod") {
		t.Errorf("source note should say live pod; body=%s", body)
	}
	// Default container for a running build is the running step ("build").
	if pods.lastLogContainer != "build" {
		t.Errorf("default container = %q, want build", pods.lastLogContainer)
	}
	if pods.lastLogPod != "mapswipe-uat-build-8-xyz" {
		t.Errorf("pod = %q", pods.lastLogPod)
	}
}

func TestLogs_LokiForCompletedBuild(t *testing.T) {
	dyn := seededDyn(t, completedBuildStatus())
	pods := &fakePodReader{}
	loki := &fakeLokiTailer{configured: true, lines: []string{"archived line 1", "archived line 2"}}
	srv := New(k8s.NewWithDynamic(dyn), pods, loki, nil)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/logs", "mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "archived line 2") {
		t.Errorf("should show Loki lines; body=%s", body)
	}
	if !strings.Contains(body, "Loki") {
		t.Errorf("source note should mention Loki; body=%s", body)
	}
}

func TestLogs_UnavailableWhenNoLokiAndNoPod(t *testing.T) {
	dyn := seededDyn(t, completedBuildStatus())
	// Loki not configured AND pod is gone → unavailable.
	pods := &fakePodReader{getErr: context.DeadlineExceeded}
	loki := &fakeLokiTailer{configured: false}
	srv := New(k8s.NewWithDynamic(dyn), pods, loki, nil)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/logs", "mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "unavailable") {
		t.Errorf("should render an unavailable note; body=%s", rec.Body.String())
	}
}

func TestLogs_SelectsHistoryBuildByJobName(t *testing.T) {
	dyn := seededDyn(t, completedBuildStatus())
	pods := &fakePodReader{}
	// Older history build (build-8) has no podName and Loki off → pod fallback
	// won't apply; but Loki configured → Loki used and scoped to that build.
	loki := &fakeLokiTailer{configured: true, lines: []string{"old build log"}}
	srv := New(k8s.NewWithDynamic(dyn), pods, loki, nil)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/logs?build=mapswipe-uat-build-8&container=build",
		"mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "old build log") {
		t.Errorf("should resolve the history build; body=%s", rec.Body.String())
	}
}

func TestLogs_FollowSelectsCurrentBuildAndActiveContainer(t *testing.T) {
	dyn := seededDyn(t, runningBuildStatus())
	pods := &fakePodReader{logLines: []string{"yarn build"}}
	loki := &fakeLokiTailer{configured: true}
	srv := New(k8s.NewWithDynamic(dyn), pods, loki, nil)

	// follow=1 ignores the &container=clone hint and chases the active step.
	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/logs?follow=1&container=clone",
		"mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Active container for the running build is the running step ("build").
	if pods.lastLogContainer != "build" {
		t.Errorf("follow container = %q, want build (active step)", pods.lastLogContainer)
	}
	if pods.lastLogPod != "mapswipe-uat-build-8-xyz" {
		t.Errorf("follow pod = %q, want current build pod", pods.lastLogPod)
	}
	// The follow checkbox must render checked.
	if !strings.Contains(body, "data-follow-toggle") || !strings.Contains(body, "checked") {
		t.Errorf("follow checkbox should render checked; body=%s", body)
	}
}

func TestLogs_FollowIgnoresBuildParamAndResolvesCurrent(t *testing.T) {
	dyn := seededDyn(t, completedBuildStatus())
	pods := &fakePodReader{}
	loki := &fakeLokiTailer{configured: true, lines: []string{"current build log"}}
	srv := New(k8s.NewWithDynamic(dyn), pods, loki, nil)

	// follow=1 must ignore the stale &build=...-8 and pin the CURRENT build (-9).
	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/logs?follow=1&build=mapswipe-uat-build-8",
		"mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-build="mapswipe-uat-build-9"`) {
		t.Errorf("follow should resolve the current build (-9); body=%s", body)
	}
}

func TestLogs_ManualBuildParamLeavesFollowOff(t *testing.T) {
	dyn := seededDyn(t, completedBuildStatus())
	pods := &fakePodReader{}
	loki := &fakeLokiTailer{configured: true, lines: []string{"old build log"}}
	srv := New(k8s.NewWithDynamic(dyn), pods, loki, nil)

	// No follow param: the history build (-8) must be returned, checkbox unchecked.
	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/logs?build=mapswipe-uat-build-8&container=build",
		"mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-build="mapswipe-uat-build-8"`) {
		t.Errorf("manual path should resolve the history build (-8); body=%s", body)
	}
	if strings.Contains(body, "checked") {
		t.Errorf("follow checkbox must NOT be checked on the manual path; body=%s", body)
	}
}

func TestLogs_RendersColoredLines(t *testing.T) {
	dyn := seededDyn(t, runningBuildStatus())
	pods := &fakePodReader{logLines: []string{"ERROR: boom", "cloning repo"}}
	loki := &fakeLokiTailer{configured: true}
	srv := New(k8s.NewWithDynamic(dyn), pods, loki, nil)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/logs", "mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "log-err") {
		t.Errorf("error line should render with the log-err class; body=%s", body)
	}
	if !strings.Contains(body, "log-ln") {
		t.Errorf("every line should be wrapped in a log-ln span; body=%s", body)
	}
}

func TestLogs_FollowToggleHiddenWhenIdle(t *testing.T) {
	dyn := seededDyn(t, completedBuildStatus()) // no build in flight
	pods := &fakePodReader{}
	loki := &fakeLokiTailer{configured: true, lines: []string{"archived"}}
	srv := New(k8s.NewWithDynamic(dyn), pods, loki, nil)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/logs", "mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "data-follow-toggle") {
		t.Errorf("follow toggle must be hidden when no build is active; body=%s", rec.Body.String())
	}
}

func TestLogs_FollowToggleShownWhenActive(t *testing.T) {
	dyn := seededDyn(t, runningBuildStatus())
	pods := &fakePodReader{logLines: []string{"yarn build"}}
	loki := &fakeLokiTailer{configured: true}
	srv := New(k8s.NewWithDynamic(dyn), pods, loki, nil)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/logs?follow=1", "mapswipe", "mapswipe-uat")
	if !strings.Contains(rec.Body.String(), "data-follow-toggle") {
		t.Errorf("follow toggle must be shown while a build is active; body=%s", rec.Body.String())
	}
}

func TestLogs_HistoricalBuildShowsViewingIndicator(t *testing.T) {
	dyn := seededDyn(t, completedBuildStatus())
	pods := &fakePodReader{}
	loki := &fakeLokiTailer{configured: true, lines: []string{"old build log"}}
	srv := New(k8s.NewWithDynamic(dyn), pods, loki, nil)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/logs?build=mapswipe-uat-build-8&container=build",
		"mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "data-viewing") {
		t.Errorf("historical build should render a 'viewing' indicator; body=%s", body)
	}
}

func TestRecentBuilds_RendersViewLogsButtonNotDetails(t *testing.T) {
	dyn := seededDyn(t, completedBuildStatus())
	pods := &fakePodReader{}
	loki := &fakeLokiTailer{configured: true}
	srv := New(k8s.NewWithDynamic(dyn), pods, loki, nil)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/partial", "mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Recent builds load into the single pane via a button, not an inline expand.
	if !strings.Contains(body, `data-view-logs="mapswipe-uat-build-8"`) {
		t.Errorf("recent build should render a View logs button; body=%s", body)
	}
	if strings.Contains(body, "data-logs-build") {
		t.Errorf("inline <details> logs loader must be gone; body=%s", body)
	}
	// The row must be addressable for selection highlighting.
	if !strings.Contains(body, `data-build-row="mapswipe-uat-build-8"`) {
		t.Errorf("recent build row should carry data-build-row; body=%s", body)
	}
}

// podWithLimits builds a pod whose named container carries cpu+mem limits, so
// the metrics bars can be computed against a known cap.
func podWithLimits(podName, container, cpu, mem string) *corev1.Pod {
	lim := corev1.ResourceList{}
	if cpu != "" {
		lim[corev1.ResourceCPU] = apiresource.MustParse(cpu)
	}
	if mem != "" {
		lim[corev1.ResourceMemory] = apiresource.MustParse(mem)
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: podName},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: container, Resources: corev1.ResourceRequirements{Limits: lim}},
			},
		},
	}
}

func TestPartial_LiveMetricsWithBars(t *testing.T) {
	dyn := seededDyn(t, runningBuildStatus())
	pods := &fakePodReader{pod: podWithLimits("mapswipe-uat-build-8-xyz", "build", "2", "4Gi")}
	metrics := &fakeMetricser{usage: map[string]k8s.ContainerUsage{
		"build": {CPUMillicores: 1500, MemoryBytes: 2 * 1024 * 1024 * 1024},
	}}
	srv := New(k8s.NewWithDynamic(dyn), pods, &fakeLokiTailer{}, metrics)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/partial", "mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(strings.ToLower(body), "live usage") {
		t.Errorf("should render a Live usage block; body=%s", body)
	}
	if !strings.Contains(body, "1.50 cores") {
		t.Errorf("should show CPU usage 1.50 cores; body=%s", body)
	}
	if !strings.Contains(body, "2.0 GiB") {
		t.Errorf("should show memory usage 2.0 GiB; body=%s", body)
	}
	// CPU bar: 1500m of 2000m = 75%. Mem bar: 2Gi of 4Gi = 50%.
	if !strings.Contains(body, "bar-fill") {
		t.Errorf("should render usage bars when limits known; body=%s", body)
	}
	if !strings.Contains(body, "75%") || !strings.Contains(body, "50%") {
		t.Errorf("bar widths should reflect limits (75%% cpu, 50%% mem); body=%s", body)
	}
}

func TestPartial_LiveMetricsNoBarWhenNoLimit(t *testing.T) {
	dyn := seededDyn(t, runningBuildStatus())
	// Pod has the container but no limits → values shown, no bar.
	pods := &fakePodReader{pod: podWithLimits("mapswipe-uat-build-8-xyz", "build", "", "")}
	metrics := &fakeMetricser{usage: map[string]k8s.ContainerUsage{
		"build": {CPUMillicores: 350, MemoryBytes: 512 * 1024 * 1024},
	}}
	srv := New(k8s.NewWithDynamic(dyn), pods, &fakeLokiTailer{}, metrics)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/partial", "mapswipe", "mapswipe-uat")
	body := rec.Body.String()
	if !strings.Contains(body, "350m") {
		t.Errorf("should show CPU usage 350m; body=%s", body)
	}
	if strings.Contains(body, "bar-fill") {
		t.Errorf("must NOT render a bar when no limit known; body=%s", body)
	}
}

func TestPartial_IdleAppFetchesNoMetrics(t *testing.T) {
	dyn := seededDyn(t, completedBuildStatus())
	pods := &fakePodReader{}
	metrics := &fakeMetricser{usage: map[string]k8s.ContainerUsage{
		"build": {CPUMillicores: 1, MemoryBytes: 1},
	}}
	srv := New(k8s.NewWithDynamic(dyn), pods, &fakeLokiTailer{}, metrics)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/partial", "mapswipe", "mapswipe-uat")
	body := rec.Body.String()
	if metrics.calls != 0 {
		t.Errorf("idle app must not fetch metrics; calls=%d", metrics.calls)
	}
	if strings.Contains(strings.ToLower(body), "live usage") {
		t.Errorf("idle app must not render a usage block; body=%s", body)
	}
	if strings.Contains(strings.ToLower(body), "metrics unavailable") {
		t.Errorf("idle app must not render an unavailable note; body=%s", body)
	}
}

func TestPartial_MetricsErrorRendersNote(t *testing.T) {
	dyn := seededDyn(t, runningBuildStatus())
	pods := &fakePodReader{}
	metrics := &fakeMetricser{err: context.DeadlineExceeded}
	srv := New(k8s.NewWithDynamic(dyn), pods, &fakeLokiTailer{}, metrics)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/partial", "mapswipe", "mapswipe-uat")
	body := rec.Body.String()
	if metrics.calls != 1 {
		t.Errorf("live build should attempt one metrics fetch; calls=%d", metrics.calls)
	}
	if strings.Contains(body, "bar-fill") {
		t.Errorf("no usage block on error; body=%s", body)
	}
	if !strings.Contains(strings.ToLower(body), "metrics unavailable") {
		t.Errorf("should render a muted unavailable note; body=%s", body)
	}
}

func TestPartial_MetricsRespectsBoundedTimeout(t *testing.T) {
	dyn := seededDyn(t, runningBuildStatus())
	pods := &fakePodReader{}
	// block honors ctx.Done(); the handler's bounded context must cancel it so the
	// fragment returns promptly with the note rather than hanging.
	metrics := &fakeMetricser{block: true}
	srv := New(k8s.NewWithDynamic(dyn), pods, &fakeLokiTailer{}, metrics)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- doGet(srv, "/ns/mapswipe/app/mapswipe-uat/partial", "mapswipe", "mapswipe-uat")
	}()
	select {
	case rec := <-done:
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(strings.ToLower(rec.Body.String()), "metrics unavailable") {
			t.Errorf("timed-out fetch should degrade to the note; body=%s", rec.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not return promptly under a blocking metrics fetch")
	}
}

func TestPartial_RendersLiveRegionFragmentNotFullPage(t *testing.T) {
	dyn := seededDyn(t, completedBuildStatus())
	srv := New(k8s.NewWithDynamic(dyn), &fakePodReader{}, &fakeLokiTailer{}, nil)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/partial", "mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Fragment, not a full page: no <html> doctype.
	if strings.Contains(body, "<!doctype html>") {
		t.Error("partial should be a fragment, not a full page")
	}
	// Should contain the recent-builds content (a history job name).
	if !strings.Contains(body, "mapswipe-uat-build-9") {
		t.Errorf("partial should show recent builds; body=%s", body)
	}
}

func TestPartial_EmitsBuildActiveDataAttr(t *testing.T) {
	// Building → data-build-active="1".
	dynB := seededDyn(t, runningBuildStatus())
	srvB := New(k8s.NewWithDynamic(dynB), &fakePodReader{}, &fakeLokiTailer{}, nil)
	recB := doGet(srvB, "/ns/mapswipe/app/mapswipe-uat/partial", "mapswipe", "mapswipe-uat")
	if !strings.Contains(recB.Body.String(), `data-build-active="1"`) {
		t.Errorf("building partial should emit data-build-active=1; body=%s", recB.Body.String())
	}

	// Idle → data-build-active="0".
	dynI := seededDyn(t, completedBuildStatus())
	srvI := New(k8s.NewWithDynamic(dynI), &fakePodReader{}, &fakeLokiTailer{}, nil)
	recI := doGet(srvI, "/ns/mapswipe/app/mapswipe-uat/partial", "mapswipe", "mapswipe-uat")
	if !strings.Contains(recI.Body.String(), `data-build-active="0"`) {
		t.Errorf("idle partial should emit data-build-active=0; body=%s", recI.Body.String())
	}
}

func TestDetail_EmbedsLiveRegionAndPoller(t *testing.T) {
	dyn := seededDyn(t, completedBuildStatus())
	srv := New(k8s.NewWithDynamic(dyn), &fakePodReader{}, &fakeLokiTailer{}, nil)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat", "mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="live-region"`) {
		t.Error("detail should contain the pollable #live-region")
	}
	if !strings.Contains(body, "/partial") {
		t.Error("detail poller should target the /partial endpoint")
	}
	if !strings.Contains(body, "/logs") {
		t.Error("detail should reference the /logs endpoint")
	}
}

func TestDetail_NotFoundOnMissingApp(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := doGet(srv, "/ns/mapswipe/app/does-not-exist", "mapswipe", "does-not-exist")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHealthz(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Errorf("healthz = %d %q", rec.Code, rec.Body.String())
	}
}
