package server

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/toggle-corp/toggle-web-baker/console/internal/k8s"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// manifestFixtureServer seeds one App carrying distinctive sentinels across the
// fields the manifest view must keep, hide, or drop: real spec content, labels,
// annotations (whose values must be hidden), and status/managedFields/metadata
// noise that must never reach the page.
func manifestFixtureServer(t *testing.T) *Server {
	t.Helper()
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{k8s.GVR: "AppList"}
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "baker.toggle-corp.com/v1alpha1",
		"kind":       "App",
		"metadata": map[string]any{
			"namespace":         "mapswipe",
			"name":              "mapswipe-uat",
			"uid":               "SENTINEL-UID",
			"resourceVersion":   "SENTINEL-RV",
			"generation":        int64(7),
			"creationTimestamp": "2026-01-01T00:00:00Z",
			"managedFields": []any{
				map[string]any{"manager": "SENTINEL-MANAGER"},
			},
			"labels": map[string]any{
				"app.kubernetes.io/name": "mapswipe",
				"tier":                   "frontend",
			},
			"annotations": map[string]any{
				"kubectl.kubernetes.io/last-applied-configuration": "SENTINEL-ANNVALUE",
				"baker.toggle-corp.com/note":                       "SENTINEL-NOTE",
			},
		},
		"spec": map[string]any{
			"group":  "grp-a",
			"source": map[string]any{"repo": "SENTINEL-REPO"},
			"auth":   map[string]any{"passwordHash": "SENTINEL-HTPASSWD"},
		},
		"status": map[string]any{
			"phase": "SENTINEL-PHASE",
		},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, app)
	return New(newClient(t, dyn), &fakePodReader{}, &fakeLokiTailer{}, nil)
}

// stripTags reduces rendered HTML to its text content (what the Copy button
// reads via textContent), so assertions see the YAML as the user would.
var tagRe = regexp.MustCompile(`<[^>]*>`)

func stripTags(s string) string { return tagRe.ReplaceAllString(s, "") }

func TestManifest_RendersKindAndSpec(t *testing.T) {
	srv := manifestFixtureServer(t)
	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/manifest", "mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	text := stripTags(rec.Body.String())
	if !strings.Contains(text, "kind: App") {
		t.Errorf("manifest should render the kind; text=%s", text)
	}
	if !strings.Contains(text, "SENTINEL-REPO") {
		t.Errorf("manifest should render a spec field; text=%s", text)
	}
}

func TestManifest_DropsStatusAndServerMetadata(t *testing.T) {
	srv := manifestFixtureServer(t)
	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/manifest", "mapswipe", "mapswipe-uat")
	body := rec.Body.String()
	for _, gone := range []string{
		"SENTINEL-PHASE",   // status
		"SENTINEL-MANAGER", // metadata.managedFields
		"SENTINEL-UID",     // metadata.uid
		"SENTINEL-RV",      // metadata.resourceVersion
		"2026-01-01",       // metadata.creationTimestamp
	} {
		if strings.Contains(body, gone) {
			t.Errorf("manifest must drop %q; body=%s", gone, body)
		}
	}
}

func TestManifest_HidesAnnotationValuesAndShowsNote(t *testing.T) {
	srv := manifestFixtureServer(t)
	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/manifest", "mapswipe", "mapswipe-uat")
	body := rec.Body.String()
	// Keys kept.
	if !strings.Contains(body, "baker.toggle-corp.com/note") {
		t.Errorf("annotation keys should be preserved; body=%s", body)
	}
	// Values hidden; real values gone.
	if !strings.Contains(body, "(hidden)") {
		t.Errorf("annotation values should be replaced with (hidden); body=%s", body)
	}
	for _, secret := range []string{"SENTINEL-ANNVALUE", "SENTINEL-NOTE"} {
		if strings.Contains(body, secret) {
			t.Errorf("real annotation value %q must not leak; body=%s", secret, body)
		}
	}
	// The security note shows when anything is masked.
	if !strings.Contains(body, "Annotation values and credentials are hidden for security reasons.") {
		t.Errorf("security note should show when annotations exist; body=%s", body)
	}
}

