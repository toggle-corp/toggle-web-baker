package sentryhttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAttachedErrorReachesCapturedEvent(t *testing.T) {
	transport := &recordingTransport{}
	hub := newTestHub(t, transport)

	h := Wrap(hub, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		AttachError(r.Context(), "kube list failed", errors.New("connection refused"))
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/apps", nil))

	events := transport.Events()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	ev := events[0]
	if !strings.Contains(ev.Message, "kube list failed") {
		t.Errorf("message %q missing attached msg", ev.Message)
	}
	if !strings.Contains(ev.Message, "connection refused") {
		t.Errorf("message %q missing attached error detail", ev.Message)
	}
}

func TestAttachErrorWithoutCarrierIsNoOp(t *testing.T) {
	// Must not panic when the middleware never planted a carrier.
	AttachError(context.Background(), "orphan", errors.New("nope"))
}
