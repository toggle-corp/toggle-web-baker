// Package sentryhttp reports console HTTP failures (panics and 5xx
// responses) to Sentry. A nil *sentry.Hub disables reporting entirely while
// keeping panic recovery and the error carrier functional.
package sentryhttp

import (
	"fmt"
	"net/http"
	"strconv"

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

// userFrom reads the GitHub username oauth2-proxy injects into the upstream
// request: X-Forwarded-User in reverse-proxy mode, X-Auth-Request-User as the
// nginx auth_request fallback. Empty means anonymous — no user is attached.
func userFrom(r *http.Request) string {
	if u := r.Header.Get("X-Forwarded-User"); u != "" {
		return u
	}
	return r.Header.Get("X-Auth-Request-User")
}

// Wrap returns a handler that recovers panics (writing a 500) and, when hub
// is non-nil, reports panics and 5xx responses to Sentry.
func Wrap(hub *sentry.Hub, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqHub *sentry.Hub
		if hub != nil {
			reqHub = hub.Clone()
			reqHub.Scope().SetRequest(r)
			if user := userFrom(r); user != "" {
				reqHub.Scope().SetUser(sentry.User{Username: user})
			}
		}

		// Plant the carrier unconditionally (even with a nil hub) so
		// handlers can always call AttachError.
		r = r.WithContext(WithCarrier(r.Context()))

		sr := &statusRecorder{ResponseWriter: w}

		defer func() {
			if rec := recover(); rec != nil {
				if reqHub != nil {
					err, ok := rec.(error)
					if !ok {
						err = fmt.Errorf("panic: %v", rec)
					}
					reqHub.CaptureException(err)
				}
				if sr.status == 0 {
					sr.WriteHeader(http.StatusInternalServerError)
				}
			}
		}()

		next.ServeHTTP(sr, r)

		if reqHub != nil && sr.status >= http.StatusInternalServerError {
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
				reqHub.CaptureMessage(msg)
			})
		}
	})
}
