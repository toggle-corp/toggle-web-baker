package controller

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The CRD reference (docs/app-crd-reference.md) documents each compiled operator
// default as a literal "default N" in the field's godoc. That number is typed by
// hand in api/v1alpha1 godoc, so it could drift from the real fallback in
// Defaults(). This guard binds the two: it asserts the generated doc contains the
// EXACT phrase built from the exported Default* constant. Change a constant
// without regenerating the doc (`just crd-docs`) — or vice versa — and this test
// goes red. Add a new operator-default field: add one row here.
//
// Only fields with a compiled numeric default belong here; chart-owned-only
// defaults (schedule/interval/timeout/memoryLimit) have no number to check.
func TestCRDDocReference_OperatorDefaults(t *testing.T) {
	// Test working dir is the package dir (internal/controller); the doc lives at
	// the repo root under docs/.
	docPath := filepath.Join("..", "..", "docs", "app-crd-reference.md")
	raw, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("reading %s: %v", docPath, err)
	}
	doc := string(raw)

	checks := []struct {
		field   string // for the failure message only
		snippet string // exact phrase the generated doc must contain
	}{
		{"spec.history.keepRecent", fmt.Sprintf("config historyKeepRecent, default %d", DefaultHistoryKeepRecent)},
		{"spec.history.keepFailed", fmt.Sprintf("config historyKeepFailed, default %d", DefaultHistoryKeepFailed)},
		{"spec.scheduledBuilds.alertThreshold", fmt.Sprintf("config scheduledAlertThreshold, default %d", DefaultScheduledAlertThreshold)},
	}

	for _, c := range checks {
		if !strings.Contains(doc, c.snippet) {
			t.Errorf("%s: doc missing %q — the godoc number drifted from the Default* constant, or the doc wasn't regenerated (run `just crd-docs`)", c.field, c.snippet)
		}
	}
}
