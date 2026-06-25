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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// newTestServer wires the real Client over a fake dynamic client seeded with
// one FrontendApp, so the handlers exercise the actual list/get/patch paths.
func newTestServer(t *testing.T) (*Server, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		k8s.GVR: "FrontendAppList",
	}
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "baker.toggle-corp.com/v1alpha1",
		"kind":       "FrontendApp",
		"metadata": map[string]any{
			"namespace": "mapswipe",
			"name":      "mapswipe-uat",
		},
		"status": map[string]any{"phase": "Ready"},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, app)
	return New(k8s.NewWithDynamic(dyn)), dyn
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

func TestHealthz(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Errorf("healthz = %d %q", rec.Code, rec.Body.String())
	}
}
