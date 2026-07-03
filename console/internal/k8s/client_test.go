package k8s

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/toggle-corp/toggle-web-baker/console/internal/view"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// fappObj builds a minimal unstructured FrontendApp for seeding the fake client.
func fappObj(namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "baker.toggle-corp.com/v1alpha1",
		"kind":       "FrontendApp",
		"metadata": map[string]any{
			"namespace": namespace,
			"name":      name,
		},
		"status": map[string]any{"phase": "Ready"},
	}}
}

// List must serve from the informer/lister cache, not a live API List. This is a
// regression guard: NewWithDynamic starts an informer against the fake dynamic
// client and blocks on cache sync, so both seeded apps come back mapped. (It
// cannot fail RED against the old live-List code — the fake serves both paths —
// so it guards the cache wiring against future regressions rather than driving
// it.)
func TestList_ServesSeededAppsFromCache(t *testing.T) {
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{GVR: "FrontendAppList"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		scheme, listKinds,
		fappObj("mapswipe", "mapswipe-uat"),
		fappObj("hot", "hot-prod"),
	)

	c := NewWithDynamic(dyn)
	t.Cleanup(c.Close)

	apps, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(apps) != 2 {
		t.Fatalf("want 2 apps from cache, got %d: %+v", len(apps), apps)
	}

	type nn struct{ ns, name string }
	got := make([]nn, 0, len(apps))
	for _, a := range apps {
		got = append(got, nn{a.Namespace, a.Name})
	}
	sort.Slice(got, func(i, j int) bool { return got[i].name < got[j].name })
	want := []nn{{"hot", "hot-prod"}, {"mapswipe", "mapswipe-uat"}}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("app[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// The cleanup patch bodies must merge-patch exactly the requested-at + by
// annotation keys for each action, with the RFC3339 timestamp and user. This
// mirrors the rebuild patch contract.
func TestCleanupPatch_CacheAndReleases(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	stamp := now.Format(time.RFC3339)

	cache := string(cleanupPatch(
		view.AnnotationCleanupCacheRequestedAt, view.AnnotationCleanupCacheBy, "octocat", now))
	for _, want := range []string{view.AnnotationCleanupCacheRequestedAt, view.AnnotationCleanupCacheBy, stamp, "octocat"} {
		if !strings.Contains(cache, want) {
			t.Errorf("cache patch %q missing %q", cache, want)
		}
	}
	if strings.Contains(cache, "cleanup-releases") || strings.Contains(cache, "rebuild.baker") {
		t.Errorf("cache patch must only carry the cache keys: %q", cache)
	}

	rel := string(cleanupPatch(
		view.AnnotationCleanupReleasesRequestedAt, view.AnnotationCleanupReleasesBy, "hubber", now))
	for _, want := range []string{view.AnnotationCleanupReleasesRequestedAt, view.AnnotationCleanupReleasesBy, stamp, "hubber"} {
		if !strings.Contains(rel, want) {
			t.Errorf("releases patch %q missing %q", rel, want)
		}
	}
}

// The rebuild patch stamps requested-at + by AND clears the commit annotation
// (merge-patch null) in the same body, so a manual rebuild can't be
// misclassified as Commit by a stale watcher SHA — mirroring how the clock tick
// clears "by". Trigger sources each clear the others' keys.
func TestRebuildPatch_SetsByAndClearsCommit(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	patch := string(rebuildPatch("octocat", now))

	for _, want := range []string{
		view.AnnotationRebuildRequestedAt, view.AnnotationRebuildBy,
		now.Format(time.RFC3339), "octocat",
	} {
		if !strings.Contains(patch, want) {
			t.Errorf("rebuild patch %q missing %q", patch, want)
		}
	}
	if !strings.Contains(patch, `"`+view.AnnotationRebuildCommit+`":null`) {
		t.Errorf("rebuild patch %q must null out the commit annotation", patch)
	}
}
