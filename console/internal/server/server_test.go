package server

import (
	"context"
	"fmt"
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
	"k8s.io/client-go/dynamic"
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
// deadline so the bounded-timeout path can be exercised. calls counts
// invocations; lastNode records the kubelet the server asked for.
type fakeMetricser struct {
	usage map[string]k8s.ContainerUsage
	err   error
	block bool

	calls    int
	lastNode string
}

func (f *fakeMetricser) PodMetrics(ctx context.Context, node, _, _ string) (map[string]k8s.ContainerUsage, error) {
	f.calls++
	f.lastNode = node
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

// fakeLister implements k8s.FrontendAppPatcher with configurable Synced/Stale so
// the warming and staleness rendering paths can be driven deterministically
// (the real Client always syncs and is never stale in tests). Only List/Synced/
// Stale are exercised by the list handler; the write/get methods are stubs.
type fakeLister struct {
	apps   []view.App
	synced bool
	stale  bool
}

func (f *fakeLister) List(context.Context) ([]view.App, error) { return f.apps, nil }
func (f *fakeLister) Synced() bool                             { return f.synced }
func (f *fakeLister) Stale() bool                              { return f.stale }
func (f *fakeLister) Get(context.Context, string, string) (*unstructured.Unstructured, error) {
	return nil, fmt.Errorf("not implemented")
}
func (f *fakeLister) RequestRebuild(context.Context, string, string, string) error { return nil }
func (f *fakeLister) RequestCleanupCache(context.Context, string, string, string) error {
	return nil
}
func (f *fakeLister) RequestCleanupReleases(context.Context, string, string, string) error {
	return nil
}

// newClient wraps k8s.NewWithDynamic and registers Close via t.Cleanup so the
// informer goroutine it starts does not leak past the test. Every server-test
// helper (and inline server build) constructs its client through this so no test
// forgets the cleanup.
func newClient(t *testing.T, dyn dynamic.Interface) *k8s.Client {
	t.Helper()
	c := k8s.NewWithDynamic(dyn)
	t.Cleanup(c.Close)
	return c
}

// newTestServer wires the real Client over a fake dynamic client seeded with
// one FrontendApp, so the handlers exercise the actual list/get/patch paths.
// The pod reader and loki tailer default to harmless fakes; tests that need
// specific log behaviour build a server directly with New.
func newTestServer(t *testing.T) (*Server, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	dyn := seededDyn(t, nil)
	return New(newClient(t, dyn), &fakePodReader{}, &fakeLokiTailer{}, nil), dyn
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

// The prune buttons live in the detail page's sticky status bar (the storage
// card keeps only the prune STATUS rows). Busy state disables both buttons.
func TestStatusBar_RendersCleanupButtonsDisabledWhenBusy(t *testing.T) {
	dyn := seededDyn(t, cleanupBusyStatus())
	srv := New(newClient(t, dyn), &fakePodReader{}, &fakeLokiTailer{}, nil)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat", "mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/cleanup-cache") || !strings.Contains(body, "/cleanup-releases") {
		t.Errorf("status bar should render both cleanup forms; body=%s", body)
	}
	if !strings.Contains(body, `btn-danger" disabled`) {
		t.Errorf("cleanup buttons should be disabled while busy; body=%s", body)
	}
	// Danger styling on the destructive actions.
	if !strings.Contains(body, "btn-danger") {
		t.Errorf("prune buttons should carry the danger variant; body=%s", body)
	}
}

func TestStatusBar_CleanupButtonsEnabledWhenIdle(t *testing.T) {
	dyn := seededDyn(t, completedBuildStatus())
	srv := New(newClient(t, dyn), &fakePodReader{}, &fakeLokiTailer{}, nil)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat", "mapswipe", "mapswipe-uat")
	body := rec.Body.String()
	if !strings.Contains(body, "/cleanup-cache") {
		t.Errorf("idle detail page should still render cleanup forms; body=%s", body)
	}
	if strings.Contains(body, `btn-danger" disabled`) {
		t.Errorf("cleanup buttons must NOT be disabled when idle; body=%s", body)
	}
}

func TestStorageCard_RendersPruneStatusRowsWithoutForms(t *testing.T) {
	dyn := seededDyn(t, cleanupBusyStatus())
	srv := New(newClient(t, dyn), &fakePodReader{}, &fakeLokiTailer{}, nil)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/partial", "mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Cleanup status (reclaimed/message/phase) stays on the storage card…
	if !strings.Contains(body, "pruned layers") {
		t.Errorf("should render the cleanup message; body=%s", body)
	}
	if !strings.Contains(body, view.HumanizeBytes(1048576)) {
		t.Errorf("should render reclaimed bytes humanized; body=%s", body)
	}
	// …but the forms moved to the status bar, out of the fragment.
	if strings.Contains(body, "/cleanup-cache") || strings.Contains(body, "<form") {
		t.Errorf("the fragment must not carry cleanup forms anymore; body=%s", body)
	}
}

// fappObj builds one FrontendApp unstructured object for multi-app list tests.
func fappObj(ns, name string, spec, status map[string]any) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": "baker.toggle-corp.com/v1alpha1",
		"kind":       "FrontendApp",
		"metadata":   map[string]any{"namespace": ns, "name": name},
	}
	if spec != nil {
		obj["spec"] = spec
	}
	if status != nil {
		obj["status"] = status
	}
	return &unstructured.Unstructured{Object: obj}
}

// listFixtureServer seeds three apps covering the filter axes:
//   - alpha/web-degraded  group=grp-a  degraded, failed build, STALE, storage Critical
//   - beta/web-ready      group=grp-b  ready, storage Alert
//   - gamma/web-ungrouped (no group)   no status → pending
func listFixtureServer(t *testing.T) *Server {
	t.Helper()
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{k8s.GVR: "FrontendAppList"}
	degraded := fappObj("alpha", "web-degraded",
		map[string]any{"group": "grp-a"},
		map[string]any{
			"phase":     "Degraded",
			"url":       "https://degraded.example.org/some/path",
			"specStale": true,
			"conditions": []any{
				map[string]any{"type": "Degraded", "status": "True"},
			},
			"build": map[string]any{
				"phase": "Failed", "result": "Failed", "jobName": "web-degraded-b-1",
				"failedStep": "build",
				"steps": []any{
					map[string]any{"name": "clone", "status": "Succeeded"},
					map[string]any{"name": "build", "status": "Failed"},
				},
			},
			"lastBuildTime":           "2026-07-01T09:00:00Z",
			"lastSuccessfulBuildTime": "2026-06-30T09:00:00Z",
			"storage":                 map[string]any{"thresholdState": "Critical"},
		})
	ready := fappObj("beta", "web-ready",
		map[string]any{"group": "grp-b"},
		map[string]any{
			"phase": "Ready",
			"url":   "https://ready.example.org",
			"conditions": []any{
				map[string]any{"type": "Ready", "status": "True"},
			},
			"build": map[string]any{
				"phase": "Complete", "result": "Succeeded", "jobName": "web-ready-b-1",
				"steps": []any{map[string]any{"name": "build", "status": "Succeeded"}},
			},
			"lastBuildTime": "2026-07-02T03:00:00Z",
			"storage":       map[string]any{"thresholdState": "Alert"},
		})
	ungrouped := fappObj("gamma", "web-ungrouped", nil, nil)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, degraded, ready, ungrouped)
	return New(newClient(t, dyn), &fakePodReader{}, &fakeLokiTailer{}, nil)
}

