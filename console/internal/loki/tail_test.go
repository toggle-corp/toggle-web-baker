package loki

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// query_range response: two streams, values are [unixNano, line] pairs, newest
// first within each stream (direction=backward). Tail must flatten and order
// oldest->newest globally.
const sampleResponse = `{
  "status": "success",
  "data": {
    "resultType": "streams",
    "result": [
      {"stream": {"pod": "p"}, "values": [["30","line-c"],["10","line-a"]]},
      {"stream": {"pod": "p"}, "values": [["20","line-b"]]}
    ]
  }
}`

func TestTail_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/query_range" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleResponse))
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL})
	lines, err := c.Tail(context.Background(), "ns", "p", "build", time.Time{}, time.Time{}, 0)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	want := []string{"line-a", "line-b", "line-c"}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %v", len(lines), len(want), lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line[%d] = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestTail_CapsToLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sampleResponse))
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL})
	// limit 2 keeps the newest 2 (line-b, line-c) in oldest->newest order.
	lines, err := c.Tail(context.Background(), "ns", "p", "", time.Time{}, time.Time{}, 2)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(lines) != 2 || lines[0] != "line-b" || lines[1] != "line-c" {
		t.Errorf("cap-to-limit wrong: %v", lines)
	}
}

func TestTail_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL})
	_, err := c.Tail(context.Background(), "ns", "p", "", time.Time{}, time.Time{}, 0)
	if err == nil {
		t.Fatal("expected error on non-200")
	}
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("expected *QueryError, got %T: %v", err, err)
	}
	if qe.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", qe.StatusCode)
	}
}

func TestTail_NotConfigured(t *testing.T) {
	c := New(Config{})
	_, err := c.Tail(context.Background(), "ns", "p", "", time.Time{}, time.Time{}, 0)
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
}
