package loki

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// defaultLimit is the number of log lines requested when the caller passes 0.
const defaultLimit = 100

// queryRangePath is Loki's range-query endpoint.
const queryRangePath = "/loki/api/v1/query_range"

// buildSelector builds the LogQL stream selector. The container clause is
// omitted entirely when container is empty.
func buildSelector(namespace, pod, container string) string {
	var b strings.Builder
	b.WriteString(`{namespace="`)
	b.WriteString(namespace)
	b.WriteString(`",pod="`)
	b.WriteString(pod)
	b.WriteByte('"')
	if container != "" {
		b.WriteString(`,container="`)
		b.WriteString(container)
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

// buildRequest assembles the GET query_range request. Timestamps are encoded as
// unix nanoseconds (Loki accepts RFC3339 or unix-nano; we use nanoseconds for an
// unambiguous integer form). Zero-valued start/end are omitted. Auth and tenant
// headers are added only when their config fields are set.
func (c *Client) buildRequest(ctx context.Context, namespace, pod, container string, start, end time.Time, limit int) (*http.Request, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	if ctx == nil {
		ctx = context.Background()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.URL+queryRangePath, nil)
	if err != nil {
		return nil, err
	}

	q := req.URL.Query()
	q.Set("query", buildSelector(namespace, pod, container))
	q.Set("limit", strconv.Itoa(limit))
	q.Set("direction", "backward")
	if !start.IsZero() {
		q.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	}
	if !end.IsZero() {
		q.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	}
	req.URL.RawQuery = q.Encode()

	// A single Authorization header: bearer token wins over basic auth when both
	// are configured.
	switch {
	case c.cfg.BearerToken != "":
		req.Header.Set("Authorization", "Bearer "+c.cfg.BearerToken)
	case c.cfg.BasicAuthUser != "" || c.cfg.BasicAuthPass != "":
		req.SetBasicAuth(c.cfg.BasicAuthUser, c.cfg.BasicAuthPass)
	}
	if c.cfg.TenantID != "" {
		req.Header.Set("X-Scope-OrgID", c.cfg.TenantID)
	}

	return req, nil
}
