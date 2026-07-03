package sentryhttp

import "testing"

func TestInitFromEnvEmptyDSNDisables(t *testing.T) {
	t.Setenv("SENTRY_DSN", "")

	hub, err := InitFromEnv()
	if err != nil {
		t.Fatalf("InitFromEnv: %v", err)
	}
	if hub != nil {
		t.Fatalf("hub = %v, want nil when SENTRY_DSN is empty", hub)
	}
}