func getList(srv *Server, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("X-Auth-Request-User", "octocat")
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	return rec
}

func TestList_StatusFacetCountsFromUnfilteredSet(t *testing.T) {
	srv := listFixtureServer(t)

	rec := getList(srv, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"all (3)", "ready (1)", "building (0)", "degraded (1)", "serving last-good (0)", "pending (1)"} {
		if !strings.Contains(body, want) {
			t.Errorf("facets should contain %q; body=%s", want, body)
		}
	}

	// Filtering must not change the counts (they come from the unfiltered set).
	rec = getList(srv, "/?status=degraded")
	body = rec.Body.String()
	if !strings.Contains(body, "web-degraded") {
		t.Errorf("degraded filter should keep the degraded app; body=%s", body)
	}
	if strings.Contains(body, "web-ready") || strings.Contains(body, "web-ungrouped") {
		t.Errorf("degraded filter should drop other apps; body=%s", body)
	}
	if !strings.Contains(body, "all (3)") || !strings.Contains(body, "ready (1)") {
		t.Errorf("facet counts must stay unfiltered; body=%s", body)
	}
	// With a filter active the heading reads "matched of total" — the count now
	// describes the same (filtered) set the storage roll-up spans.
	if !strings.Contains(body, "· 1 of 3") {
		t.Errorf("filtered heading should show matched-of-total; body=%s", body)
	}
}

func TestList_HeadingCountUnfilteredWhenNoFilter(t *testing.T) {
	srv := listFixtureServer(t)
	body := getList(srv, "/").Body.String()
	// No filter/search: the count is just the total, no "of".
	if !strings.Contains(body, "· 3") {
		t.Errorf("unfiltered heading should show the bare total; body=%s", body)
	}
	if strings.Contains(body, " of 3") {
		t.Errorf("unfiltered heading must not show 'of'; body=%s", body)
	}
}

func TestList_HeadingCountFilteredByGroup(t *testing.T) {
	srv := listFixtureServer(t)
	// group=grp-a matches 1 of the 3 apps; heading reads "1 of 3".
	body := getList(srv, "/?group=grp-a").Body.String()
	if !strings.Contains(body, "· 1 of 3") {
		t.Errorf("group filter heading should show matched-of-total; body=%s", body)
	}
}

func TestList_UnknownStatusParamIsIgnored(t *testing.T) {
	srv := listFixtureServer(t)
	body := getList(srv, "/?status=bogus").Body.String()
	for _, name := range []string{"web-degraded", "web-ready", "web-ungrouped"} {
		if !strings.Contains(body, name) {
			t.Errorf("bogus status should be ignored (all rows); missing %s; body=%s", name, body)
		}
	}
}

func TestList_GroupChipsAndFilter(t *testing.T) {
	srv := listFixtureServer(t)

	body := getList(srv, "/").Body.String()
	if !strings.Contains(body, "Filter by group") {
		t.Fatalf("group chip row should render; body=%s", body)
	}
	for _, want := range []string{">grp-a</a>", ">grp-b</a>", ">ungrouped</a>"} {
		if !strings.Contains(body, want) {
			t.Errorf("group chips should contain %q; body=%s", want, body)
		}
	}

	// Group filter keeps only that group's apps.
	body = getList(srv, "/?group=grp-a").Body.String()
	if !strings.Contains(body, "web-degraded") || strings.Contains(body, "web-ready") {
		t.Errorf("group=grp-a should keep only grp-a apps; body=%s", body)
	}
	// The ungrouped sentinel selects apps WITHOUT a group.
	body = getList(srv, "/?group=ungrouped").Body.String()
	if !strings.Contains(body, "web-ungrouped") || strings.Contains(body, "web-degraded") {
		t.Errorf("group=ungrouped should keep only group-less apps; body=%s", body)
	}

	// Group + status compose (server-side).
	body = getList(srv, "/?group=grp-a&status=ready").Body.String()
	if strings.Contains(body, "web-degraded") || strings.Contains(body, "web-ready") {
		t.Errorf("composed filters should exclude everything here; body=%s", body)
	}
	if !strings.Contains(body, "No FrontendApps match") {
		t.Errorf("empty composed filter should render the empty row; body=%s", body)
	}
	// Facet links carry the active group so the params keep composing.
	if !strings.Contains(body, "group=grp-a&amp;status=degraded") {
		t.Errorf("status facet URLs should preserve the group param; body=%s", body)
	}
}

func TestList_SearchMatchesName(t *testing.T) {
	srv := listFixtureServer(t)
	// "ungrouped" is a substring of the web-ungrouped name; the other rows drop.
	body := getList(srv, "/?search=ungrouped").Body.String()
	if !strings.Contains(body, "web-ungrouped") {
		t.Errorf("search should keep the matching app; body=%s", body)
	}
	if strings.Contains(body, "web-degraded") || strings.Contains(body, "web-ready") {
		t.Errorf("search should drop non-matching apps; body=%s", body)
	}
}

func TestList_SearchMatchesNamespace(t *testing.T) {
	srv := listFixtureServer(t)
	// "beta" is the namespace of web-ready only.
	body := getList(srv, "/?search=beta").Body.String()
	if !strings.Contains(body, "web-ready") {
		t.Errorf("search should match on namespace; body=%s", body)
	}
	if strings.Contains(body, "web-degraded") || strings.Contains(body, "web-ungrouped") {
		t.Errorf("namespace search should drop other apps; body=%s", body)
	}
}

func TestList_SearchMatchesGroup(t *testing.T) {
	srv := listFixtureServer(t)
	// "grp-b" is the group of web-ready only.
	body := getList(srv, "/?search=grp-b").Body.String()
	if !strings.Contains(body, "web-ready") {
		t.Errorf("search should match on group; body=%s", body)
	}
	if strings.Contains(body, "web-degraded") || strings.Contains(body, "web-ungrouped") {
		t.Errorf("group search should drop other apps; body=%s", body)
	}
}

func TestList_SearchMatchesURLHost(t *testing.T) {
	srv := listFixtureServer(t)
	// Case-insensitive host match: web-degraded's host is degraded.example.org.
	body := getList(srv, "/?search=DEGRADED.EXAMPLE").Body.String()
	if !strings.Contains(body, "web-degraded") {
		t.Errorf("search should match on URL host (case-insensitive); body=%s", body)
	}
	if strings.Contains(body, "web-ready") || strings.Contains(body, "web-ungrouped") {
		t.Errorf("URL-host search should drop other apps; body=%s", body)
	}
}

