package sentryhttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getsentry/sentry-go"
)

func TestWrapRecoversPanicAndCapturesOneEvent(t *testing.T) {
	transport := &recordingTransport{}
	hub := newTestHub(t, transport)

	h := Wrap(hub, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/apps", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	events := transport.Events()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if len(events[0].Exception) == 0 {
		t.Fatalf("event has no exception: %+v", events[0])
	}
}

func TestWrapCaptures500Response(t *testing.T) {
	transport := &recordingTransport{}
	hub := newTestHub(t, transport)
	hub.Scope().SetTag("component", "console")

	h := Wrap(hub, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "kaboom", http.StatusInternalServerError)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ns/apps/app/foo", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	events := transport.Events()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	ev := events[0]
	if ev.Level != sentry.LevelError {
		t.Errorf("level = %q, want error", ev.Level)
	}
	if !strings.Contains(ev.Message, "GET") || !strings.Contains(ev.Message, "/ns/apps/app/foo") || !strings.Contains(ev.Message, "500") {
		t.Errorf("message %q missing method/path/status", ev.Message)
	}
	if ev.Tags["component"] != "console" {
		t.Errorf("component tag = %q, want console", ev.Tags["component"])
	}
	if ev.Tags["status"] != "500" {
		t.Errorf("status tag = %q, want 500", ev.Tags["status"])
	}
	if ev.Request == nil || ev.Request.Method != http.MethodGet {
		t.Errorf("event.Request missing or wrong method: %+v", ev.Request)
	}
}

func TestWrapIgnores404Response(t *testing.T) {
	transport := &recordingTransport{}
	hub := newTestHub(t, transport)

	h := Wrap(hub, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/missing", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if n := len(transport.Events()); n != 0 {
		t.Fatalf("got %d events, want 0", n)
	}
}

func TestWrapIgnores200Response(t *testing.T) {
	transport := &recordingTransport{}
	hub := newTestHub(t, transport)

	h := Wrap(hub, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok")) // implicit 200, no WriteHeader
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if n := len(transport.Events()); n != 0 {
		t.Fatalf("got %d events, want 0", n)
	}
}

func TestWrapSetsUserFromForwardedUserHeader(t *testing.T) {
	transport := &recordingTransport{}
	hub := newTestHub(t, transport)

	h := Wrap(hub, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "kaboom", http.StatusInternalServerError)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-User", "octocat")
	h.ServeHTTP(httptest.NewRecorder(), req)

	events := transport.Events()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].User.Username != "octocat" {
		t.Fatalf("user.username = %q, want octocat", events[0].User.Username)
	}
}

func TestWrapFallsBackToAuthRequestUserHeader(t *testing.T) {
	transport := &recordingTransport{}
	hub := newTestHub(t, transport)

	h := Wrap(hub, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "kaboom", http.StatusInternalServerError)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Auth-Request-User", "hubot")
	h.ServeHTTP(httptest.NewRecorder(), req)

	events := transport.Events()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].User.Username != "hubot" {
		t.Fatalf("user.username = %q, want hubot", events[0].User.Username)
	}
}

func TestWrapRecoversPanicWithNilHub(t *testing.T) {
	h := Wrap(nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/apps", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}
