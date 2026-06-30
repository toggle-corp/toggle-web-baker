package loki

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestBuildRequest_SelectorAndParams(t *testing.T) {
	c := New(Config{
		URL:           "http://loki:3100",
		BasicAuthUser: "u",
		BasicAuthPass: "p",
		TenantID:      "tenant-1",
	})
	start := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 30, 12, 5, 0, 0, time.UTC)

	req, err := c.buildRequest(context.Background(), "mapswipe", "pod-1", "build", start, end, 50)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if req.Method != http.MethodGet {
		t.Errorf("method = %q", req.Method)
	}
	if req.URL.Path != "/loki/api/v1/query_range" {
		t.Errorf("path = %q", req.URL.Path)
	}
	q := req.URL.Query()
	wantQuery := `{namespace="mapswipe",pod="pod-1",container="build"}`
	if q.Get("query") != wantQuery {
		t.Errorf("query = %q, want %q", q.Get("query"), wantQuery)
	}
	if q.Get("limit") != "50" {
		t.Errorf("limit = %q, want 50", q.Get("limit"))
	}
	if q.Get("direction") != "backward" {
		t.Errorf("direction = %q", q.Get("direction"))
	}
	// unix nanoseconds
	if q.Get("start") != "1782820800000000000" {
		t.Errorf("start = %q", q.Get("start"))
	}
	if q.Get("end") != "1782821100000000000" {
		t.Errorf("end = %q", q.Get("end"))
	}
	// basic-auth header
	if u, p, ok := req.BasicAuth(); !ok || u != "u" || p != "p" {
		t.Errorf("basic auth = %q/%q ok=%v", u, p, ok)
	}
	if got := req.Header.Get("X-Scope-OrgID"); got != "tenant-1" {
		t.Errorf("X-Scope-OrgID = %q", got)
	}
}

func TestBuildRequest_BearerTokenTakesPrecedence(t *testing.T) {
	// When both are set, the single Authorization header carries the bearer
	// token (basic auth cannot coexist in the same header).
	c := New(Config{
		URL:           "http://loki:3100",
		BasicAuthUser: "u",
		BasicAuthPass: "p",
		BearerToken:   "tok",
	})
	req, err := c.buildRequest(context.Background(), "ns", "pod", "", time.Time{}, time.Time{}, 0)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer tok" {
		t.Errorf("authorization = %q, want Bearer tok", got)
	}
}

func TestBuildRequest_OmitsContainerWhenEmpty_DefaultLimit(t *testing.T) {
	c := New(Config{URL: "http://loki:3100"})
	req, err := c.buildRequest(context.Background(), "ns", "pod-1", "", time.Time{}, time.Time{}, 0)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	q := req.URL.Query()
	wantQuery := `{namespace="ns",pod="pod-1"}`
	if q.Get("query") != wantQuery {
		t.Errorf("query = %q, want %q", q.Get("query"), wantQuery)
	}
	if q.Get("limit") != "100" {
		t.Errorf("default limit = %q, want 100", q.Get("limit"))
	}
	// no auth headers when unset
	if _, _, ok := req.BasicAuth(); ok {
		t.Error("should not have basic auth")
	}
	if req.Header.Get("Authorization") != "" {
		t.Error("should not have bearer")
	}
	if req.Header.Get("X-Scope-OrgID") != "" {
		t.Error("should not have tenant header")
	}
}