func TestList_SearchComposesWithStatusFilter(t *testing.T) {
	srv := listFixtureServer(t)
	// "web" matches every name; status=ready narrows to web-ready (AND).
	body := getList(srv, "/?search=web&status=ready").Body.String()
	if !strings.Contains(body, "web-ready") {
		t.Errorf("search+status should keep the ready app; body=%s", body)
	}
	if strings.Contains(body, "web-degraded") || strings.Contains(body, "web-ungrouped") {
		t.Errorf("search+status should drop non-ready apps; body=%s", body)
	}
}

// TestSearchApps_PerFieldNotCrossField pins the FIX-2 behaviour: a multi-word
// term must match WITHIN a single field, never across the field boundary of the
// joined name+ns+group+host haystack. "web prod" must NOT match an app whose
// name ends "-web" and whose namespace starts "prod-" (the old joined-haystack
// matched it via "…-web prod-…"); a term truly contained in one field still
// matches.
func TestSearchApps_PerFieldNotCrossField(t *testing.T) {
	apps := []view.App{
		{Name: "mapswipe-web", Namespace: "prod-x", Group: "g", URL: "https://a.example.org"},
		{Name: "other", Namespace: "prod-x", Group: "web prod team", URL: "https://b.example.org"},
	}

	// Cross-field: "web prod" spans name→namespace only in the joined haystack.
	got := searchApps(apps, "web prod")
	for _, a := range got {
		if a.Name == "mapswipe-web" {
			t.Errorf("cross-field term must NOT match mapswipe-web; got %+v", got)
		}
	}
	// But a term wholly inside one field (the group "web prod team") still matches.
	if len(got) != 1 || got[0].Name != "other" {
		t.Errorf("term contained in one field should match that app only; got %+v", got)
	}

	// A single-field substring still matches (case-insensitive).
	if out := searchApps(apps, "MAPSWIPE"); len(out) != 1 || out[0].Name != "mapswipe-web" {
		t.Errorf("single-field substring should match; got %+v", out)
	}
}

func TestList_ChipURLsPreserveSearch(t *testing.T) {
	srv := listFixtureServer(t)
	body := getList(srv, "/?search=web").Body.String()
	// Status facet + group chip links must keep the active search term so
	// clicking a chip does not drop the search.
	if !strings.Contains(body, "search=web") {
		t.Errorf("chip URLs should preserve the search term; body=%s", body)
	}
	if !strings.Contains(body, "search=web&amp;status=degraded") {
		t.Errorf("status facet URLs should carry search; body=%s", body)
	}
	if !strings.Contains(body, "group=grp-a&amp;search=web") {
		t.Errorf("group chip URLs should carry search; body=%s", body)
	}
}

func TestList_FacetCountsUnchangedBySearch(t *testing.T) {
	srv := listFixtureServer(t)
	// A narrowing search must not change the facet counts or heading total —
	// they come from the population unfiltered by search.
	body := getList(srv, "/?search=ungrouped").Body.String()
	if !strings.Contains(body, "web-ungrouped") || strings.Contains(body, "web-ready") {
		t.Fatalf("search should have narrowed to web-ungrouped; body=%s", body)
	}
	for _, want := range []string{"all (3)", "ready (1)", "degraded (1)", "pending (1)"} {
		if !strings.Contains(body, want) {
			t.Errorf("facet counts should stay unfiltered by search: missing %q; body=%s", want, body)
		}
	}
	// The count switches to "matched of total" under search — but Total (3) is
	// preserved (search narrows to 1 of 3), and the facet counts above stay
	// unfiltered. This is the FIX-1 consistency: count and storage span one set.
	if !strings.Contains(body, "· 1 of 3") {
		t.Errorf("search heading should show matched-of-total (Total preserved); body=%s", body)
	}
	// The group chips must still all render (computed pre-search).
	for _, want := range []string{">grp-a</a>", ">grp-b</a>"} {
		if !strings.Contains(body, want) {
			t.Errorf("group chips should stay present under search: missing %q; body=%s", want, body)
		}
	}
}

func TestList_SearchInputAndClearLink(t *testing.T) {
	srv := listFixtureServer(t)

	// No search active: input is empty, no clear link.
	body := getList(srv, "/").Body.String()
	if !strings.Contains(body, `name="search"`) {
		t.Errorf("list should render a search input; body=%s", body)
	}
	if !strings.Contains(body, "search name, ns, group, url") {
		t.Errorf("search input should carry the placeholder; body=%s", body)
	}
	if strings.Contains(body, "✕ clear") {
		t.Errorf("clear link must be hidden when no search is active; body=%s", body)
	}

	// Search active: value persists, clear link appears keeping status/group.
	body = getList(srv, "/?search=web&status=ready").Body.String()
	if !strings.Contains(body, `value="web"`) {
		t.Errorf("search input should echo the active term; body=%s", body)
	}
	if !strings.Contains(body, "✕ clear") {
		t.Errorf("clear link should appear when a search is active; body=%s", body)
	}
	// The clear link drops search but keeps status.
	if !strings.Contains(body, `href="/?status=ready"`) {
		t.Errorf("clear link should keep status and drop search; body=%s", body)
	}
	// Status is carried as a hidden input so submitting a search preserves it.
	if !strings.Contains(body, `type="hidden" name="status" value="ready"`) {
		t.Errorf("form should carry status as a hidden input; body=%s", body)
	}
}

func TestList_EmptyStateQuotesSearchTerm(t *testing.T) {
	srv := listFixtureServer(t)

	// A search matching nothing shows the quoted-term empty state.
	body := getList(srv, "/?search=zzznope").Body.String()
	if !strings.Contains(body, `No FrontendApps match "zzznope".`) {
		t.Errorf("empty state should quote the search term; body=%s", body)
	}

	// A composed filter with no search keeps the plain empty message.
	body = getList(srv, "/?group=grp-a&status=ready").Body.String()
	if !strings.Contains(body, "No FrontendApps match.") {
		t.Errorf("no-search empty state should stay plain; body=%s", body)
	}
}

func TestList_GroupChipsHiddenWhenNoGroups(t *testing.T) {
	srv, _ := newTestServer(t) // single seeded app without spec.group
	body := getList(srv, "/").Body.String()
	if strings.Contains(body, "Filter by group") {
		t.Errorf("group chip row must be hidden when no app carries a group; body=%s", body)
	}
}

