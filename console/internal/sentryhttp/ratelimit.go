package sentryhttp

import (
	"strings"
	"sync"
	"time"
)

// limitWindow is how long a fingerprint stays muted after an event is sent.
// HTMX-polling dashboards re-hit a failing endpoint every few seconds; during
// an apiserver outage every poll is a 5xx, and without this gate each one
// would land in the shared Sentry project.
const limitWindow = time.Hour

// pruneInterval throttles the stale-entry sweep so a hot 5xx path does not
// pay an O(map) scan on every request.
const pruneInterval = time.Minute

// fingerprintLimiter allows one event per fingerprint per window. The clock
// is injected so tests can drive the window deterministically.
type fingerprintLimiter struct {
	now func() time.Time

	mu        sync.Mutex
	lastSent  map[string]time.Time
	lastPrune time.Time
}

func newFingerprintLimiter(now func() time.Time) *fingerprintLimiter {
	if now == nil {
		now = time.Now
	}
	return &fingerprintLimiter{now: now, lastSent: make(map[string]time.Time)}
}

// allow reports whether an event with this fingerprint may be sent now,
// recording the send time when it is. Stale entries are pruned at most once
// per pruneInterval so the map does not grow with dead fingerprints. The
// parts are joined with NUL so ["a","bc"] and ["ab","c"] stay distinct.
func (l *fingerprintLimiter) allow(fingerprint []string) bool {
	key := strings.Join(fingerprint, "\x00")
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastPrune) >= pruneInterval {
		l.lastPrune = now
		for k, sent := range l.lastSent {
			if now.Sub(sent) >= limitWindow {
				delete(l.lastSent, k)
			}
		}
	}

	if sent, ok := l.lastSent[key]; ok && now.Sub(sent) < limitWindow {
		return false
	}
	l.lastSent[key] = now
	return true
}
