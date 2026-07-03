package sentryhttp

import (
	"testing"
	"time"
)

func TestFingerprintLimiterAllowsOncePerWindow(t *testing.T) {
	now := time.Unix(1700000000, 0)
	l := newFingerprintLimiter(func() time.Time { return now })

	fp := []string{"GET", "/apps", "500"}
	if !l.allow(fp) {
		t.Fatal("first allow = false, want true")
	}
	if l.allow(fp) {
		t.Fatal("second allow within window = true, want false")
	}

	now = now.Add(limitWindow + time.Second)
	if !l.allow(fp) {
		t.Fatal("allow after window = false, want true")
	}
}

func TestFingerprintLimiterKeysAreDistinct(t *testing.T) {
	l := newFingerprintLimiter(nil)

	if !l.allow([]string{"a", "bc"}) {
		t.Fatal(`allow(["a","bc"]) = false, want true`)
	}
	if !l.allow([]string{"ab", "c"}) {
		t.Fatal(`allow(["ab","c"]) = false, want true: keys must not collide`)
	}
	if !l.allow([]string{"GET", "/other", "500"}) {
		t.Fatal("distinct fingerprint = false, want true")
	}
}

func TestFingerprintLimiterPruneThrottledAndRemovesStale(t *testing.T) {
	start := time.Unix(1700000000, 0)
	now := start
	l := newFingerprintLimiter(func() time.Time { return now })

	l.allow([]string{"a"}) // t0; first call also runs a (empty) prune

	// Just under the window: prune runs (>1min since last scan) but "a" is
	// not yet stale.
	now = start.Add(limitWindow - 30*time.Second)
	l.allow([]string{"b"})

	// "a" is now stale, but the scan is throttled (<1min since last prune),
	// so it must survive this call.
	now = start.Add(limitWindow + 12*time.Second)
	l.allow([]string{"c"})
	l.mu.Lock()
	n := len(l.lastSent)
	l.mu.Unlock()
	if n != 3 {
		t.Fatalf("entries = %d, want 3 (prune throttled, stale entry kept)", n)
	}

	// Past the throttle interval the scan runs and drops "a".
	now = start.Add(limitWindow + 2*time.Minute)
	l.allow([]string{"d"})
	l.mu.Lock()
	n = len(l.lastSent)
	l.mu.Unlock()
	if n != 3 {
		t.Fatalf("entries = %d, want 3 (b, c, d after pruning a)", n)
	}
}
