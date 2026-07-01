//go:build envtest

package controller

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
)

var (
	testClient client.Client
	testCtx    context.Context
)

// TestMain spins up a real apiserver+etcd (envtest), installs the generated
// CRD, and builds a client. CEL validation only runs in a real apiserver, so
// this whole suite is gated behind the `envtest` build tag.
func TestMain(m *testing.M) {
	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd")},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := testEnv.Start()
	if err != nil {
		panic("failed to start envtest environment: " + err.Error())
	}

	if err := bakerv1alpha1.AddToScheme(scheme.Scheme); err != nil {
		panic("failed to add baker scheme: " + err.Error())
	}

	testClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		panic("failed to build client: " + err.Error())
	}

	testCtx = context.Background()

	code := m.Run()

	if err := testEnv.Stop(); err != nil {
		panic("failed to stop envtest environment: " + err.Error())
	}

	os.Exit(code)
}

// validApp returns a fully-valid FrontendApp that satisfies every required
// field and CEL rule. Tests mutate it to isolate the one rule under test.
func validApp(name string) *bakerv1alpha1.FrontendApp {
	return &bakerv1alpha1.FrontendApp{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: bakerv1alpha1.FrontendAppSpec{
			Repo:        "https://example.com/repo.git",
			Ref:         "main",
			NodeVersion: 18, // satisfies the build-needs-an-image rule
			Build: bakerv1alpha1.BuildPhaseSpec{
				PhaseSpec: bakerv1alpha1.PhaseSpec{
					Command: []string{"yarn", "build"},
				},
			},
			Ingress: bakerv1alpha1.IngressConfig{
				Host: "app.example.com",
			},
		},
	}
}

func TestValidation_RejectsBuildWithoutImageOrNodeVersion(t *testing.T) {
	app := validApp("reject-no-build-image")
	app.Spec.NodeVersion = 0 // omit both nodeVersion and build.image
	app.Spec.Build.Image = ""

	err := testClient.Create(testCtx, app)
	if err == nil {
		t.Fatalf("expected Create to be rejected when build has no image source")
	}
	if !strings.Contains(err.Error(), "nodeVersion or build.image") {
		t.Fatalf("expected error mentioning the image sources, got: %v", err)
	}
}

func TestValidation_AcceptsBuildImageWithoutNodeVersion(t *testing.T) {
	app := validApp("accept-byo-build-image")
	app.Spec.NodeVersion = 0
	app.Spec.Build.Image = "docker.io/cimg/node:18.20"

	if err := testClient.Create(testCtx, app); err != nil {
		t.Fatalf("expected explicit build.image to satisfy the rule, got: %v", err)
	}
	t.Cleanup(func() { _ = testClient.Delete(testCtx, app) })
}

func TestValidation_AcceptsNodeVersionAndBuildImageTogether(t *testing.T) {
	app := validApp("accept-nodeversion-and-image")
	app.Spec.NodeVersion = 18
	app.Spec.Build.Image = "docker.io/cimg/node:18.20" // per-phase override is legal

	if err := testClient.Create(testCtx, app); err != nil {
		t.Fatalf("expected nodeVersion + explicit build.image to be accepted, got: %v", err)
	}
	t.Cleanup(func() { _ = testClient.Delete(testCtx, app) })
}

func TestValidation_RejectsZeroNodeVersion(t *testing.T) {
	// nodeVersion is Minimum=1; a bogus 0 with no build image is rejected. (An
	// explicitly-set 0 is dropped by omitempty, so this asserts the pair: no
	// image source available.)
	app := validApp("reject-zero-nodeversion")
	app.Spec.NodeVersion = 0
	app.Spec.Build.Image = ""

	if err := testClient.Create(testCtx, app); err == nil {
		t.Fatalf("expected rejection for nodeVersion 0 with no build image")
	}
}

func TestValidation_RejectsMissingBuildCommand(t *testing.T) {
	app := validApp("reject-missing-build-command")
	app.Spec.Build.Command = nil

	err := testClient.Create(testCtx, app)
	if err == nil {
		t.Fatalf("expected Create to be rejected, got nil error")
	}
	if !strings.Contains(err.Error(), "build.command") {
		t.Fatalf("expected error mentioning build.command, got: %v", err)
	}
}

func TestValidation_RejectsSecretsWithoutFetchCommand(t *testing.T) {
	app := validApp("reject-secrets-without-fetch")
	app.Spec.Secrets = []bakerv1alpha1.EnvVarWithSecret{
		{
			Name: "API_TOKEN",
			ValueFrom: bakerv1alpha1.EnvVarWithSecretSource{
				SecretKeyRef: bakerv1alpha1.SecretKeySelector{
					Name: "creds",
					Key:  "token",
				},
			},
		},
	}
	app.Spec.Fetch.Command = nil

	err := testClient.Create(testCtx, app)
	if err == nil {
		t.Fatalf("expected Create to be rejected, got nil error")
	}
	if !strings.Contains(err.Error(), "fetch.command") {
		t.Fatalf("expected error mentioning fetch.command, got: %v", err)
	}
}

