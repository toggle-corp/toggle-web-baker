// Package loki is a minimal, defensively-typed client for Grafana Loki's
// query_range API, used by the console to read a completed build's logs when
// the build pod is gone. A zero/empty Config.URL means "Loki is not configured"
// and callers fall back to the live/retained pod (see ResolveLogSource).
package loki

import (
	"net/http"
	"time"
)

// Config configures a Loki client. URL is the Loki base URL (no path); an empty
// URL marks the client unconfigured. Auth fields are all optional and applied
// to every request when set.
type Config struct {
	URL string

	BasicAuthUser string
	BasicAuthPass string
	BearerToken   string
	TenantID      string // sent as the X-Scope-OrgID header (Loki multi-tenancy)
}

// Client queries a Loki instance. Construct it with New.
type Client struct {
	cfg  Config
	http *http.Client
}

// New returns a Client for the given config. It is always non-nil; call
// Configured to learn whether it can actually reach a Loki instance.
func New(cfg Config) *Client {
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: 10 * time.Second},
	}
}

// Configured reports whether a Loki URL is set. When false, Tail returns
// ErrNotConfigured and callers should fall back to a pod-based source.
func (c *Client) Configured() bool {
	return c != nil && c.cfg.URL != ""
}
