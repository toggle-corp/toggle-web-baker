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
	return New(k8s.NewWithDynamic(dyn), &fakePodReader{}, &fakeLokiTailer{}), dyn
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

func doGet(srv *Server, path, ns, name string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.SetPathValue("namespace", ns)
	req.SetPathValue("name", name)
	req.Header.Set("X-Auth-Request-User", "octocat")
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	return rec
}

func TestLogs_LivePodForRunningBuild(t *testing.T) {
	dyn := seededDyn(t, runningBuildStatus())
	pods := &fakePodReader{logLines: []string{"cloning repo", "yarn build"}}
	loki := &fakeLokiTailer{configured: true} // configured, but live pod wins
	srv := New(k8s.NewWithDynamic(dyn), pods, loki)

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
	srv := New(k8s.NewWithDynamic(dyn), pods, loki)

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
	srv := New(k8s.NewWithDynamic(dyn), pods, loki)

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
	srv := New(k8s.NewWithDynamic(dyn), pods, loki)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/logs?build=mapswipe-uat-build-8&container=build",
		"mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "old build log") {
		t.Errorf("should resolve the history build; body=%s", rec.Body.String())
	}
}

func TestPartial_RendersLiveRegionFragmentNotFullPage(t *testing.T) {
	dyn := seededDyn(t, completedBuildStatus())
	srv := New(k8s.NewWithDynamic(dyn), &fakePodReader{}, &fakeLokiTailer{})

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

func TestDetail_EmbedsLiveRegionAndPoller(t *testing.T) {
	dyn := seededDyn(t, completedBuildStatus())
	srv := New(k8s.NewWithDynamic(dyn), &fakePodReader{}, &fakeLokiTailer{})

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
