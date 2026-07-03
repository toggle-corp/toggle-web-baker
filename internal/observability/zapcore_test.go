package observability

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/toggle-corp/toggle-web-baker/internal/sentrytest"
)

// newTestLogger builds a zap logger whose only core is the Sentry tee,
// backed by a recording transport.
func newTestLogger(t *testing.T, transport *sentrytest.RecordingTransport) *zap.Logger {
	t.Helper()
	r := NewReporterForTest(sentrytest.NewHub(t, transport), time.Now)
	return zap.New(NewZapCore(r))
}

// Zap core: an Error-level log becomes one Sentry event carrying the message.
func TestZapCore_ErrorLogIsCaptured(t *testing.T) {
	transport := &sentrytest.RecordingTransport{}
	logger := newTestLogger(t, transport)

	logger.Error("boom", zap.Error(errors.New("disk on fire")))

	events := transport.Events()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Message != "boom" {
		t.Errorf("Message = %q, want boom", events[0].Message)
	}
	if events[0].Level != "error" {
		t.Errorf("Level = %q, want error", events[0].Level)
	}
}

// Zap core: Info-level logs are below the core's threshold and never reach
// Sentry.
func TestZapCore_InfoLogIsIgnored(t *testing.T) {
	transport := &sentrytest.RecordingTransport{}
	logger := newTestLogger(t, transport)

	logger.Info("reconciled fine")

	if got := len(transport.Events()); got != 0 {
		t.Fatalf("got %d events for an info log, want 0", got)
	}
}

// Zap core: fields accumulated via logger.With appear in the event's
// "fields" context (sentry-go v0.47 removed Event.Extra), and the derived
// logger is a clone — the parent stays unpolluted.
func TestZapCore_WithFieldsAppearInEventAndDoNotLeakToParent(t *testing.T) {
	transport := &sentrytest.RecordingTransport{}
	logger := newTestLogger(t, transport)

	derived := logger.With(zap.String("app", "web-a"))
	derived.Error("derived boom")
	logger.Error("parent boom")

	events := transport.Events()
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if got := events[0].Contexts["fields"]["app"]; got != "web-a" {
		t.Errorf("derived event Contexts[fields][app] = %v, want web-a", got)
	}
	if _, ok := events[1].Contexts["fields"]["app"]; ok {
		t.Errorf("parent event carries Contexts[fields][app] = %v; With must clone, not mutate", events[1].Contexts["fields"]["app"])
	}
}

// Drop filter: Kubernetes optimistic-concurrency conflicts are routine
// reconcile noise, never platform bugs — they must not reach Sentry.
func TestZapCore_ConflictErrorIsDropped(t *testing.T) {
	transport := &sentrytest.RecordingTransport{}
	logger := newTestLogger(t, transport)

	conflict := apierrors.NewConflict(
		schema.GroupResource{Group: "baker.toggle-corp.com", Resource: "frontendapps"},
		"web-a", errors.New("the object has been modified"))
	logger.Error("failed to update status", zap.Error(conflict))

	if got := len(transport.Events()); got != 0 {
		t.Fatalf("got %d events for a conflict error, want 0", got)
	}
}

// Drop filter: context cancellation (even wrapped) is shutdown/requeue
// noise — no event.
func TestZapCore_WrappedContextCanceledIsDropped(t *testing.T) {
	transport := &sentrytest.RecordingTransport{}
	logger := newTestLogger(t, transport)

	wrapped := fmt.Errorf("watch closed: %w", context.Canceled)
	logger.Error("reconcile aborted", zap.Error(wrapped))

	if got := len(transport.Events()); got != 0 {
		t.Fatalf("got %d events for a wrapped context.Canceled, want 0", got)
	}
}

// Drop filter: controller-runtime's leader-election chatter is matched on
// the message itself — no event.
func TestZapCore_LeaderElectionMessageIsDropped(t *testing.T) {
	transport := &sentrytest.RecordingTransport{}
	logger := newTestLogger(t, transport)

	logger.Error("error retrieving resource lock during leader election", zap.Error(errors.New("timeout")))

	if got := len(transport.Events()); got != 0 {
		t.Fatalf("got %d events for a leader-election message, want 0", got)
	}
}

