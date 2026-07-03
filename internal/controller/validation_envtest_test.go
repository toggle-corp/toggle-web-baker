//go:build envtest

package controller

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
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

// validApp returns a fully-valid App that satisfies every required
// field and CEL rule. Tests mutate it to isolate the one rule under test.
func validApp(name string) *bakerv1alpha1.App {
	return &bakerv1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: bakerv1alpha1.AppSpec{
			Repo: "https://example.com/repo.git",
			Ref:  "main",
			Pipeline: bakerv1alpha1.PipelineSpec{
				NodeVersion: 18, // satisfies the build-needs-an-image rule
				Phases: bakerv1alpha1.PhasesSpec{
					Build: bakerv1alpha1.BuildPhaseSpec{
						PhaseSpec: bakerv1alpha1.PhaseSpec{
							Command: []string{"yarn", "build"},
						},
					},
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
	app.Spec.Pipeline.NodeVersion = 0 // omit both nodeVersion and build.image
	app.Spec.Pipeline.Phases.Build.Image = ""

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
	app.Spec.Pipeline.NodeVersion = 0
	app.Spec.Pipeline.Phases.Build.Image = "docker.io/cimg/node:18.20"

	if err := testClient.Create(testCtx, app); err != nil {
		t.Fatalf("expected explicit build.image to satisfy the rule, got: %v", err)
	}
	t.Cleanup(func() { _ = testClient.Delete(testCtx, app) })
}

func TestValidation_AcceptsNodeVersionAndBuildImageTogether(t *testing.T) {
	app := validApp("accept-nodeversion-and-image")
	app.Spec.Pipeline.NodeVersion = 18
	app.Spec.Pipeline.Phases.Build.Image = "docker.io/cimg/node:18.20" // per-phase override is legal

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
	app.Spec.Pipeline.NodeVersion = 0
	app.Spec.Pipeline.Phases.Build.Image = ""

	if err := testClient.Create(testCtx, app); err == nil {
		t.Fatalf("expected rejection for nodeVersion 0 with no build image")
	}
}

func TestValidation_RejectsMissingBuildCommand(t *testing.T) {
	app := validApp("reject-missing-build-command")
	app.Spec.Pipeline.Phases.Build.Command = nil

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
	app.Spec.Pipeline.Phases.Fetch.Secrets = []bakerv1alpha1.EnvVarWithSecret{
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
	app.Spec.Pipeline.Phases.Fetch.Command = nil

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
	app.Spec.Pipeline.Phases.Fetch.Secrets = []bakerv1alpha1.EnvVarWithSecret{
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
	app.Spec.Pipeline.Phases.Fetch.Command = []string{"sh", "-c", "fetch-data"}

	if err := testClient.Create(testCtx, app); err != nil {
		t.Fatalf("expected Create to succeed, got: %v", err)
	}
	t.Cleanup(func() { _ = testClient.Delete(testCtx, app) })
}

func TestValidation_RejectsOutputDirWithParentSegment(t *testing.T) {
	// "a/../b" has a ".." segment: the CEL rule rejects it (RE2 pattern alone
	// can't catch an interior "..").
	app := validApp("reject-outputdir-parent")
	app.Spec.Pipeline.Phases.Build.OutputDir = "a/../b"

	if err := testClient.Create(testCtx, app); err == nil {
		t.Fatalf("expected rejection for outputDir with a '..' segment")
	}
}

func TestValidation_RejectsOutputDirLeadingParent(t *testing.T) {
	app := validApp("reject-outputdir-leading-parent")
	app.Spec.Pipeline.Phases.Build.OutputDir = "../x"

	if err := testClient.Create(testCtx, app); err == nil {
		t.Fatalf("expected rejection for outputDir starting with '..'")
	}
}

func TestValidation_RejectsAbsoluteOutputDir(t *testing.T) {
	// A leading "/" fails the RE2 pattern (first char must be alnum/_/.).
	app := validApp("reject-outputdir-absolute")
	app.Spec.Pipeline.Phases.Build.OutputDir = "/abs"

	if err := testClient.Create(testCtx, app); err == nil {
		t.Fatalf("expected rejection for an absolute outputDir")
	}
}

func TestValidation_RejectsCurrentDirOutputDir(t *testing.T) {
	// "." is a "." segment: the copier would publish the ENTIRE workspace
	// (node_modules/.git/source), so the segment CEL rule rejects it even though
	// it passes the RE2 pattern.
	app := validApp("reject-outputdir-dot")
	app.Spec.Pipeline.Phases.Build.OutputDir = "."

	if err := testClient.Create(testCtx, app); err == nil {
		t.Fatalf("expected rejection for outputDir '.'")
	}
}

func TestValidation_RejectsTrailingSlashOutputDir(t *testing.T) {
	// "out/" has a trailing empty segment: the segment CEL rule rejects it.
	app := validApp("reject-outputdir-trailing-slash")
	app.Spec.Pipeline.Phases.Build.OutputDir = "out/"

	if err := testClient.Create(testCtx, app); err == nil {
		t.Fatalf("expected rejection for outputDir with a trailing slash")
	}
}

func TestValidation_AcceptsDottedOutputDirName(t *testing.T) {
	// "assets..min" contains ".." as a SUBSTRING but not as a path SEGMENT, so
	// it is a safe relative dir and must be accepted (guards against a substring
	// contains('..') false-positive).
	app := validApp("accept-outputdir-dotted")
	app.Spec.Pipeline.Phases.Build.OutputDir = "assets..min"

	if err := testClient.Create(testCtx, app); err != nil {
		t.Fatalf("expected 'assets..min' outputDir to be accepted, got: %v", err)
	}
	t.Cleanup(func() { _ = testClient.Delete(testCtx, app) })
}

func TestValidation_AcceptsSimpleOutputDir(t *testing.T) {
	app := validApp("accept-outputdir-simple")
	app.Spec.Pipeline.Phases.Build.OutputDir = "out"

	if err := testClient.Create(testCtx, app); err != nil {
		t.Fatalf("expected 'out' outputDir to be accepted, got: %v", err)
	}
	t.Cleanup(func() { _ = testClient.Delete(testCtx, app) })
}

func TestValidation_AcceptsNestedOutputDir(t *testing.T) {
	app := validApp("accept-outputdir-nested")
	app.Spec.Pipeline.Phases.Build.OutputDir = "build/static"

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

func TestValidation_AcceptsValidGroupLabel(t *testing.T) {
	app := validApp("accept-group-label")
	app.Spec.Group = "mapswipe"

	if err := testClient.Create(testCtx, app); err != nil {
		t.Fatalf("expected 'mapswipe' group to be accepted, got: %v", err)
	}
	t.Cleanup(func() { _ = testClient.Delete(testCtx, app) })
}

func TestValidation_RejectsUppercaseGroup(t *testing.T) {
	app := validApp("reject-group-uppercase")
	app.Spec.Group = "MapSwipe"

	if err := testClient.Create(testCtx, app); err == nil {
		t.Fatalf("expected rejection for uppercase group label")
	}
}

func TestValidation_RejectsTrailingHyphenGroup(t *testing.T) {
	app := validApp("reject-group-trailing-hyphen")
	app.Spec.Group = "mapswipe-"

	if err := testClient.Create(testCtx, app); err == nil {
		t.Fatalf("expected rejection for group label with a trailing hyphen")
	}
}

func TestValidation_RejectsOverlongGroup(t *testing.T) {
	app := validApp("reject-group-overlong")
	app.Spec.Group = strings.Repeat("a", 64) // MaxLength is 63

	if err := testClient.Create(testCtx, app); err == nil {
		t.Fatalf("expected rejection for group label longer than 63 chars")
	}
}

func TestValidation_RejectsAuthWithBothSources(t *testing.T) {
	// passwordHash and secretRef are alternative sources for the same htpasswd
	// line; setting both is ambiguous (which one wins?), so the CEL rule demands
	// exactly one.
	hash := "user:$2y$05$abcdefghijklmnopqrstuv"
	app := validApp("reject-auth-both-sources")
	app.Spec.Auth = &bakerv1alpha1.AuthConfig{
		PasswordHash: &hash,
		SecretRef:    &bakerv1alpha1.AuthSecretRef{Name: "creds", Key: "htpasswd"},
	}

	err := testClient.Create(testCtx, app)
	if err == nil {
		t.Fatalf("expected rejection when both passwordHash and secretRef are set")
	}
	if !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("expected error mentioning 'exactly one', got: %v", err)
	}
}

func TestValidation_RejectsAuthWithNoSource(t *testing.T) {
	// An empty auth block would silently mean "no auth" while LOOKING like auth
	// was configured; if auth is present it must actually carry a credential.
	app := validApp("reject-auth-no-source")
	app.Spec.Auth = &bakerv1alpha1.AuthConfig{}

	if err := testClient.Create(testCtx, app); err == nil {
		t.Fatalf("expected rejection for auth with neither passwordHash nor secretRef")
	}
}

func TestValidation_AcceptsAuthPasswordHashOnly(t *testing.T) {
	hash := "user:$2y$05$abcdefghijklmnopqrstuv"
	app := validApp("accept-auth-passwordhash")
	app.Spec.Auth = &bakerv1alpha1.AuthConfig{PasswordHash: &hash}

	if err := testClient.Create(testCtx, app); err != nil {
		t.Fatalf("expected auth with only passwordHash to be accepted, got: %v", err)
	}
	t.Cleanup(func() { _ = testClient.Delete(testCtx, app) })
}

func TestValidation_AcceptsAuthSecretRefOnly(t *testing.T) {
	app := validApp("accept-auth-secretref")
	app.Spec.Auth = &bakerv1alpha1.AuthConfig{
		SecretRef: &bakerv1alpha1.AuthSecretRef{Name: "creds", Key: "htpasswd"},
	}

	if err := testClient.Create(testCtx, app); err != nil {
		t.Fatalf("expected auth with only secretRef to be accepted, got: %v", err)
	}
	t.Cleanup(func() { _ = testClient.Delete(testCtx, app) })
}

func TestValidation_RejectsMissingIngressHost(t *testing.T) {
	// ingress.host is Required: without a host there is nothing to route. The
	// typed struct always serializes host (no omitempty), and structural-schema
	// `required` only checks key PRESENCE (an empty string passes), so we must
	// drop the key via unstructured to exercise the rule.
	app := validApp("reject-missing-ingress-host")
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(app)
	if err != nil {
		t.Fatalf("failed to convert app to unstructured: %v", err)
	}
	unstructured.RemoveNestedField(obj, "spec", "ingress", "host")
	u := &unstructured.Unstructured{Object: obj}
	u.SetGroupVersionKind(bakerv1alpha1.GroupVersion.WithKind("App"))

	err = testClient.Create(testCtx, u)
	if err == nil {
		t.Fatalf("expected rejection for ingress without a host")
	}
	if !strings.Contains(err.Error(), "host") {
		t.Fatalf("expected error mentioning host, got: %v", err)
	}
}

func TestValidation_RejectsNegativeNodeVersion(t *testing.T) {
	// nodeVersion is Minimum=1. Unlike the zero case (dropped by omitempty), -1
	// survives serialization, so this hits the numeric bound itself even though a
	// BYO build.image satisfies the image-source CEL rule.
	app := validApp("reject-negative-nodeversion")
	app.Spec.Pipeline.NodeVersion = -1
	app.Spec.Pipeline.Phases.Build.Image = "docker.io/cimg/node:18.20"

	if err := testClient.Create(testCtx, app); err == nil {
		t.Fatalf("expected rejection for negative nodeVersion")
	}
}

func TestValidation_RejectsZeroTimeout(t *testing.T) {
	// An explicit "0s" is rejected: unset (nil) is the way to ask for the
	// operator default, and a zero deadline is never a sane pipeline bound.
	app := validApp("reject-zero-timeout")
	app.Spec.Pipeline.Timeout = &metav1.Duration{Duration: 0}

	err := testClient.Create(testCtx, app)
	if err == nil {
		t.Fatalf("expected rejection for explicit zero pipeline.timeout")
	}
	if !strings.Contains(err.Error(), "positive duration") {
		t.Fatalf("expected error mentioning positive duration, got: %v", err)
	}
}

func TestValidation_RejectsNegativeTimeout(t *testing.T) {
	app := validApp("reject-negative-timeout")
	app.Spec.Pipeline.Timeout = &metav1.Duration{Duration: -5 * time.Minute}

	if err := testClient.Create(testCtx, app); err == nil {
		t.Fatalf("expected rejection for negative pipeline.timeout")
	}
}

func TestValidation_AcceptsPositiveTimeout(t *testing.T) {
	app := validApp("accept-positive-timeout")
	app.Spec.Pipeline.Timeout = &metav1.Duration{Duration: 90 * time.Minute}

	if err := testClient.Create(testCtx, app); err != nil {
		t.Fatalf("expected 90m pipeline.timeout to be accepted, got: %v", err)
	}
	t.Cleanup(func() { _ = testClient.Delete(testCtx, app) })
}

func TestValidation_AcceptsAbsentTimeout(t *testing.T) {
	// validApp sets no timeout: nil must stay valid (operator default applies).
	app := validApp("accept-absent-timeout")

	if err := testClient.Create(testCtx, app); err != nil {
		t.Fatalf("expected absent pipeline.timeout to be accepted, got: %v", err)
	}
	t.Cleanup(func() { _ = testClient.Delete(testCtx, app) })
}

func TestValidation_RejectsMalformedMemoryLimit(t *testing.T) {
	// A memoryLimit that is not a k8s memory quantity used to be silently
	// ignored (operator default applied). It must now fail at admission.
	for _, bad := range []string{"banana", "2 Gi", "-1Gi", "2Gib"} {
		app := validApp("reject-memlimit")
		app.Spec.Pipeline.Phases.Build.MemoryLimit = bad

		if err := testClient.Create(testCtx, app); err == nil {
			t.Fatalf("expected rejection for memoryLimit %q", bad)
		}
	}
}

func TestValidation_AcceptsQuantityMemoryLimit(t *testing.T) {
	for i, good := range []string{"2Gi", "512Mi", "1.5Gi", "2G"} {
		app := validApp("accept-memlimit-" + string(rune('a'+i)))
		app.Spec.Pipeline.Phases.Build.MemoryLimit = good

		if err := testClient.Create(testCtx, app); err != nil {
			t.Fatalf("expected memoryLimit %q to be accepted, got: %v", good, err)
		}
		t.Cleanup(func() { _ = testClient.Delete(testCtx, app) })
	}
}

func TestValidation_RejectsNegativeStorageBytes(t *testing.T) {
	// Byte thresholds are absolute sizes; negatives are meaningless. Zero keeps
	// its "unset/disabled" meaning (omitempty), so the bound is Minimum=0.
	app := validApp("reject-negative-cleanup-bytes")
	app.Spec.Storage.Cache.CleanupBytes = -1

	if err := testClient.Create(testCtx, app); err == nil {
		t.Fatalf("expected rejection for negative cache.cleanupBytes")
	}

	app = validApp("reject-negative-rundelta-bytes")
	app.Spec.Storage.DataCache.RunDeltaBytes = -1

	if err := testClient.Create(testCtx, app); err == nil {
		t.Fatalf("expected rejection for negative dataCache.runDeltaBytes")
	}
}

func TestValidation_RejectsCacheCleanupNotBelowAlert(t *testing.T) {
	// cleanup must trigger BEFORE the alert threshold, otherwise the operator
	// would page a human for a condition it was configured to fix itself.
	app := validApp("reject-cache-cleanup-ge-alert")
	app.Spec.Storage.Cache.CleanupBytes = 200
	app.Spec.Storage.Cache.AlertBytes = 100

	err := testClient.Create(testCtx, app)
	if err == nil {
		t.Fatalf("expected rejection for cache.cleanupBytes >= cache.alertBytes")
	}
	if !strings.Contains(err.Error(), "cleanupBytes must be <") {
		t.Fatalf("expected error mentioning the cleanup/alert ordering, got: %v", err)
	}
}

func TestValidation_RejectsOutputAlertNotBelowCap(t *testing.T) {
	// The cap is the hard ceiling for the served bundle; an alert at or above it
	// could never fire before the cap kicks in, so the ordering is enforced.
	app := validApp("reject-output-alert-ge-cap")
	app.Spec.Storage.Output.AlertBytes = 100
	app.Spec.Storage.Output.CapBytes = 100

	if err := testClient.Create(testCtx, app); err == nil {
		t.Fatalf("expected rejection for output.alertBytes >= output.capBytes")
	}
}

func TestValidation_AcceptsOrderedStorageThresholds(t *testing.T) {
	// cleanup < alert and alert < cap is the intended configuration shape.
	app := validApp("accept-ordered-storage")
	app.Spec.Storage.Cache.CleanupBytes = 100
	app.Spec.Storage.Cache.AlertBytes = 200
	app.Spec.Storage.Output.AlertBytes = 300
	app.Spec.Storage.Output.CapBytes = 400

	if err := testClient.Create(testCtx, app); err != nil {
		t.Fatalf("expected ordered storage thresholds to be accepted, got: %v", err)
	}
	t.Cleanup(func() { _ = testClient.Delete(testCtx, app) })
}

func TestValidation_AcceptsAlertBytesWithoutCleanupBytes(t *testing.T) {
	// Only alertBytes set (cleanupBytes stays 0/omitted): the has() guards mean
	// the ordering rule only applies when BOTH thresholds are present, so a
	// partial config must not be rejected.
	app := validApp("accept-alert-without-cleanup")
	app.Spec.Storage.Cache.AlertBytes = 100

	if err := testClient.Create(testCtx, app); err != nil {
		t.Fatalf("expected lone cache.alertBytes to be accepted, got: %v", err)
	}
	t.Cleanup(func() { _ = testClient.Delete(testCtx, app) })
}

func TestValidation_RejectsNegativeKeepReleases(t *testing.T) {
	app := validApp("reject-negative-keep-releases")
	app.Spec.KeepReleases = -1

	if err := testClient.Create(testCtx, app); err == nil {
		t.Fatalf("expected rejection for negative keepReleases")
	}
}

func TestValidation_RejectsMalformedRepo(t *testing.T) {
	// A garbage repo used to only fail minutes later at clone time. The shape
	// check is deliberately loose (https/ssh/scp-style) — it must never reject
	// a URL git can clone.
	for _, bad := range []string{"not a url", "example.com/repo.git", ""} {
		app := validApp("reject-repo")
		app.Spec.Repo = bad

		if err := testClient.Create(testCtx, app); err == nil {
			t.Fatalf("expected rejection for repo %q", bad)
		}
	}
}

func TestValidation_AcceptsGitTransportRepos(t *testing.T) {
	for i, good := range []string{
		"https://github.com/mapswipe/website",
		"http://gitea.local/org/repo.git",
		"git@github.com:org/repo.git",
		"ssh://git@github.com/org/repo.git",
	} {
		app := validApp("accept-repo-" + string(rune('a'+i)))
		app.Spec.Repo = good

		if err := testClient.Create(testCtx, app); err != nil {
			t.Fatalf("expected repo %q to be accepted, got: %v", good, err)
		}
		t.Cleanup(func() { _ = testClient.Delete(testCtx, app) })
	}
}

func TestValidation_DefaultsOutputDirToDist(t *testing.T) {
	// The copier has always treated empty OUTPUT_DIR as "dist"; the CRD default
	// makes that visible in the stored spec / kubectl explain instead of being
	// buried in controller logic.
	app := validApp("defaults-outputdir-dist")

	if err := testClient.Create(testCtx, app); err != nil {
		t.Fatalf("expected Create to succeed, got: %v", err)
	}
	t.Cleanup(func() { _ = testClient.Delete(testCtx, app) })

	got := &bakerv1alpha1.App{}
	key := client.ObjectKey{Namespace: "default", Name: "defaults-outputdir-dist"}
	if err := testClient.Get(testCtx, key, got); err != nil {
		t.Fatalf("failed to Get created object: %v", err)
	}
	if got.Spec.Pipeline.Phases.Build.OutputDir != "dist" {
		t.Fatalf("expected outputDir defaulted to dist, got %q", got.Spec.Pipeline.Phases.Build.OutputDir)
	}
}

func TestValidation_DefaultsRefToHEAD(t *testing.T) {
	app := validApp("defaults-ref-to-head")
	app.Spec.Ref = "" // omit so the apiserver applies the default

	if err := testClient.Create(testCtx, app); err != nil {
		t.Fatalf("expected Create to succeed, got: %v", err)
	}
	t.Cleanup(func() { _ = testClient.Delete(testCtx, app) })

	got := &bakerv1alpha1.App{}
	key := client.ObjectKey{Namespace: "default", Name: "defaults-ref-to-head"}
	if err := testClient.Get(testCtx, key, got); err != nil {
		t.Fatalf("failed to Get created object: %v", err)
	}
	if got.Spec.Ref != "HEAD" {
		t.Fatalf("expected Spec.Ref defaulted to HEAD, got %q", got.Spec.Ref)
	}
}

// ---- trigger opt-in structs (scheduledBuilds / watchCommits) ----------------

func TestValidation_AcceptsEnabledTriggersWithTuning(t *testing.T) {
	app := validApp("accept-triggers")
	app.Spec.ScheduledBuilds = &bakerv1alpha1.ScheduledBuildsSpec{Enabled: true, Schedule: "0 3 * * *"}
	app.Spec.WatchCommits = &bakerv1alpha1.WatchCommitsSpec{Enabled: true, Interval: "5m"}

	if err := testClient.Create(testCtx, app); err != nil {
		t.Fatalf("expected Create to succeed, got: %v", err)
	}
	t.Cleanup(func() { _ = testClient.Delete(testCtx, app) })
}

func TestValidation_RejectsSubMinuteWatchInterval(t *testing.T) {
	app := validApp("reject-subminute-interval")
	app.Spec.WatchCommits = &bakerv1alpha1.WatchCommitsSpec{Enabled: true, Interval: "30s"}

	if err := testClient.Create(testCtx, app); err == nil {
		t.Fatalf("expected rejection for sub-minute watch interval")
	}
}

func TestValidation_RejectsGarbageWatchInterval(t *testing.T) {
	app := validApp("reject-garbage-interval")
	app.Spec.WatchCommits = &bakerv1alpha1.WatchCommitsSpec{Enabled: true, Interval: "often"}

	if err := testClient.Create(testCtx, app); err == nil {
		t.Fatalf("expected rejection for non-duration watch interval")
	}
}

// enabled is Required inside each trigger struct precisely so that
// `scheduledBuilds: {schedule: ...}` (tuning without an explicit decision)
// fails at admission instead of silently doing nothing. The typed struct
// always serializes enabled (no omitempty), so drop the key via unstructured.
func TestValidation_RejectsTriggerStructWithoutEnabled(t *testing.T) {
	for _, field := range []string{"scheduledBuilds", "watchCommits"} {
		app := validApp("reject-" + strings.ToLower(field) + "-no-enabled")
		app.Spec.ScheduledBuilds = &bakerv1alpha1.ScheduledBuildsSpec{Enabled: true, Schedule: "0 3 * * *"}
		app.Spec.WatchCommits = &bakerv1alpha1.WatchCommitsSpec{Enabled: true, Interval: "5m"}
		obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(app)
		if err != nil {
			t.Fatalf("failed to convert app to unstructured: %v", err)
		}
		unstructured.RemoveNestedField(obj, "spec", field, "enabled")
		u := &unstructured.Unstructured{Object: obj}
		u.SetGroupVersionKind(bakerv1alpha1.GroupVersion.WithKind("App"))

		if err := testClient.Create(testCtx, u); err == nil {
			t.Errorf("expected rejection for %s without enabled", field)
			_ = testClient.Delete(testCtx, u)
		}
	}
}

// The old flat spec.schedule is GONE from the schema: a manifest still carrying
// it is admitted (structural schemas prune unknown fields) but the field is
// silently dropped — callers must migrate to scheduledBuilds.
func TestValidation_OldFlatScheduleIsPruned(t *testing.T) {
	app := validApp("old-flat-schedule-pruned")
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(app)
	if err != nil {
		t.Fatalf("failed to convert app to unstructured: %v", err)
	}
	if err := unstructured.SetNestedField(obj, "0 */6 * * *", "spec", "schedule"); err != nil {
		t.Fatalf("set spec.schedule: %v", err)
	}
	u := &unstructured.Unstructured{Object: obj}
	u.SetGroupVersionKind(bakerv1alpha1.GroupVersion.WithKind("App"))

	if err := testClient.Create(testCtx, u); err != nil {
		t.Fatalf("expected Create with legacy spec.schedule to succeed (pruned), got: %v", err)
	}
	t.Cleanup(func() { _ = testClient.Delete(testCtx, u) })

	stored := &unstructured.Unstructured{}
	stored.SetGroupVersionKind(bakerv1alpha1.GroupVersion.WithKind("App"))
	if err := testClient.Get(testCtx, client.ObjectKeyFromObject(u), stored); err != nil {
		t.Fatalf("get stored app: %v", err)
	}
	if _, found, _ := unstructured.NestedString(stored.Object, "spec", "schedule"); found {
		t.Fatalf("legacy spec.schedule survived; want pruned")
	}
}
