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
