package observability

import (
	"fmt"
	"os"
	"time"

	"github.com/getsentry/sentry-go"
)

// InitFromEnv builds a Reporter from SENTRY_DSN, SENTRY_ENVIRONMENT and
// SENTRY_RELEASE. An empty or unset SENTRY_DSN returns (nil, nil): a nil
// Reporter is the fully disabled mode and safe to use everywhere.
func InitFromEnv() (*Reporter, error) {
	dsn := os.Getenv("SENTRY_DSN")
	if dsn == "" {
		return nil, nil
	}

	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:              dsn,
		Environment:      os.Getenv("SENTRY_ENVIRONMENT"),
		Release:          os.Getenv("SENTRY_RELEASE"),
		TracesSampleRate: 0, // errors only
	})
	if err != nil {
		return nil, fmt.Errorf("init sentry: %w", err)
	}

	scope := sentry.NewScope()
	scope.SetTag("component", "operator")
	hub := sentry.NewHub(client, scope)

	return &Reporter{hub: hub, limiter: newFingerprintLimiter(time.Now)}, nil
}
