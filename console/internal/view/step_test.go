package view

import "testing"

func TestStepIcon(t *testing.T) {
	cases := []struct {
		status string
		want   string
	}{
		{"Succeeded", "✅"},
		{"Running", "⏳"},
		{"Failed", "❌"},
		{"Aborted", "⏹️"},
		{"Pending", "·"},
		{"", "·"},
		{"weird", "·"},
	}
	for _, c := range cases {
		if got := (Step{Status: c.status}).Icon(); got != c.want {
			t.Errorf("Icon(%q) = %q, want %q", c.status, got, c.want)
		}
	}
}

func TestStepCSSClass(t *testing.T) {
	cases := []struct {
		status string
		want   string
	}{
		{"Succeeded", "step-succeeded"},
		{"Running", "step-running"},
		{"Failed", "step-failed"},
		{"Aborted", "step-aborted"},
		{"Pending", "step-pending"},
		{"", "step-pending"},
		{"weird", "step-pending"},
	}
	for _, c := range cases {
		if got := (Step{Status: c.status}).CSSClass(); got != c.want {
			t.Errorf("CSSClass(%q) = %q, want %q", c.status, got, c.want)
		}
	}
}

func TestStepAriaLabel(t *testing.T) {
	if got := (Step{Name: "fetch", Status: "Failed"}).AriaLabel(); got != "fetch: failed" {
		t.Errorf("AriaLabel = %q, want %q", got, "fetch: failed")
	}
	if got := (Step{Name: "clone", Status: "Succeeded"}).AriaLabel(); got != "clone: succeeded" {
		t.Errorf("AriaLabel = %q, want %q", got, "clone: succeeded")
	}
}
