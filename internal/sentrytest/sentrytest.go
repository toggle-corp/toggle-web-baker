// Package sentrytest provides a recording Sentry transport and an isolated
// hub for tests in this module. It exists so internal/observability and
// internal/controller share one copy instead of duplicating it (test files
// are not importable across packages). The console module keeps its own copy.
package sentrytest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
)

// RecordingTransport implements sentry.Transport and records every event
// sent through it so tests can assert on the decoded *sentry.Event.
type RecordingTransport struct {
	mu     sync.Mutex
	events []*sentry.Event
}

func (t *RecordingTransport) Configure(sentry.ClientOptions) {}

func (t *RecordingTransport) SendEvent(event *sentry.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, event)
}

func (t *RecordingTransport) Flush(time.Duration) bool { return true }

func (t *RecordingTransport) FlushWithContext(context.Context) bool { return true }

func (t *RecordingTransport) Close() {}

// Events returns a copy of everything sent so far.
func (t *RecordingTransport) Events() []*sentry.Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]*sentry.Event, len(t.events))
	copy(out, t.events)
	return out
}

// NewHub builds an isolated Sentry hub backed by the given transport.
// Tests must never touch sentry.CurrentHub().
func NewHub(tb testing.TB, transport sentry.Transport) *sentry.Hub {
	tb.Helper()
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:       "https://key@example.ingest.sentry.io/1",
		Transport: transport,
	})
	if err != nil {
		tb.Fatalf("sentry.NewClient: %v", err)
	}
	return sentry.NewHub(client, sentry.NewScope())
}
