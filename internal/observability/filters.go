package observability

import (
	"context"
	"errors"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// dropError reports whether an error attached to a log entry is routine
// operational noise that must not reach Sentry: Kubernetes optimistic-
// concurrency conflicts and (possibly wrapped) context cancellation from
// shutdown or requeue.
func dropError(err error) bool {
	if err == nil {
		return false
	}
	return apierrors.IsConflict(err) || errors.Is(err, context.Canceled)
}

// dropMessage reports whether the log message itself marks noise, e.g.
// controller-runtime's leader-election chatter on shutdown.
func dropMessage(msg string) bool {
	return strings.Contains(msg, "leader election")
}
