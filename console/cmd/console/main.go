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

	"github.com/toggle-corp/toggle-web-baker/console/internal/k8s"
	"github.com/toggle-corp/toggle-web-baker/console/internal/loki"
	"github.com/toggle-corp/toggle-web-baker/console/internal/server"
)

func main() {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	client, err := k8s.New()
	if err != nil {
		log.Fatalf("console: kubernetes client: %v", err)
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
		Handler:           server.New(client, client, lokiClient).Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("console: listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("console: server: %v", err)
	}
}
