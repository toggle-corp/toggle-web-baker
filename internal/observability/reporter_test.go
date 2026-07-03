package observability

import (
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/toggle-corp/toggle-web-baker/internal/sentrytest"
)

func failure(app, reason string) TerminalFailure {
	return failureInNamespace("team-x", app, reason)
}

func failureInNamespace(ns, app, reason string) TerminalFailure {
	return TerminalFailure{
		App:       app,
		Namespace: ns,
		Step:      "copier",
		Reason:    reason,
		Message:   "boom",
	}
}

// fakeClock is an adjustable clock for driving the rate-limit window.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// CaptureTerminalFailure: emits one error-level event carrying the failure
// message, identifying tags, and a fingerprint of [namespace, app, reason].
func TestReporter_CaptureTerminalFailure(t *testing.T) {
	transport := &sentrytest.RecordingTransport{}
	r := NewReporterForTest(sentrytest.NewHub(t, transport), time.Now)

	r.CaptureTerminalFailure(TerminalFailure{
		App:       "web-a",
		Namespace: "team-x",
		Step:      "copier",
		Reason:    "BuildFailed",
		Message:   "copier exited 1",
	})

	events := transport.Events()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	ev := events[0]
	if ev.Message != "copier exited 1" {
		t.Errorf("Message = %q, want %q", ev.Message, "copier exited 1")
	}
	if ev.Level != "error" {
		t.Errorf("Level = %q, want error", ev.Level)
	}
	wantTags := map[string]string{
		"app":       "web-a",
		"namespace": "team-x",
		"step":      "copier",
		"reason":    "BuildFailed",
	}
	for k, want := range wantTags {
		if got := ev.Tags[k]; got != want {
			t.Errorf("Tags[%q] = %q, want %q", k, got, want)
		}
	}
	if len(ev.Fingerprint) != 3 || ev.Fingerprint[0] != "team-x" || ev.Fingerprint[1] != "web-a" || ev.Fingerprint[2] != "BuildFailed" {
		t.Errorf("Fingerprint = %v, want [team-x web-a BuildFailed]", ev.Fingerprint)
	}
}

// Rate limit: the same app+reason fires at most once per hour — the
// controller re-fails every minute, so this gate is load-bearing.
func TestReporter_RateLimitsSameFingerprintWithinAnHour(t *testing.T) {
	transport := &sentrytest.RecordingTransport{}
	clock := newFakeClock()
	r := NewReporterForTest(sentrytest.NewHub(t, transport), clock.Now)

	r.CaptureTerminalFailure(failure("web-a", "ConfigError"))
	clock.Advance(59 * time.Minute)
	r.CaptureTerminalFailure(failure("web-a", "ConfigError"))

	if got := len(transport.Events()); got != 1 {
		t.Fatalf("got %d events within the hour, want 1", got)
	}
}

// Rate limit: once the hour has elapsed the same fingerprint is allowed again.
func TestReporter_AllowsSameFingerprintAfterAnHour(t *testing.T) {
	transport := &sentrytest.RecordingTransport{}
	clock := newFakeClock()
	r := NewReporterForTest(sentrytest.NewHub(t, transport), clock.Now)

	r.CaptureTerminalFailure(failure("web-a", "ConfigError"))
	clock.Advance(61 * time.Minute)
	r.CaptureTerminalFailure(failure("web-a", "ConfigError"))

	if got := len(transport.Events()); got != 2 {
		t.Fatalf("got %d events across >1h, want 2", got)
	}
}

// Rate limit: distinct fingerprints (different app or reason) do not share
// a bucket — each fires independently.
func TestReporter_DistinctFingerprintsRateLimitedIndependently(t *testing.T) {
	transport := &sentrytest.RecordingTransport{}
	clock := newFakeClock()
	r := NewReporterForTest(sentrytest.NewHub(t, transport), clock.Now)

	r.CaptureTerminalFailure(failure("web-a", "ConfigError"))
	r.CaptureTerminalFailure(failure("web-b", "ConfigError"))
	r.CaptureTerminalFailure(failure("web-a", "BuildFailed"))

	if got := len(transport.Events()); got != 3 {
		t.Fatalf("got %d events for 3 distinct fingerprints, want 3", got)
	}
}

// Rate limit: apps are namespace-scoped — the same app name + reason in two
// different namespaces are two distinct failures, never one shared bucket.
func TestReporter_SameAppAndReasonInDifferentNamespacesAreDistinct(t *testing.T) {
	transport := &sentrytest.RecordingTransport{}
	clock := newFakeClock()
	r := NewReporterForTest(sentrytest.NewHub(t, transport), clock.Now)

	r.CaptureTerminalFailure(failureInNamespace("team-a", "web", "ConfigError"))
	r.CaptureTerminalFailure(failureInNamespace("team-b", "web", "ConfigError"))

	if got := len(transport.Events()); got != 2 {
		t.Fatalf("got %d events for the same app+reason in two namespaces, want 2", got)
	}
}

// A nil *Reporter is the disabled mode: every method is a safe no-op, and
// NewZapCore still hands back a usable (no-op) core.
func TestReporter_NilReporterIsSafeNoOp(t *testing.T) {
	var r *Reporter

	r.CaptureTerminalFailure(failure("web-a", "ConfigError")) // must not panic
	r.Flush(time.Second)                                      // must not panic

	core := NewZapCore(r)
	if core == nil {
		t.Fatal("NewZapCore(nil) = nil, want a no-op core")
	}
	logger := zap.New(core)
	logger.Error("boom") // must not panic
}

// InitFromEnv: an empty (or unset) SENTRY_DSN disables Sentry entirely —
// nil Reporter, no error.
func TestInitFromEnv_EmptyDSNDisablesSentry(t *testing.T) {
	t.Setenv("SENTRY_DSN", "")

	r, err := InitFromEnv()
	if err != nil {
		t.Fatalf("InitFromEnv() error = %v, want nil", err)
	}
	if r != nil {
		t.Fatalf("InitFromEnv() = %v, want nil Reporter for empty DSN", r)
	}
}