func TestValidation_AcceptsSecretsWithFetchCommand(t *testing.T) {
	app := validApp("accept-secrets-with-fetch")
	app.Spec.Secrets = []bakerv1alpha1.EnvVarWithSecret{
		{
			Name: "API_TOKEN",
			ValueFrom: bakerv1alpha1.EnvVarWithSecretSource{
				SecretKeyRef: bakerv1alpha1.SecretKeySelector{
					Name: "creds",
					Key:  "token",
				},
			},
		},
	}
	app.Spec.Fetch.Command = []string{"sh", "-c", "fetch-data"}

	if err := testClient.Create(testCtx, app); err != nil {
		t.Fatalf("expected Create to succeed, got: %v", err)
	}
	t.Cleanup(func() { _ = testClient.Delete(testCtx, app) })
}

func TestValidation_RejectsOutputDirWithParentSegment(t *testing.T) {
	// "a/../b" has a ".." segment: the CEL rule rejects it (RE2 pattern alone
	// can't catch an interior "..").
	app := validApp("reject-outputdir-parent")
	app.Spec.Build.OutputDir = "a/../b"

	if err := testClient.Create(testCtx, app); err == nil {
		t.Fatalf("expected rejection for outputDir with a '..' segment")
	}
}

func TestValidation_RejectsOutputDirLeadingParent(t *testing.T) {
	app := validApp("reject-outputdir-leading-parent")
	app.Spec.Build.OutputDir = "../x"

	if err := testClient.Create(testCtx, app); err == nil {
		t.Fatalf("expected rejection for outputDir starting with '..'")
	}
}

func TestValidation_RejectsAbsoluteOutputDir(t *testing.T) {
	// A leading "/" fails the RE2 pattern (first char must be alnum/_/.).
	app := validApp("reject-outputdir-absolute")
	app.Spec.Build.OutputDir = "/abs"

	if err := testClient.Create(testCtx, app); err == nil {
		t.Fatalf("expected rejection for an absolute outputDir")
	}
}

func TestValidation_RejectsCurrentDirOutputDir(t *testing.T) {
	// "." is a "." segment: the copier would publish the ENTIRE workspace
	// (node_modules/.git/source), so the segment CEL rule rejects it even though
	// it passes the RE2 pattern.
	app := validApp("reject-outputdir-dot")
	app.Spec.Build.OutputDir = "."

	if err := testClient.Create(testCtx, app); err == nil {
		t.Fatalf("expected rejection for outputDir '.'")
	}
}

func TestValidation_RejectsTrailingSlashOutputDir(t *testing.T) {
	// "out/" has a trailing empty segment: the segment CEL rule rejects it.
	app := validApp("reject-outputdir-trailing-slash")
	app.Spec.Build.OutputDir = "out/"

	if err := testClient.Create(testCtx, app); err == nil {
		t.Fatalf("expected rejection for outputDir with a trailing slash")
	}
}

func TestValidation_AcceptsDottedOutputDirName(t *testing.T) {
	// "assets..min" contains ".." as a SUBSTRING but not as a path SEGMENT, so
	// it is a safe relative dir and must be accepted (guards against a substring
	// contains('..') false-positive).
	app := validApp("accept-outputdir-dotted")
	app.Spec.Build.OutputDir = "assets..min"

	if err := testClient.Create(testCtx, app); err != nil {
		t.Fatalf("expected 'assets..min' outputDir to be accepted, got: %v", err)
	}
	t.Cleanup(func() { _ = testClient.Delete(testCtx, app) })
}

func TestValidation_AcceptsSimpleOutputDir(t *testing.T) {
	app := validApp("accept-outputdir-simple")
	app.Spec.Build.OutputDir = "out"

	if err := testClient.Create(testCtx, app); err != nil {
		t.Fatalf("expected 'out' outputDir to be accepted, got: %v", err)
	}
	t.Cleanup(func() { _ = testClient.Delete(testCtx, app) })
}

func TestValidation_AcceptsNestedOutputDir(t *testing.T) {
	app := validApp("accept-outputdir-nested")
	app.Spec.Build.OutputDir = "build/static"

	if err := testClient.Create(testCtx, app); err != nil {
		t.Fatalf("expected 'build/static' outputDir to be accepted, got: %v", err)
	}
	t.Cleanup(func() { _ = testClient.Delete(testCtx, app) })
}

func TestValidation_AcceptsValidApp(t *testing.T) {
	app := validApp("accept-valid-app")

	if err := testClient.Create(testCtx, app); err != nil {
		t.Fatalf("expected Create to succeed, got: %v", err)
	}
	t.Cleanup(func() { _ = testClient.Delete(testCtx, app) })
}

func TestValidation_DefaultsRefToHEAD(t *testing.T) {
	app := validApp("defaults-ref-to-head")
	app.Spec.Ref = "" // omit so the apiserver applies the default

	if err := testClient.Create(testCtx, app); err != nil {
		t.Fatalf("expected Create to succeed, got: %v", err)
	}
	t.Cleanup(func() { _ = testClient.Delete(testCtx, app) })

	got := &bakerv1alpha1.FrontendApp{}
	key := client.ObjectKey{Namespace: "default", Name: "defaults-ref-to-head"}
	if err := testClient.Get(testCtx, key, got); err != nil {
		t.Fatalf("failed to Get created object: %v", err)
	}
	if got.Spec.Ref != "HEAD" {
		t.Fatalf("expected Spec.Ref defaulted to HEAD, got %q", got.Spec.Ref)
	}
}
