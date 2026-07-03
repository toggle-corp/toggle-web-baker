package sentryhttp

import (
	"fmt"
	"os"

	"github.com/getsentry/sentry-go"
)

// InitFromEnv builds a Sentry hub from SENTRY_DSN, SENTRY_ENVIRONMENT and
// SENTRY_RELEASE. An empty or unset SENTRY_DSN returns (nil, nil): a nil hub
// is the fully disabled mode and safe to pass to Wrap.
func InitFromEnv() (*sentry.Hub, error) {
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
	scope.SetTag("component", "console")
	return sentry.NewHub(client, scope), nil
}