func TestList_SortsMostBrokenFirst(t *testing.T) {
	srv := listFixtureServer(t)
	body := getList(srv, "/").Body.String()
	iDegraded := strings.Index(body, "web-degraded")
	iPending := strings.Index(body, "web-ungrouped") // pending (no status)
	iReady := strings.Index(body, "web-ready")
	if iDegraded < 0 || iPending < 0 || iReady < 0 {
		t.Fatalf("all three rows should render; body=%s", body)
	}
	if iDegraded >= iPending || iPending >= iReady {
		t.Errorf("rows should sort degraded < pending < ready; got %d/%d/%d", iDegraded, iPending, iReady)
	}
	// Degraded rows carry the background tint, not a side stripe.
	if !strings.Contains(body, "tint-degraded") {
		t.Errorf("degraded row should carry tint-degraded; body=%s", body)
	}
	if strings.Contains(body, "row-degraded") {
		t.Errorf("side-stripe row classes must be gone; body=%s", body)
	}
}

func TestList_RowBadgesColumnsAndFlow(t *testing.T) {
	srv := listFixtureServer(t)
	body := getList(srv, "/").Body.String()

	// Storage badges: Critical → red, Alert → amber.
	if !strings.Contains(body, "STORAGE CRITICAL") || !strings.Contains(body, "STORAGE ALERT") {
		t.Errorf("storage threshold badges should render; body=%s", body)
	}
	if !strings.Contains(body, ">STALE<") {
		t.Errorf("STALE badge should render on the stale app; body=%s", body)
	}
	// URL cell: host only, full URL in the title attribute.
	if !strings.Contains(body, "degraded.example.org ↗") {
		t.Errorf("URL cell should show the bare host with ↗; body=%s", body)
	}
	if !strings.Contains(body, `title="https://degraded.example.org/some/path"`) {
		t.Errorf("full URL should live in the title attr; body=%s", body)
	}
	// Group shows as small text in the Name cell.
	if !strings.Contains(body, "alpha · grp-a") {
		t.Errorf("Name cell should show namespace · group; body=%s", body)
	}
	// Next build column renders for every row (from spec.scheduledBuilds when
	// enabled with an explicit schedule; em-dash otherwise — the console never
	// guesses the operator's default).
	if !strings.Contains(body, "Next build") {
		t.Errorf("list should have a Next build column; body=%s", body)
	}
	// The flow strip renders only on the failed row, not the healthy one.
	if !strings.Contains(body, "step-failed") {
		t.Errorf("failed row should carry its flow strip; body=%s", body)
	}
	if got := strings.Count(body, `<div class="flow">`); got != 1 {
		t.Errorf("only the failed/active rows get a flow strip; got %d strips; body=%s", got, body)
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

// When the cache is not synced yet, the list page must show the warming notice
// and NOT the table nor the empty-cluster "No FrontendApps match." row (which
// would look like a healthy empty cluster).
func TestList_WarmingWhenNotSynced(t *testing.T) {
	srv := New(&fakeLister{synced: false}, &fakePodReader{}, &fakeLokiTailer{}, nil)
	rec := getList(srv, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Cache warming up") {
		t.Errorf("warming page should show the warming notice; body=%s", body)
	}
	if strings.Contains(body, "No FrontendApps match") {
		t.Errorf("warming page must not show the empty-cluster row; body=%s", body)
	}
	if strings.Contains(body, "<table>") {
		t.Errorf("warming page must not render the table; body=%s", body)
	}
}

// When synced and not stale, the list renders the normal table with no warming
// notice and no staleness banner.
func TestList_SyncedNormalNoBanners(t *testing.T) {
	srv := New(&fakeLister{synced: true, apps: []view.App{{Namespace: "ns", Name: "app-a"}}},
		&fakePodReader{}, &fakeLokiTailer{}, nil)
	rec := getList(srv, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "app-a") {
		t.Errorf("synced list should render the app row; body=%s", body)
	}
	if strings.Contains(body, "Cache warming up") {
		t.Errorf("synced list must not show the warming notice; body=%s", body)
	}
	if strings.Contains(body, "may be out of date") {
		t.Errorf("synced, non-stale list must not show the staleness banner; body=%s", body)
	}
}

// When synced but stale, the list renders the staleness banner above the table,
// with the table still present.
func TestList_StaleBannerAboveTable(t *testing.T) {
	srv := New(&fakeLister{synced: true, stale: true, apps: []view.App{{Namespace: "ns", Name: "app-a"}}},
		&fakePodReader{}, &fakeLokiTailer{}, nil)
	rec := getList(srv, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	banner := "may be out of date"
	if !strings.Contains(body, banner) {
		t.Errorf("stale list should show the staleness banner; body=%s", body)
	}
	if !strings.Contains(body, "app-a") {
		t.Errorf("stale list should still render the table rows; body=%s", body)
	}
	if strings.Index(body, banner) > strings.Index(body, "app-a") {
		t.Errorf("staleness banner should appear above the table; body=%s", body)
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

// The detail page is the cockpit layout: a sticky status bar (breadcrumb,
// health badge, warning badges, actions) instead of the standard header, a
// left column ending in the Internals disclosure, and the docked log pane.
// URL / phase / group moved OFF the bar into the Build details card.
func TestDetail_CockpitLayout(t *testing.T) {
	status := map[string]any{
		"phase": "Ready",
		"url":   "https://mapswipe.org/some/page",
		"conditions": []any{
			map[string]any{"type": "Ready", "status": "True"},
		},
	}
	srv := New(newClient(t, seededDyn(t, status)), &fakePodReader{}, &fakeLokiTailer{}, nil)
	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat", "mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`class="statusbar"`,              // sticky status bar
		`class="crumbs"`,                 // breadcrumb inside it
		`>Build details</h2>`,            // renamed card carrying URL/group/phase
		`>mapswipe.org&nbsp;↗<`,          // bare-host URL link (now in Build details)
		">Build phase</span>",            // build phase readout in the card
		`class="cockpit"`,                // split layout
		`class="card logdock col-right"`, // docked log pane
		`<details class="internals">`,    // internals disclosure
		"Observed generation",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page should contain %q; body=%s", want, body)
		}
	}
	// URL and phase moved off the sticky bar into the Build details card.
	for _, gone := range []string{`class="sb-url"`, "phase <b>"} {
		if strings.Contains(body, gone) {
			t.Errorf("detail status bar should no longer contain %q", gone)
		}
	}
	// Bare page: no standard header (the status bar replaces it).
	if strings.Contains(body, "<header>") {
		t.Errorf("detail page must not render the standard header; body=%s", body)
	}
	// The theme select still exists (in the status bar).
	if !strings.Contains(body, `id="theme-select"`) {
		t.Errorf("detail page should keep the theme select; body=%s", body)
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
	srv := New(newClient(t, seededDyn(t, status)), &fakePodReader{}, &fakeLokiTailer{}, nil)
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
	srv := New(newClient(t, dyn), &fakePodReader{}, &fakeLokiTailer{}, nil)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/partial", "mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// The loud callout carries the summary + the remediation hint.
	if !strings.Contains(body, "OOM Killed — the build step exceeded its 256Mi memory limit.") {
		t.Errorf("should render the OOM callout summary; body=%s", body)
	}
	if !strings.Contains(body, "spec.pipeline.phases.build.memoryLimit") {
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
	srv := New(newClient(t, dyn), &fakePodReader{}, &fakeLokiTailer{}, nil)

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

func TestDefaultContainer_BetweenPhasesGapPicksNextPendingStep(t *testing.T) {
	// Between phases the kubelet reports no Running container: the finished
	// steps are Succeeded and everything after is Pending. The default must be
	// the FIRST Pending step after a Succeeded one (the step about to start),
	// NOT the last container — follow mode used to jump to copier on every
	// phase change.
	gap := []view.Step{
		{Name: "clone", Status: "Succeeded"},
		{Name: "setup", Status: "Succeeded"},
		{Name: "fetch", Status: "Pending"},
		{Name: "build", Status: "Pending"},
		{Name: "copier", Status: "Pending"},
	}
	if got := defaultContainer(gap); got != "fetch" {
		t.Errorf("defaultContainer(between-phases gap) = %q, want fetch (not copier)", got)
	}

	// Nothing has run yet (pod still scheduling): follow the first step.
	allPending := []view.Step{
		{Name: "clone", Status: "Pending"},
		{Name: "build", Status: "Pending"},
		{Name: "copier", Status: "Pending"},
	}
	if got := defaultContainer(allPending); got != "clone" {
		t.Errorf("defaultContainer(all pending) = %q, want clone (first step)", got)
	}

	// All done: the last container (copier) holds the final word.
	allDone := []view.Step{
		{Name: "clone", Status: "Succeeded"},
		{Name: "build", Status: "Succeeded"},
		{Name: "copier", Status: "Succeeded"},
	}
	if got := defaultContainer(allDone); got != "copier" {
		t.Errorf("defaultContainer(all succeeded) = %q, want copier (last)", got)
	}
}

func TestLogs_LivePodForRunningBuild(t *testing.T) {
	dyn := seededDyn(t, runningBuildStatus())
	pods := &fakePodReader{logLines: []string{"cloning repo", "yarn build"}}
	loki := &fakeLokiTailer{configured: true} // configured, but live pod wins
	srv := New(newClient(t, dyn), pods, loki, nil)

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
	srv := New(newClient(t, dyn), pods, loki, nil)

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
	srv := New(newClient(t, dyn), pods, loki, nil)

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
	srv := New(newClient(t, dyn), pods, loki, nil)

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
	srv := New(newClient(t, dyn), pods, loki, nil)

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
	srv := New(newClient(t, dyn), pods, loki, nil)

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
	srv := New(newClient(t, dyn), pods, loki, nil)

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
	srv := New(newClient(t, dyn), pods, loki, nil)

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

// The container picker is a row of badge buttons (not a <select>): one per
// real container, the active one marked selected/aria-pressed, all carrying
// the data-log-container contract the detail JS delegates on.
func TestLogs_ContainerBadgeButtonsReplaceSelect(t *testing.T) {
	dyn := seededDyn(t, completedBuildStatus())
	pods := &fakePodReader{}
	loki := &fakeLokiTailer{configured: true, lines: []string{"archived"}}
	srv := New(newClient(t, dyn), pods, loki, nil)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/logs?build=mapswipe-uat-build-9&container=clone",
		"mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "<select") {
		t.Errorf("the container <select> must be gone; body=%s", body)
	}
	// One badge button per container step, each carrying the contract attr.
	if got := strings.Count(body, "data-log-container"); got != 2 {
		t.Errorf("want 2 container badge buttons (clone, build), got %d; body=%s", got, body)
	}
	// The requested container renders selected.
	if !strings.Contains(body, `value="clone" data-log-container class="cbtn selected" aria-pressed="true"`) {
		t.Errorf("clone badge should render selected; body=%s", body)
	}
	if !strings.Contains(body, `value="build" data-log-container class="cbtn" aria-pressed="false"`) {
		t.Errorf("build badge should render unselected; body=%s", body)
	}
}

func TestLogs_FollowToggleHiddenWhenIdle(t *testing.T) {
	dyn := seededDyn(t, completedBuildStatus()) // no build in flight
	pods := &fakePodReader{}
	loki := &fakeLokiTailer{configured: true, lines: []string{"archived"}}
	srv := New(newClient(t, dyn), pods, loki, nil)

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
	srv := New(newClient(t, dyn), pods, loki, nil)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/logs?follow=1", "mapswipe", "mapswipe-uat")
	if !strings.Contains(rec.Body.String(), "data-follow-toggle") {
		t.Errorf("follow toggle must be shown while a build is active; body=%s", rec.Body.String())
	}
}

func TestLogs_HistoricalBuildShowsViewingIndicator(t *testing.T) {
	dyn := seededDyn(t, completedBuildStatus())
	pods := &fakePodReader{}
	loki := &fakeLokiTailer{configured: true, lines: []string{"old build log"}}
	srv := New(newClient(t, dyn), pods, loki, nil)

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
	srv := New(newClient(t, dyn), pods, loki, nil)

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

// podWithLimits builds a pod whose named container carries a memory limit, so
// the metrics bar can be computed against a known cap. NodeName is always set:
// the server resolves the kubelet to ask from it before fetching metrics.
func podWithLimits(podName, container, mem string) *corev1.Pod {
	pod := podWithInitLimits(podName, container, mem)
	pod.Spec.Containers, pod.Spec.InitContainers = pod.Spec.InitContainers, nil
	return pod
}

// podWithInitLimits is podWithLimits with the container as an INIT container —
// the real build pod's shape (every step is init; only the copier is an app
// container).
func podWithInitLimits(podName, container, mem string) *corev1.Pod {
	lim := corev1.ResourceList{}
	if mem != "" {
		lim[corev1.ResourceMemory] = apiresource.MustParse(mem)
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: podName},
		Spec: corev1.PodSpec{
			NodeName: "node-1",
			InitContainers: []corev1.Container{
				{Name: container, Resources: corev1.ResourceRequirements{Limits: lim}},
			},
		},
	}
}

func TestPartial_LiveMetricsWithBars(t *testing.T) {
	dyn := seededDyn(t, runningBuildStatus())
	pods := &fakePodReader{pod: podWithLimits("mapswipe-uat-build-8-xyz", "build", "4Gi")}
	metrics := &fakeMetricser{usage: map[string]k8s.ContainerUsage{
		"build": {MemoryBytes: 2 * 1024 * 1024 * 1024},
	}}
	srv := New(newClient(t, dyn), pods, &fakeLokiTailer{}, metrics)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/partial", "mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(strings.ToLower(body), "live usage") {
		t.Errorf("should render a Live usage block; body=%s", body)
	}
	if !strings.Contains(body, "2.0 GiB") {
		t.Errorf("should show memory usage 2.0 GiB; body=%s", body)
	}
	// Mem bar: 2Gi of 4Gi = 50%.
	if !strings.Contains(body, "bar-fill") {
		t.Errorf("should render the memory bar when the limit is known; body=%s", body)
	}
	if !strings.Contains(body, "50%") {
		t.Errorf("bar width should reflect the limit (50%% mem); body=%s", body)
	}
	// CPU is intentionally gone from the usage block.
	if strings.Contains(body, "CPU") || strings.Contains(body, "cores") {
		t.Errorf("usage block must not render CPU; body=%s", body)
	}
	if metrics.lastNode != "node-1" {
		t.Errorf("metrics fetch must target the pod's node; got %q", metrics.lastNode)
	}
}

func TestPartial_LiveMetricsForInitContainerStep(t *testing.T) {
	// The real build pod runs every step as an initContainer; limits for the
	// bars must resolve from spec.initContainers, not just spec.containers.
	dyn := seededDyn(t, runningBuildStatus())
	pods := &fakePodReader{pod: podWithInitLimits("mapswipe-uat-build-8-xyz", "build", "4Gi")}
	metrics := &fakeMetricser{usage: map[string]k8s.ContainerUsage{
		"build": {MemoryBytes: 2 * 1024 * 1024 * 1024},
	}}
	srv := New(newClient(t, dyn), pods, &fakeLokiTailer{}, metrics)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/partial", "mapswipe", "mapswipe-uat")
	body := rec.Body.String()
	if !strings.Contains(strings.ToLower(body), "live usage") {
		t.Errorf("should render a Live usage block for an init step; body=%s", body)
	}
	if !strings.Contains(body, "50%") {
		t.Errorf("bar should draw against initContainer limits; body=%s", body)
	}
}

func TestPartial_PodGoneSkipsMetricsWithNote(t *testing.T) {
	// Without the pod there is no nodeName to resolve a kubelet from: the
	// metrics fetch must be skipped entirely and the note rendered.
	dyn := seededDyn(t, runningBuildStatus())
	pods := &fakePodReader{getErr: context.DeadlineExceeded}
	metrics := &fakeMetricser{usage: map[string]k8s.ContainerUsage{
		"build": {MemoryBytes: 1},
	}}
	srv := New(newClient(t, dyn), pods, &fakeLokiTailer{}, metrics)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/partial", "mapswipe", "mapswipe-uat")
	body := rec.Body.String()
	if metrics.calls != 0 {
		t.Errorf("metrics must not be fetched when the pod read fails; calls=%d", metrics.calls)
	}
	if !strings.Contains(strings.ToLower(body), "metrics unavailable") {
		t.Errorf("should render the unavailable note; body=%s", body)
	}
}

func TestPartial_LiveMetricsNoBarWhenNoLimit(t *testing.T) {
	dyn := seededDyn(t, runningBuildStatus())
	// Pod has the container but no limits → value shown, no bar.
	pods := &fakePodReader{pod: podWithLimits("mapswipe-uat-build-8-xyz", "build", "")}
	metrics := &fakeMetricser{usage: map[string]k8s.ContainerUsage{
		"build": {MemoryBytes: 512 * 1024 * 1024},
	}}
	srv := New(newClient(t, dyn), pods, &fakeLokiTailer{}, metrics)

	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/partial", "mapswipe", "mapswipe-uat")
	body := rec.Body.String()
	if !strings.Contains(body, "512.0 MiB") {
		t.Errorf("should show memory usage 512.0 MiB; body=%s", body)
	}
	if strings.Contains(body, "bar-fill") {
		t.Errorf("must NOT render a bar when no limit known; body=%s", body)
	}
}

func TestPartial_IdleAppFetchesNoMetrics(t *testing.T) {
	dyn := seededDyn(t, completedBuildStatus())
	pods := &fakePodReader{}
	metrics := &fakeMetricser{usage: map[string]k8s.ContainerUsage{
		"build": {MemoryBytes: 1},
	}}
	srv := New(newClient(t, dyn), pods, &fakeLokiTailer{}, metrics)

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
	srv := New(newClient(t, dyn), pods, &fakeLokiTailer{}, metrics)

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
	srv := New(newClient(t, dyn), pods, &fakeLokiTailer{}, metrics)

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
	srv := New(newClient(t, dyn), &fakePodReader{}, &fakeLokiTailer{}, nil)

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
	srvB := New(newClient(t, dynB), &fakePodReader{}, &fakeLokiTailer{}, nil)
	recB := doGet(srvB, "/ns/mapswipe/app/mapswipe-uat/partial", "mapswipe", "mapswipe-uat")
	if !strings.Contains(recB.Body.String(), `data-build-active="1"`) {
		t.Errorf("building partial should emit data-build-active=1; body=%s", recB.Body.String())
	}

	// Idle → data-build-active="0".
	dynI := seededDyn(t, completedBuildStatus())
	srvI := New(newClient(t, dynI), &fakePodReader{}, &fakeLokiTailer{}, nil)
	recI := doGet(srvI, "/ns/mapswipe/app/mapswipe-uat/partial", "mapswipe", "mapswipe-uat")
	if !strings.Contains(recI.Body.String(), `data-build-active="0"`) {
		t.Errorf("idle partial should emit data-build-active=0; body=%s", recI.Body.String())
	}
}

func TestDetail_EmbedsLiveRegionAndPoller(t *testing.T) {
	dyn := seededDyn(t, completedBuildStatus())
	srv := New(newClient(t, dyn), &fakePodReader{}, &fakeLokiTailer{}, nil)

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

// A commit-watch app surfaces the trigger config (watch interval, operator
// default schedule note, last-seen SHA linked to the repo's commit page) and a
// Commit-triggered build renders its short SHA as an external link.
func TestDetail_RendersCommitTriggerAndWatchConfig(t *testing.T) {
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{k8s.GVR: "FrontendAppList"}
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "baker.toggle-corp.com/v1alpha1",
		"kind":       "FrontendApp",
		"metadata": map[string]any{
			"namespace": "mapswipe",
			"name":      "mapswipe-uat",
			"annotations": map[string]any{
				"watch.baker.toggle-corp.com/last-seen-sha": "cafebabe1234567890",
			},
		},
		"spec": map[string]any{
			"repo":            "https://github.com/mapswipe/website.git",
			"scheduledBuilds": map[string]any{"enabled": true},
			"watchCommits":    map[string]any{"enabled": true, "interval": "5m"},
		},
		"status": map[string]any{
			"phase": "Ready",
			"build": map[string]any{
				"phase":   "Complete",
				"result":  "Succeeded",
				"jobName": "mapswipe-uat-build-8",
				"trigger": "Commit",
				"commit":  "cafebabe1234567890",
			},
		},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, app)
	srv := New(newClient(t, dyn), &fakePodReader{}, &fakeLokiTailer{}, nil)
	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat", "mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Commit · cafebab", // trigger label with short SHA (current build + history)
		`href="https://github.com/mapswipe/website/commit/cafebabe1234567890"`, // linked commit
		"operator default", // enabled scheduledBuilds without explicit schedule
		">Watch commits</", // trigger-config row label
		"5m",               // watch interval
		"cafebab",          // last-seen short SHA
	} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page should contain %q", want)
		}
	}
}

// paginationFixtureServer seeds n Ready apps in one namespace named app-000…,
// all same health so the sort collapses to ns/name order and page boundaries
// are deterministic. group=grp-p on every app so filter+search preservation can
// be exercised.
func paginationFixtureServer(t *testing.T, n int) *Server {
	t.Helper()
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{k8s.GVR: "FrontendAppList"}
	objs := make([]runtime.Object, 0, n)
	for i := 0; i < n; i++ {
		objs = append(objs, fappObj("pag", fmt.Sprintf("app-%03d", i),
			map[string]any{"group": "grp-p"},
			map[string]any{
				"phase":      "Ready",
				"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
			}))
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, objs...)
	return New(newClient(t, dyn), &fakePodReader{}, &fakeLokiTailer{}, nil)
}

func TestList_Page1ShowsFirstPageAndPager(t *testing.T) {
	srv := paginationFixtureServer(t, 60)
	body := getList(srv, "/").Body.String()
	// First 50 present, 51st (app-050) absent.
	if !strings.Contains(body, "app-000") || !strings.Contains(body, "app-049") {
		t.Errorf("page 1 should show the first 50 apps; body=%s", body)
	}
	if strings.Contains(body, "app-050") {
		t.Errorf("page 1 must not show the 51st app; body=%s", body)
	}
	if !strings.Contains(body, "Page 1 of 2") {
		t.Errorf("pager should show 'Page 1 of 2'; body=%s", body)
	}
	// Next present, Prev disabled/absent-as-link on page 1.
	if !strings.Contains(body, "Next") {
		t.Errorf("pager should render a Next link on page 1; body=%s", body)
	}
	if strings.Contains(body, `href="/?page=0"`) {
		t.Errorf("page 1 must not link Prev to page 0; body=%s", body)
	}
}

func TestList_Page2ShowsRemainderAndPrev(t *testing.T) {
	srv := paginationFixtureServer(t, 60)
	body := getList(srv, "/?page=2").Body.String()
	// Remaining 10 (app-050…app-059) present; first-page app absent.
	if !strings.Contains(body, "app-050") || !strings.Contains(body, "app-059") {
		t.Errorf("page 2 should show the remaining 10 apps; body=%s", body)
	}
	if strings.Contains(body, "app-049") {
		t.Errorf("page 2 must not show page-1 apps; body=%s", body)
	}
	if !strings.Contains(body, "Page 2 of 2") {
		t.Errorf("pager should show 'Page 2 of 2'; body=%s", body)
	}
	// Prev present as a link; Next greyed (not a link) on the last page.
	if !strings.Contains(body, `href="/?page=1"`) {
		t.Errorf("page 2 should link Prev to page 1; body=%s", body)
	}
	if strings.Contains(body, `href="/?page=3"`) {
		t.Errorf("last page must not link Next past the end; body=%s", body)
	}
}

func TestList_InvalidPageClamps(t *testing.T) {
	srv := paginationFixtureServer(t, 60)

	// page<1 and non-numeric clamp to page 1 (first window), never 404.
	for _, raw := range []string{"0", "abc", "-5"} {
		rec := getList(srv, "/?page="+raw)
		if rec.Code != http.StatusOK {
			t.Fatalf("page=%s: status = %d, want 200 (never 404)", raw, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "app-000") || strings.Contains(body, "app-050") {
			t.Errorf("page=%s should clamp to page 1; body=%s", raw, body)
		}
		if !strings.Contains(body, "Page 1 of 2") {
			t.Errorf("page=%s should read 'Page 1 of 2'; body=%s", raw, body)
		}
	}

	// page past the end clamps to the last page.
	rec := getList(srv, "/?page=999")
	if rec.Code != http.StatusOK {
		t.Fatalf("page=999: status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "app-059") || !strings.Contains(body, "Page 2 of 2") {
		t.Errorf("page=999 should clamp to the last page; body=%s", body)
	}
}

func TestList_PagerHiddenWhenSinglePage(t *testing.T) {
	srv := listFixtureServer(t) // 3 apps → 1 page
	body := getList(srv, "/").Body.String()
	if strings.Contains(body, "Pagination") || strings.Contains(body, "Page 1 of 1") {
		t.Errorf("pager must be hidden on a single page; body=%s", body)
	}
}

func TestList_PagerURLsPreserveFilters(t *testing.T) {
	srv := paginationFixtureServer(t, 60) // all group=grp-p, all Ready
	// On page 2 with status+group+search active, the Prev link must carry all
	// three filters AND set page=1. search=app matches every name, so 60 results
	// remain and the pager stays.
	body := getList(srv, "/?status=ready&group=grp-p&search=app&page=2").Body.String()
	if !strings.Contains(body, "Page 2 of 2") {
		t.Fatalf("expected a 2-page result set under these filters; body=%s", body)
	}
	// The Prev href must contain every active filter and the target page.
	for _, want := range []string{"status=ready", "group=grp-p", "search=app", "page=1"} {
		if !strings.Contains(body, want) {
			t.Errorf("Prev URL should preserve %q; body=%s", want, body)
		}
	}
}

func TestList_ChipAndSearchURLsDropPage(t *testing.T) {
	srv := paginationFixtureServer(t, 60)
	// On page 2, every chip and the clear-search link must OMIT page= (so any
	// filter/search change resets to page 1); only the pager's Prev/Next carry it.
	body := getList(srv, "/?search=app&page=2").Body.String()
	// The clear-search link and chips drop page (keeping only the filters).
	if strings.Contains(body, `href="/?search=app&amp;page`) {
		t.Errorf("chip/search URLs must not carry page=; body=%s", body)
	}
	// The status facet for degraded must be a clean filter URL without page.
	if !strings.Contains(body, `href="/?search=app&amp;status=degraded"`) {
		t.Errorf("status facet URL should carry filters but not page; body=%s", body)
	}
	// The pager itself DOES carry page (sanity: the two builders differ).
	if !strings.Contains(body, "page=1") {
		t.Errorf("pager Prev should still carry page=1; body=%s", body)
	}
}

// Disabled triggers state it plainly instead of implying a default cadence.
func TestDetail_RendersDisabledTriggers(t *testing.T) {
	srv, _ := newTestServer(t) // seededDyn: no spec triggers at all
	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat", "mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, ">Scheduled builds</") {
		t.Errorf("detail page should carry a Scheduled builds row")
	}
	if !strings.Contains(body, "Disabled") {
		t.Errorf("detail page should say Disabled for absent triggers")
	}
}

// storageListFixtureServer seeds apps carrying real status.storage.sizes so the
// list-page storage roll-up is assertable. Byte sizes are chosen to humanize to
// clean, distinct figures (see the per-test asserts). group/name let the
// filter+search aggregation tests select a subset.
func storageListFixtureServer(t *testing.T, objs ...*unstructured.Unstructured) *Server {
	t.Helper()
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{k8s.GVR: "FrontendAppList"}
	rt := make([]runtime.Object, len(objs))
	for i, o := range objs {
		rt[i] = o
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, rt...)
	return New(newClient(t, dyn), &fakePodReader{}, &fakeLokiTailer{}, nil)
}

// storageApp builds a Ready app in group carrying the given per-volume byte sizes.
func storageApp(ns, name, group string, cache, dataCache, outputTotal, output int64) *unstructured.Unstructured {
	return fappObj(ns, name,
		map[string]any{"group": group},
		map[string]any{
			"phase":      "Ready",
			"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
			"storage": map[string]any{
				"sizes": map[string]any{
					"cache":       cache,
					"dataCache":   dataCache,
					"outputTotal": outputTotal,
					"output":      output,
				},
			},
		})
}

const (
	gib4  = 4 * 1024 * 1024 * 1024 // "4.0 GiB"
	gib2  = 2 * 1024 * 1024 * 1024 // "2.0 GiB"
	gib64 = 6871947674             // ≈ "6.4 GiB"
	gib3  = 3 * 1024 * 1024 * 1024 // "3.0 GiB" (active)
)

func TestList_HeaderStorageGrandTotal(t *testing.T) {
	srv := storageListFixtureServer(t, storageApp("a", "one", "g", gib4, gib2, gib64, gib3))
	body := getList(srv, "/").Body.String()
	// Grand = cache+dataCache+outputTotal = 12.4 GiB (active excluded).
	if !strings.Contains(body, "12.4 GiB") {
		t.Errorf("header should show the grand storage total 12.4 GiB; body=%s", body)
	}
}

func TestList_HeaderStorageBreakdown(t *testing.T) {
	srv := storageListFixtureServer(t, storageApp("a", "one", "g", gib4, gib2, gib64, gib3))
	body := getList(srv, "/").Body.String()
	for _, want := range []string{"Cache: 4.0 GiB", "Data cache: 2.0 GiB", "Output: 6.4 GiB", "active 3.0 GiB"} {
		if !strings.Contains(body, want) {
			t.Errorf("header breakdown should contain %q; body=%s", want, body)
		}
	}
}

func TestList_HeaderStorageReflectsFilteredSet(t *testing.T) {
	srv := storageListFixtureServer(t,
		storageApp("a", "one", "grp-a", gib4, gib2, gib64, gib3), // grand 12.4 GiB
		storageApp("b", "two", "grp-b", gib2, 0, 0, 0),           // grand 2.0 GiB
	)
	// Unfiltered: grand = 12.4 + 2.0 = 14.4 GiB.
	body := getList(srv, "/").Body.String()
	if !strings.Contains(body, "14.4 GiB") {
		t.Errorf("unfiltered header should sum both apps to 14.4 GiB; body=%s", body)
	}
	// Filter to grp-b: only the 2.0 GiB app remains, header shrinks.
	body = getList(srv, "/?group=grp-b").Body.String()
	if strings.Contains(body, "14.4 GiB") {
		t.Errorf("filtered header must not show the unfiltered total; body=%s", body)
	}
	if !strings.Contains(body, "· 2.0 GiB (Cache: 2.0 GiB") {
		t.Errorf("filtered header should show only grp-b's 2.0 GiB; body=%s", body)
	}
}

func TestList_HeaderStorageIsPrePagination(t *testing.T) {
	// 60 apps × 2 GiB cache each = 120 GiB grand — spans both pages, so the
	// header must reflect all 60, not just the 50 on page 1.
	objs := make([]*unstructured.Unstructured, 60)
	for i := range objs {
		objs[i] = storageApp("pag", fmt.Sprintf("app-%03d", i), "grp-p", gib2, 0, 0, 0)
	}
	srv := storageListFixtureServer(t, objs...)
	body := getList(srv, "/").Body.String()
	// 60 × 2 GiB = 120 GiB (page 1 alone would be 50 × 2 = 100 GiB).
	if !strings.Contains(body, "120.0 GiB") {
		t.Errorf("header total should cover all 60 matches (120.0 GiB), not just the page; body=%s", body)
	}
	if strings.Contains(body, "· 100.0 GiB (Cache") {
		t.Errorf("header total must not be the per-page sum (100.0 GiB); body=%s", body)
	}
}

func TestList_PerAppStorageColumn(t *testing.T) {
	srv := storageListFixtureServer(t,
		storageApp("a", "one", "grp-a", gib4, gib2, gib64, gib3), // total 12.4 GiB
		storageApp("b", "two", "grp-b", gib2, 0, 0, 0),           // total 2.0 GiB
	)
	body := getList(srv, "/").Body.String()
	if !strings.Contains(body, `<th class="c-storage">Storage</th>`) {
		t.Errorf("table should carry a Storage column header; body=%s", body)
	}
	// Each row's storage cell shows that app's own total.
	if !strings.Contains(body, `<td class="c-storage"`) {
		t.Errorf("each row should carry a storage cell; body=%s", body)
	}
	// The smaller app's own total (2.0 GiB) must render even though the header
	// grand (14.4 GiB) differs.
	if !strings.Contains(body, "14.4 GiB") {
		t.Fatalf("precondition: header grand should be 14.4 GiB; body=%s", body)
	}
}

func TestList_PerAppStorageTooltip(t *testing.T) {
	srv := storageListFixtureServer(t, storageApp("a", "one", "grp-a", gib4, gib2, gib64, gib3))
	body := getList(srv, "/").Body.String()
	want := `title="Cache 4.0 GiB · Data cache 2.0 GiB · Output 6.4 GiB (active 3.0 GiB)"`
	if !strings.Contains(body, want) {
		t.Errorf("row storage cell should carry the breakdown tooltip %q; body=%s", want, body)
	}
}

func TestList_EmptyStateSpansStorageColumn(t *testing.T) {
	srv := storageListFixtureServer(t, storageApp("a", "one", "grp-a", gib4, gib2, gib64, gib3))
	// A search matching nothing yields the empty-state row.
	body := getList(srv, "/?search=zzzznomatch").Body.String()
	if !strings.Contains(body, `colspan="6"`) {
		t.Errorf("empty-state row should span all 6 columns; body=%s", body)
	}
	if strings.Contains(body, `colspan="5"`) {
		t.Errorf("empty-state colspan should be updated from 5 to 6; body=%s", body)
	}
}
