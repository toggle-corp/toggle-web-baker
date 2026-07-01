package server

import (
	"html/template"
	"regexp"
	"strings"
)

// Log highlighting is heuristic and line-level: build steps run in pods without
// a TTY, so tools emit plain text (no ANSI). We classify each line by a few
// high-signal patterns and dim a leading timestamp. Precedence is error > warn >
// ok; the first matching family wins so a line reading "error" is never downgraded.

var (
	reLogErr  = regexp.MustCompile(`(?i)(\berror\b|\berrors\b|\bfatal\b|\bfailed\b|\bfailure\b|\bpanic\b|npm ERR!|exit (code|status) [1-9])`)
	reLogWarn = regexp.MustCompile(`(?i)(\bwarn\b|\bwarning\b|\bdeprecated\b)`)
	reLogOk   = regexp.MustCompile(`(?i)(\bsucceeded\b|\bsuccessfully\b|\bsuccess\b|build complete|\bdone\b)`)
	// Leading timestamp: RFC3339 (2026-07-01T12:00:00Z) or a bracketed/bare clock
	// ([12:00:00] / 12:00:00.123). Captured so it can be dimmed independently.
	reLogTS = regexp.MustCompile(`^(\[?\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}\S*\]?|\[\d{2}:\d{2}:\d{2}(?:\.\d+)?\]|\d{2}:\d{2}:\d{2}(?:\.\d+)?)(\s+)`)
)

// logLevelClass returns the severity class for a line, or "" for plain lines.
func logLevelClass(line string) string {
	switch {
	case reLogErr.MatchString(line):
		return "log-err"
	case reLogWarn.MatchString(line):
		return "log-warn"
	case reLogOk.MatchString(line):
		return "log-ok"
	default:
		return ""
	}
}

// highlightLogLine wraps one escaped log line in a span carrying its level
// class, dimming any leading timestamp. The result is trusted HTML (all dynamic
// content is escaped here) so the template can emit it via {{loghl .}}.
func highlightLogLine(line string) template.HTML {
	class := "log-ln"
	if lvl := logLevelClass(line); lvl != "" {
		class += " " + lvl
	}

	var b strings.Builder
	b.WriteString(`<span class="`)
	b.WriteString(class)
	b.WriteString(`">`)
	if m := reLogTS.FindStringSubmatch(line); m != nil {
		b.WriteString(`<span class="log-ts">`)
		b.WriteString(template.HTMLEscapeString(m[1]))
		b.WriteString(`</span>`)
		b.WriteString(template.HTMLEscapeString(m[2]))
		b.WriteString(template.HTMLEscapeString(line[len(m[0]):]))
	} else {
		b.WriteString(template.HTMLEscapeString(line))
	}
	b.WriteString(`</span>`)
	return template.HTML(b.String())
}
