package sentryhttp

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
)

// recordingTransport implements sentry.Transport and records every event
// sent through it so tests can assert on the decoded *sentry.Event.
type recordingTransport struct {
	mu     sync.Mutex
	events []*sentry.Event
}

func (t *recordingTransport) Configure(sentry.ClientOptions) {}

func (t *recordingTransport) SendEvent(event *sentry.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, event)
}

func (t *recordingTransport) Flush(time.Duration) bool { return true }

func (t *recordingTransport) FlushWithContext(context.Context) bool { return true }

func (t *recordingTransport) Close() {}

func (t *recordingTransport) Events() []*sentry.Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]*sentry.Event, len(t.events))
	copy(out, t.events)
	return out
}

// newTestHub builds an isolated Sentry hub backed by the recording transport.
// Tests must never touch sentry.CurrentHub().
func newTestHub(t *testing.T, transport *recordingTransport) *sentry.Hub {
	t.Helper()
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:       "https://key@example.ingest.sentry.io/1",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("sentry.NewClient: %v", err)
	}
	return sentry.NewHub(client, sentry.NewScope())
}
