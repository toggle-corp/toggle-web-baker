package k8s

import (
	"strings"
	"testing"
	"time"

	"github.com/toggle-corp/toggle-web-baker/console/internal/view"
)

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
