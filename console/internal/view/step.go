package view

import "strings"

// Step display helpers. Icon and CSSClass are paired with AriaLabel so the
// pipeline flow is conveyed by text too, never emoji/color alone.

// Icon returns the glyph for the step's status. Unknown/Pending → "·".
func (s Step) Icon() string {
	switch s.Status {
	case "Succeeded":
		return "✅"
	case "Running":
		return "⏳"
	case "Failed":
		return "❌"
	case "Aborted":
		return "⏹️"
	default: // Pending and anything unrecognised
		return "·"
	}
}

// CSSClass returns the step's CSS class. Unknown/Pending → "step-pending".
func (s Step) CSSClass() string {
	switch s.Status {
	case "Succeeded":
		return "step-succeeded"
	case "Running":
		return "step-running"
	case "Failed":
		return "step-failed"
	case "Aborted":
		return "step-aborted"
	default:
		return "step-pending"
	}
}

// AriaLabel is the accessible label, e.g. "fetch: failed".
func (s Step) AriaLabel() string {
	return s.Name + ": " + strings.ToLower(s.Status)
}