func TestManifest_MasksInlineAuthCredential(t *testing.T) {
	srv := manifestFixtureServer(t)
	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/manifest", "mapswipe", "mapswipe-uat")
	body := rec.Body.String()
	if strings.Contains(body, "SENTINEL-HTPASSWD") {
		t.Errorf("spec.auth.passwordHash must not leak; body=%s", body)
	}
	// The key stays visible (masked), and the rest of spec is untouched.
	if !strings.Contains(body, "passwordHash") {
		t.Errorf("passwordHash key should remain visible; body=%s", body)
	}
	if !strings.Contains(body, "SENTINEL-REPO") {
		t.Errorf("masking the credential must not drop other spec fields; body=%s", body)
	}
}

func TestManifest_NoAnnotationsNoNoteNoKey(t *testing.T) {
	// The default seeded app (seededDyn) carries no labels/annotations.
	srv := New(newClient(t, seededDyn(t, nil)), &fakePodReader{}, &fakeLokiTailer{}, nil)
	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/manifest", "mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "hidden for security reasons") {
		t.Errorf("no security note when nothing is masked; body=%s", body)
	}
	if strings.Contains(body, "annotations:") {
		t.Errorf("annotations key must be omitted when empty; body=%s", body)
	}
}

func TestManifest_PreservesLabelsVerbatim(t *testing.T) {
	srv := manifestFixtureServer(t)
	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/manifest", "mapswipe", "mapswipe-uat")
	body := rec.Body.String()
	for _, want := range []string{"app.kubernetes.io/name", "tier", "frontend"} {
		if !strings.Contains(body, want) {
			t.Errorf("labels should be preserved verbatim: missing %q; body=%s", want, body)
		}
	}
}

func TestDetail_HasShowManifestLink(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat", "mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/ns/mapswipe/app/mapswipe-uat/manifest">Show manifest</a>`) {
		t.Errorf("detail page should link to the manifest; body=%s", body)
	}
	// It must sit left of the Request rebuild form.
	if strings.Index(body, "Show manifest") > strings.Index(body, "Request rebuild") {
		t.Errorf("Show manifest should appear before Request rebuild; body=%s", body)
	}
}

func TestManifest_CopyButtonReadsManifestText(t *testing.T) {
	srv := manifestFixtureServer(t)
	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/manifest", "mapswipe", "mapswipe-uat")
	body := rec.Body.String()
	if !strings.Contains(body, "Copy YAML") {
		t.Errorf("manifest page should have a Copy YAML button; body=%s", body)
	}
	if !strings.Contains(body, "navigator.clipboard") {
		t.Errorf("Copy button should use the clipboard API; body=%s", body)
	}
	// The button copies the highlighted block's textContent — the document must
	// ship exactly once, not in a second hidden copy.
	if got := strings.Count(body, "SENTINEL-REPO"); got != 1 {
		t.Errorf("manifest text should appear exactly once, got %d; body=%s", got, body)
	}
}

// manifestServerWithSpec seeds one App carrying the given spec so the setup-hint
// behaviour can be exercised across the meaningful pipeline shapes.
func manifestServerWithSpec(t *testing.T, spec map[string]any) *Server {
	t.Helper()
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{k8s.GVR: "AppList"}
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "baker.toggle-corp.com/v1alpha1",
		"kind":       "App",
		"metadata": map[string]any{
			"namespace": "mapswipe",
			"name":      "mapswipe-uat",
		},
		"spec": spec,
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, app)
	return New(newClient(t, dyn), &fakePodReader{}, &fakeLokiTailer{}, nil)
}

func TestManifest_SetupOmittedWithNodeVersion_HintsDefaultInstall(t *testing.T) {
	srv := manifestServerWithSpec(t, map[string]any{
		"pipeline": map[string]any{
			"nodeVersion":    int64(18),
			"packageManager": "pnpm",
			"phases": map[string]any{
				"build": map[string]any{"command": []any{"yarn", "build"}},
			},
		},
	})
	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/manifest", "mapswipe", "mapswipe-uat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	text := stripTags(rec.Body.String())
	want := "setup omitted — the operator runs its default install for pnpm; the exact command is echoed in the build logs"
	if !strings.Contains(text, want) {
		t.Errorf("expected default-install hint %q; text=%s", want, text)
	}
}

