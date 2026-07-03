// Package sentryhttp reports console HTTP failures (panics and 5xx
// responses) to Sentry. A nil *sentry.Hub disables reporting entirely while
// keeping panic recovery functional.
package sentryhttp

import (
	"fmt"
	"log"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/getsentry/sentry-go"
)

// statusRecorder wraps http.ResponseWriter to remember the status code the
// handler wrote. A Write without an explicit WriteHeader implies 200.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	if sr.status == 0 {
		sr.status = code
	}
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	if sr.status == 0 {
		sr.status = http.StatusOK
	}
	return sr.ResponseWriter.Write(b)
}

// UserFrom reads the GitHub username oauth2-proxy injects into the upstream
// request. In reverse-proxy mode oauth2-proxy passes X-Forwarded-User (via
// --pass-user-headers) — that is the live source. X-Auth-Request-User is only
// emitted in nginx auth_request mode, kept here as a harmless fallback.
// Empty means anonymous.
func UserFrom(r *http.Request) string {
	if u := r.Header.Get("X-Forwarded-User"); u != "" {
		return u
	}
	return r.Header.Get("X-Auth-Request-User")
}

// Wrap returns a handler that recovers panics (writing a 500 when nothing has
// been sent yet, aborting the connection otherwise) and, when hub is non-nil,
// reports panics and rate-limited 5xx responses to Sentry.
func Wrap(hub *sentry.Hub, next http.Handler) http.Handler {
	return wrapWithClock(hub, next, time.Now)
}

// wrapWithClock is Wrap with an injectable clock driving the 5xx rate
// limiter, split out so tests can move time deterministically.
func wrapWithClock(hub *sentry.Hub, next http.Handler, now func() time.Time) http.Handler {
	limiter := newFingerprintLimiter(now)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqHub *sentry.Hub
		if hub != nil {
			reqHub = hub.Clone()
			reqHub.Scope().SetRequest(r)
			if user := UserFrom(r); user != "" {
				reqHub.Scope().SetUser(sentry.User{Username: user})
			}
			// Plant the error carrier only when reporting is enabled:
			// nothing reads it with a nil hub, and AttachError is a
			// safe no-op without it.
			r = r.WithContext(withCarrier(r.Context()))
		}

		sr := &statusRecorder{ResponseWriter: w}

		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// net/http contract: ErrAbortHandler is the quiet way to
			// abort a response — propagate without logging or capturing.
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			// Log unconditionally: a panic must be visible in pod logs
			// even when Sentry is disabled or ingest is down.
			log.Printf("console: panic serving %s %s: %v\n%s",
				r.Method, r.URL.Path, rec, debug.Stack())
			if reqHub != nil {
				err, ok := rec.(error)
				if !ok {
					err = fmt.Errorf("panic: %v", rec)
				}
				reqHub.CaptureException(err)
			}
			if sr.status == 0 {
				sr.WriteHeader(http.StatusInternalServerError)
				return
			}
			// A response was already started: returning normally would
			// finalize the truncated body as a clean success. Abort the
			// connection so the client can detect truncation; using
			// ErrAbortHandler keeps net/http from logging the panic a
			// second time.
			panic(http.ErrAbortHandler)
		}()

		next.ServeHTTP(sr, r)

		if reqHub != nil && sr.status >= http.StatusInternalServerError {
			fingerprint := []string{r.Method, r.URL.Path, strconv.Itoa(sr.status)}
			if !limiter.allow(fingerprint) {
				return
			}
			msg := fmt.Sprintf("%s %s -> %d", r.Method, r.URL.Path, sr.status)
			if c := carrierFrom(r.Context()); c != nil {
				if c.msg != "" {
					msg += ": " + c.msg
				}
				if c.err != nil {
					msg += ": " + c.err.Error()
				}
			}
			reqHub.WithScope(func(scope *sentry.Scope) {
				scope.SetLevel(sentry.LevelError)
				scope.SetTag("status", strconv.Itoa(sr.status))
				scope.SetFingerprint(fingerprint)
				reqHub.CaptureMessage(msg)
			})
		}
	})
}