// Drop filter: an ordinary error passes the filters and the attached error
// reaches the event as an exception.
func TestZapCore_OrdinaryErrorIsCapturedWithException(t *testing.T) {
	transport := &sentrytest.RecordingTransport{}
	logger := newTestLogger(t, transport)

	logger.Error("pvc resize failed", zap.Error(errors.New("storageclass does not support expansion")))

	events := transport.Events()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if len(events[0].Exception) == 0 {
		t.Fatal("event has no exception; the zap.Error field should be attached")
	}
	if got := events[0].Exception[len(events[0].Exception)-1].Value; got != "storageclass does not support expansion" {
		t.Errorf("Exception value = %q, want the original error message", got)
	}
}

// Rate limit: the same logger+message+error fired back-to-back sends one
// event, fingerprinted [loggerName, message, errMsg].
func TestZapCore_IdenticalErrorLogsAreRateLimited(t *testing.T) {
	transport := &sentrytest.RecordingTransport{}
	logger := newTestLogger(t, transport).Named("frontendapp")

	logger.Error("upsert failed", zap.Error(errors.New("boom")))
	logger.Error("upsert failed", zap.Error(errors.New("boom")))

	events := transport.Events()
	if len(events) != 1 {
		t.Fatalf("got %d events for identical back-to-back errors, want 1", len(events))
	}
	fp := events[0].Fingerprint
	if len(fp) != 3 || fp[0] != "frontendapp" || fp[1] != "upsert failed" || fp[2] != "boom" {
		t.Errorf("Fingerprint = %v, want [frontendapp upsert failed boom]", fp)
	}
}

// Fingerprint: controller-runtime logs EVERY reconcile failure as the constant
// message "Reconciler error" (the controller identity is a field, not the
// logger name), so the error text must join the fingerprint — two different
// errors under the same message are distinct events, not one rate-limited
// bucket.
func TestZapCore_SameMessageDifferentErrorsAreDistinctEvents(t *testing.T) {
	transport := &sentrytest.RecordingTransport{}
	logger := newTestLogger(t, transport)

	logger.Error("Reconciler error", zap.Error(errors.New("pvc resize failed")))
	logger.Error("Reconciler error", zap.Error(errors.New("ingress upsert failed")))

	events := transport.Events()
	if len(events) != 2 {
		t.Fatalf("got %d events for two distinct errors under one message, want 2", len(events))
	}
	if events[0].Fingerprint[2] == events[1].Fingerprint[2] {
		t.Errorf("both events share fingerprint error component %q; errors must be distinguished", events[0].Fingerprint[2])
	}
}

// Fingerprint: the error component is truncated to 200 chars so unbounded
// error strings cannot blow up the fingerprint (or the limiter's keyspace).
func TestZapCore_FingerprintErrorComponentIsTruncated(t *testing.T) {
	transport := &sentrytest.RecordingTransport{}
	logger := newTestLogger(t, transport)

	long := strings.Repeat("x", 500)
	logger.Error("Reconciler error", zap.Error(errors.New(long)))

	events := transport.Events()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	fp := events[0].Fingerprint
	if len(fp) != 3 {
		t.Fatalf("Fingerprint = %v, want 3 components", fp)
	}
	if got := len(fp[2]); got != 200 {
		t.Errorf("fingerprint error component length = %d, want 200", got)
	}
}

// Fingerprint: entries without an error field keep the two-component
// [loggerName, message] fingerprint.
func TestZapCore_NoErrorFieldKeepsTwoComponentFingerprint(t *testing.T) {
	transport := &sentrytest.RecordingTransport{}
	logger := newTestLogger(t, transport).Named("frontendapp")

	logger.Error("upsert failed")

	events := transport.Events()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	fp := events[0].Fingerprint
	if len(fp) != 2 || fp[0] != "frontendapp" || fp[1] != "upsert failed" {
		t.Errorf("Fingerprint = %v, want [frontendapp upsert failed]", fp)
	}
}
