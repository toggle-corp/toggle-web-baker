package sentryhttp

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
)

// captureLog redirects the standard logger to a buffer for the duration of
// the test so panic-log assertions do not depend on stderr.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

func TestWrapRecoversPanicAndCapturesOneEvent(t *testing.T) {
	captureLog(t)
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
	wantFP := []string{"GET", "/ns/apps/app/foo", "500"}
	if !reflect.DeepEqual(ev.Fingerprint, wantFP) {
		t.Errorf("fingerprint = %v, want %v", ev.Fingerprint, wantFP)
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
	captureLog(t)
	h := Wrap(nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/apps", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestWrapPanicLogsUnconditionally(t *testing.T) {
	buf := captureLog(t)

	// nil hub: the panic must still land in pod logs when Sentry is disabled.
	h := Wrap(nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom-log")
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/apps", nil))

	out := buf.String()
	for _, want := range []string{"GET", "/apps", "boom-log", "goroutine"} {
		if !strings.Contains(out, want) {
			t.Errorf("log output missing %q:\n%s", want, out)
		}
	}
}

func TestWrapErrAbortHandlerPropagatesWithoutCapture(t *testing.T) {
	buf := captureLog(t)
	transport := &recordingTransport{}
	hub := newTestHub(t, transport)

	h := Wrap(hub, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	defer func() {
		rec := recover()
		if rec != http.ErrAbortHandler {
			t.Fatalf("recovered %v, want http.ErrAbortHandler", rec)
		}
		if n := len(transport.Events()); n != 0 {
			t.Errorf("got %d events, want 0", n)
		}
		if buf.Len() != 0 {
			t.Errorf("log output %q, want empty (quiet abort)", buf.String())
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/apps", nil))
	t.Fatal("ServeHTTP returned normally, want http.ErrAbortHandler panic")
}

func TestWrapPanicAfterPartialWriteAbortsConnection(t *testing.T) {
	captureLog(t)
	transport := &recordingTransport{}
	hub := newTestHub(t, transport)

	// Big enough to push headers and a partial body onto the wire before the
	// panic, so the client sees a truncated response rather than a clean one.
	partial := strings.Repeat("x", 64<<10)
	h := Wrap(hub, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, partial)
		panic("mid-write boom")
	}))

	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/partial")
	if err == nil {
		defer func() { _ = resp.Body.Close() }()
		if _, rerr := io.ReadAll(resp.Body); rerr == nil {
			t.Fatal("client read a clean response, want aborted connection error")
		}
	}

	events := transport.Events()
	if len(events) != 1 {
		t.Fatalf("got %d events, want exactly 1 (panic capture, no 5xx message)", len(events))
	}
	if len(events[0].Exception) == 0 {
		t.Fatalf("event is not an exception capture: %+v", events[0])
	}
}

func TestWrapPanicsAreNotRateLimited(t *testing.T) {
	captureLog(t)
	transport := &recordingTransport{}
	hub := newTestHub(t, transport)

	h := Wrap(hub, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	for range 2 {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/apps", nil))
	}
	if n := len(transport.Events()); n != 2 {
		t.Fatalf("got %d events, want 2 (panic captures bypass the limiter)", n)
	}
}

func TestWrap5xxRateLimitedPerFingerprint(t *testing.T) {
	transport := &recordingTransport{}
	hub := newTestHub(t, transport)

	now := time.Unix(1700000000, 0)
	h := wrapWithClock(hub, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "kaboom", http.StatusInternalServerError)
	}), func() time.Time { return now })

	do := func(path string) {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	do("/apps")
	do("/apps")
	if n := len(transport.Events()); n != 1 {
		t.Fatalf("after duplicate 5xx: got %d events, want 1", n)
	}

	// A different path is an independent fingerprint.
	do("/other")
	if n := len(transport.Events()); n != 2 {
		t.Fatalf("after distinct path: got %d events, want 2", n)
	}

	// Past the window the fingerprint is allowed again.
	now = now.Add(time.Hour + time.Minute)
	do("/apps")
	if n := len(transport.Events()); n != 3 {
		t.Fatalf("after window expiry: got %d events, want 3", n)
	}
}

func TestWrapDoesNotPlantCarrierWithNilHub(t *testing.T) {
	var planted bool
	h := Wrap(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		planted = carrierFrom(r.Context()) != nil
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if planted {
		t.Fatal("carrier planted with nil hub, want skipped (nothing reads it)")
	}
}

func TestUserFromPrefersForwardedUser(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := UserFrom(r); got != "" {
		t.Fatalf("UserFrom(no headers) = %q, want empty", got)
	}
	r.Header.Set("X-Auth-Request-User", "hubot")
	if got := UserFrom(r); got != "hubot" {
		t.Fatalf("UserFrom = %q, want hubot", got)
	}
	r.Header.Set("X-Forwarded-User", "octocat")
	if got := UserFrom(r); got != "octocat" {
		t.Fatalf("UserFrom = %q, want octocat", got)
	}
}