func TestManifest_SetupSkip_HintsSkipped(t *testing.T) {
	srv := manifestServerWithSpec(t, map[string]any{
		"pipeline": map[string]any{
			"nodeVersion":    int64(18),
			"packageManager": "yarn",
			"phases": map[string]any{
				"setup": map[string]any{"skip": true},
				"build": map[string]any{"command": []any{"yarn", "build"}},
			},
		},
	})
	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/manifest", "mapswipe", "mapswipe-uat")
	text := stripTags(rec.Body.String())
	if !strings.Contains(text, "setup skipped by spec") {
		t.Errorf("expected skip hint; text=%s", text)
	}
	if strings.Contains(text, "default install") {
		t.Errorf("skip must not also claim a default install runs; text=%s", text)
	}
}

func TestManifest_ExplicitSetup_NoHint(t *testing.T) {
	srv := manifestServerWithSpec(t, map[string]any{
		"pipeline": map[string]any{
			"nodeVersion":    int64(18),
			"packageManager": "yarn",
			"phases": map[string]any{
				"setup": map[string]any{"command": []any{"yarn", "install"}},
				"build": map[string]any{"command": []any{"yarn", "build"}},
			},
		},
	})
	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/manifest", "mapswipe", "mapswipe-uat")
	text := stripTags(rec.Body.String())
	for _, unwanted := range []string{"default install", "setup omitted", "setup skipped"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("explicit setup should get no hint, found %q; text=%s", unwanted, text)
		}
	}
}

func TestManifest_SetupOmittedNoNodeVersion_NoHint(t *testing.T) {
	// BYO: no nodeVersion, so no default install runs and no setup phase exists.
	srv := manifestServerWithSpec(t, map[string]any{
		"pipeline": map[string]any{
			"packageManager": "yarn",
			"phases": map[string]any{
				"build": map[string]any{"image": "node:18", "command": []any{"yarn", "build"}},
			},
		},
	})
	rec := doGet(srv, "/ns/mapswipe/app/mapswipe-uat/manifest", "mapswipe", "mapswipe-uat")
	text := stripTags(rec.Body.String())
	for _, unwanted := range []string{"default install", "setup omitted", "setup skipped"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("BYO (no nodeVersion) should get no hint, found %q; text=%s", unwanted, text)
		}
	}
}

// setupHint must mirror the operator's phaseConfigured predicate (image or
// command only): a setup block carrying just env/memoryLimit/runAsUser still
// gets the default install injected, so it still needs the hint. Table-driven
// on the pure function — the HTTP tests above prove the hint reaches the page.
func TestSetupHint_OperatorParity(t *testing.T) {
	cases := []struct {
		name  string
		setup map[string]any
		want  string // substring; "" means no hint at all
	}{
		{"env-only setup still hints", map[string]any{"env": []any{map[string]any{"name": "CI", "value": "true"}}}, "default install for yarn"},
		{"memoryLimit-only setup still hints", map[string]any{"memoryLimit": "1Gi"}, "default install for yarn"},
		{"explicit skip false hints", map[string]any{"skip": false}, "default install for yarn"},
		{"image-only setup is explicit", map[string]any{"image": "node:18"}, ""},
		{"command-only setup is explicit", map[string]any{"command": []any{"yarn", "install"}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{Object: map[string]any{
				"spec": map[string]any{
					"pipeline": map[string]any{
						"nodeVersion":    int64(18),
						"packageManager": "yarn",
						"phases":         map[string]any{"setup": tc.setup},
					},
				},
			}}
			got := setupHint(obj)
			if tc.want == "" && got != "" {
				t.Fatalf("want no hint, got %q", got)
			}
			if tc.want != "" && !strings.Contains(got, tc.want) {
				t.Fatalf("want hint containing %q, got %q", tc.want, got)
			}
		})
	}
}

func TestManifest_UnknownAppIs404(t *testing.T) {
	srv := manifestFixtureServer(t)
	rec := doGet(srv, "/ns/mapswipe/app/nope/manifest", "mapswipe", "nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "App not found") {
		t.Errorf("unknown app should render the App-not-found error; body=%s", rec.Body.String())
	}
}
