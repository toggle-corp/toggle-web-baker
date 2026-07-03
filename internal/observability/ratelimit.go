package observability

import (
	"strings"
	"sync"
	"time"
)

// limitWindow is how long a fingerprint stays muted after an event is sent.
// The controller re-fails every minute; without this gate a single stuck app
// would emit ~60 identical events per hour.
const limitWindow = time.Hour

// pruneInterval throttles the full-map stale-entry sweep: correctness for
// the queried key is handled per-key, so pruning is pure housekeeping and
// need not run on every allow() call.
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
// recording the send time when it is. Stale entries are pruned
// opportunistically (at most once per pruneInterval) so the map does not
// grow with dead fingerprints.
func (l *fingerprintLimiter) allow(fingerprint []string) bool {
	key := joinFingerprint(fingerprint)
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastPrune) >= pruneInterval {
		for k, sent := range l.lastSent {
			if now.Sub(sent) >= limitWindow {
				delete(l.lastSent, k)
			}
		}
		l.lastPrune = now
	}

	if sent, ok := l.lastSent[key]; ok && now.Sub(sent) < limitWindow {
		return false
	}
	l.lastSent[key] = now
	return true
}

// joinFingerprint flattens a fingerprint into a map key. The separator makes
// ["a","bc"] and ["ab","c"] distinct.
func joinFingerprint(fingerprint []string) string {
	return strings.Join(fingerprint, "\x00")
}
