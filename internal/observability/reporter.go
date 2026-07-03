// Package observability wires the operator into Sentry: explicit events for
// platform-fault terminal failures and a zap core that tees Error+ logs.
// A nil *Reporter is a valid, fully disabled reporter — every method is
// nil-receiver-safe so callers never branch on whether Sentry is configured.
package observability

import (
	"time"

	"github.com/getsentry/sentry-go"
)

// Reporter sends operator error events to Sentry. The zero value is not
// usable; construct via InitFromEnv (production) or NewReporterForTest.
type Reporter struct {
	hub     *sentry.Hub
	limiter *fingerprintLimiter
}

// TerminalFailure describes a App that reached a terminal failure
// state attributable to the platform (not user code).
type TerminalFailure struct {
	App       string
	Namespace string
	Step      string
	Reason    string
	Message   string
}

// NewReporterForTest builds a Reporter around an explicit hub and clock.
// It exists so tests (here and in the controller package) can inject a
// recording transport and a fake clock; production code uses InitFromEnv.
func NewReporterForTest(hub *sentry.Hub, now func() time.Time) *Reporter {
	return &Reporter{hub: hub, limiter: newFingerprintLimiter(now)}
}

// CaptureTerminalFailure emits one error event for a platform-fault terminal
// failure, fingerprinted by [namespace, app, reason] — apps are
// namespace-scoped, so the same app name in two namespaces must not share
// a bucket.
func (r *Reporter) CaptureTerminalFailure(f TerminalFailure) {
	if r == nil {
		return
	}
	fingerprint := []string{f.Namespace, f.App, f.Reason}
	if !r.limiter.allow(fingerprint) {
		return
	}

	event := sentry.NewEvent()
	event.Level = sentry.LevelError
	event.Message = f.Message
	event.Tags = map[string]string{
		"app":       f.App,
		"namespace": f.Namespace,
		"step":      f.Step,
		"reason":    f.Reason,
	}
	event.Fingerprint = fingerprint
	r.hub.CaptureEvent(event)
}

// Flush waits up to timeout for buffered events to reach Sentry. Call once
// at shutdown; safe on a nil Reporter.
func (r *Reporter) Flush(timeout time.Duration) {
	if r == nil {
		return
	}
	r.hub.Flush(timeout)
}
