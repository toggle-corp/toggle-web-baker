// Command console serves the read-only Baker FrontendApp admin console.
//
// Auth is terminated entirely by oauth2-proxy in front of this process (see
// deploy/ and README); the console trusts the X-Auth-Request-User header it
// forwards and never speaks to GitHub itself.
package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/getsentry/sentry-go"

	"github.com/toggle-corp/toggle-web-baker/console/internal/k8s"
	"github.com/toggle-corp/toggle-web-baker/console/internal/loki"
	"github.com/toggle-corp/toggle-web-baker/console/internal/sentryhttp"
	"github.com/toggle-corp/toggle-web-baker/console/internal/server"
)

// fatalf flushes any buffered Sentry events, then exits via log.Fatalf
// (which calls os.Exit and would otherwise drop them).
func fatalf(hub *sentry.Hub, format string, args ...any) {
	if hub != nil {
		hub.Flush(2 * time.Second)
	}
	log.Fatalf(format, args...)
}

func main() {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	hub, err := sentryhttp.InitFromEnv()
	if err != nil {
		log.Fatalf("console: sentry: %v", err)
	}

	client, err := k8s.New()
	if err != nil {
		fatalf(hub, "console: kubernetes client: %v", err)
	}

	lokiClient := loki.New(loki.Config{
		URL:           os.Getenv("LOKI_URL"),
		BasicAuthUser: os.Getenv("LOKI_BASIC_AUTH_USER"),
		BasicAuthPass: os.Getenv("LOKI_BASIC_AUTH_PASS"),
		BearerToken:   os.Getenv("LOKI_BEARER_TOKEN"),
		TenantID:      os.Getenv("LOKI_TENANT_ID"),
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           sentryhttp.Wrap(hub, server.New(client, client, lokiClient, client).Routes()),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("console: listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fatalf(hub, "console: server: %v", err)
	}
}
