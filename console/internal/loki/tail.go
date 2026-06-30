package loki

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"time"
)

// ErrNotConfigured is returned by Tail when the client has no Loki URL, so the
// caller can fall back to a pod-based log source.
var ErrNotConfigured = errors.New("loki: not configured")

// QueryError is a typed error for a failed Loki query (non-2xx response). It
// lets callers distinguish a Loki failure from a transport error and fall back.
type QueryError struct {
	StatusCode int
	Body       string
}

func (e *QueryError) Error() string {
	return fmt.Sprintf("loki: query failed with status %d: %s", e.StatusCode, e.Body)
}

// queryRangeResponse is the subset of Loki's query_range JSON we consume.
type queryRangeResponse struct {
	Data struct {
		Result []struct {
			// Values is a list of [unixNanoString, logLine] pairs.
			Values [][2]string `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// Tail executes a query_range against Loki and returns the log lines ordered
// oldest->newest, capped to the newest `limit` lines. A missing URL yields
// ErrNotConfigured; a non-2xx response yields a *QueryError; transport/parse
// failures are returned as-is. All so callers can degrade gracefully.
func (c *Client) Tail(ctx context.Context, namespace, pod, container string, start, end time.Time, limit int) ([]string, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	if limit <= 0 {
		limit = defaultLimit
	}

	req, err := c.buildRequest(ctx, namespace, pod, container, start, end, limit)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &QueryError{StatusCode: resp.StatusCode, Body: string(body)}
	}

	var parsed queryRangeResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("loki: decode response: %w", err)
	}

	// Flatten all (timestamp, line) pairs across streams, then order
	// oldest->newest by the nanosecond timestamp.
	type entry struct {
		ts   int64
		line string
	}
	var entries []entry
	for _, stream := range parsed.Data.Result {
		for _, v := range stream.Values {
			ts, _ := strconv.ParseInt(v[0], 10, 64) // bad ts sorts as 0, never panics
			entries = append(entries, entry{ts: ts, line: v[1]})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].ts < entries[j].ts })

	// Cap to the newest `limit` entries (drop the oldest overflow).
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}

	lines := make([]string, len(entries))
	for i, e := range entries {
		lines[i] = e.line
	}
	return lines, nil
}
