package server

import (
	"strings"
	"testing"
)

// highlightLogLine classifies a single plain-text log line by severity and
// returns safe HTML: the whole line wrapped in a span carrying a level class,
// with any leading timestamp dimmed. These tests pin the observable behavior
// (which class, escaped content) rather than the exact markup.

func TestHighlightLogLine_ClassifiesLevels(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string // expected level class
	}{
		{"error keyword", "ERROR: build failed", "log-err"},
		{"npm err", "npm ERR! code ELIFECYCLE", "log-err"},
		{"panic", "panic: runtime error", "log-err"},
		{"warning", "WARN deprecated API in use", "log-warn"},
		{"success", "Build complete: Succeeded", "log-ok"},
		{"plain", "cloning repository into /workspace", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := string(highlightLogLine(c.line))
			if c.want == "" {
				for _, lvl := range []string{"log-err", "log-warn", "log-ok"} {
					if strings.Contains(got, lvl) {
						t.Errorf("plain line got level class %q: %s", lvl, got)
					}
				}
				return
			}
			if !strings.Contains(got, c.want) {
				t.Errorf("line %q: want class %q; got %s", c.line, c.want, got)
			}
		})
	}
}

func TestHighlightLogLine_EscapesHTML(t *testing.T) {
	got := string(highlightLogLine(`error: <script>alert(1)</script>`))
	if strings.Contains(got, "<script>") {
		t.Errorf("must escape angle brackets in log content; got %s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("escaped script tag should be present; got %s", got)
	}
}

func TestHighlightLogLine_DimsLeadingTimestamp(t *testing.T) {
	got := string(highlightLogLine("2026-07-01T12:00:00Z starting build"))
	if !strings.Contains(got, "log-ts") {
		t.Errorf("leading timestamp should be wrapped in a log-ts span; got %s", got)
	}
	// The message after the timestamp must remain.
	if !strings.Contains(got, "starting build") {
		t.Errorf("message text lost; got %s", got)
	}
}
