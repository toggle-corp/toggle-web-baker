package controller

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Platform helper images default to the flat ghcr.io/toggle-corp/toggle-web-baker-<name>
// repo scheme (matching images/Makefile), still digest-pinned. These defaults are a
// fallback for non-helm runs; the Helm chart supplies real refs via the -image-* flags.
func TestConfigDefaults_HelperImageRepos(t *testing.T) {
	c := validConfig()
	c.Defaults()
	cases := map[string]string{
		"clone":   c.Images.Clone,
		"copier":  c.Images.Copier,
		"du":      c.Images.Du,
		"cleanup": c.Images.Cleanup,
	}
	for name, ref := range cases {
		wantRepo := "ghcr.io/toggle-corp/toggle-web-baker-" + name
		if !strings.HasPrefix(ref, wantRepo+"@sha256:") {
			t.Errorf("Images.%s = %q, want prefix %q@sha256:", name, ref, wantRepo)
		}
	}
}

// The node-image map arrives as a single JSON flag (Helm-templated from
// values.yaml). Empty is valid (feature simply unused); malformed JSON is a
// hard error so a bad chart value fails loudly at startup.
func TestParseNodeImages_ParsesJSONMap(t *testing.T) {
	m, err := ParseNodeImages(`{"18":{"image":"ghcr.io/x-node18@sha256:aaa","runAsUser":1000},"24":{"image":"ghcr.io/x-node24@sha256:bbb","runAsUser":1000,"home":"/home/node"}}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m["18"].Image != "ghcr.io/x-node18@sha256:aaa" || m["18"].RunAsUser == nil || *m["18"].RunAsUser != 1000 {
		t.Fatalf("node18 entry parsed wrong: %+v", m["18"])
	}
	if m["24"].Home != "/home/node" {
		t.Fatalf("node24 home parsed wrong: %+v", m["24"])
	}
}

func TestParseNodeImages_EmptyIsNilNoError(t *testing.T) {
	m, err := ParseNodeImages("")
	if err != nil {
		t.Fatalf("empty must not error, got %v", err)
	}
	if len(m) != 0 {
		t.Fatalf("empty must yield no entries, got %+v", m)
	}
}

func TestParseNodeImages_MalformedErrors(t *testing.T) {
	if _, err := ParseNodeImages(`{"18": not-json}`); err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

// A semantically broken entry must fail at parse (startup) time, not deep in the
// build pipeline. Empty image / missing UID / root UID / non-numeric key.
func TestParseNodeImages_RejectsBrokenEntries(t *testing.T) {
	cases := map[string]string{
		"empty image":     `{"18":{"runAsUser":1000}}`,
		"missing UID":     `{"18":{"image":"repo@sha256:aaa"}}`,
		"root UID":        `{"18":{"image":"repo@sha256:aaa","runAsUser":0}}`,
		"non-numeric key": `{"lts":{"image":"repo@sha256:aaa","runAsUser":1000}}`,
	}
	for name, in := range cases {
		if _, err := ParseNodeImages(in); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func validConfig() OperatorConfig {
	c := OperatorConfig{
		ClusterCIDRs: []string{"10.0.0.0/8", "172.20.0.0/16"},
		TraefikGroup: "traefik.io",
	}
	return c
}

// Fix 4: a malformed cluster CIDR must be rejected by Validate.
func TestConfigValidate_RejectsMalformedClusterCIDR(t *testing.T) {
	c := validConfig()
	c.ClusterCIDRs = []string{"10.0.0.0/8", "not-a-cidr"}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error for malformed CIDR, got nil")
	}
	if !strings.Contains(err.Error(), "not-a-cidr") {
		t.Fatalf("expected error to name the bad CIDR, got %v", err)
	}
}

// Fix 4: a CIDR without a mask (host IP) must also be rejected (ParseCIDR fails).
func TestConfigValidate_RejectsBareIP(t *testing.T) {
	c := validConfig()
	c.ClusterCIDRs = []string{"10.0.0.1"}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for bare IP without mask, got nil")
	}
}

// Fix 4: valid CIDRs pass.
func TestConfigValidate_AcceptsValidCIDRs(t *testing.T) {
	c := validConfig()
	if err := c.Validate(); err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}
}

// Fix 4: empty cluster CIDRs still rejected (refuse-if-unset preserved).
func TestConfigValidate_RejectsEmptyClusterCIDRs(t *testing.T) {
	c := validConfig()
	c.ClusterCIDRs = nil
	if err := c.Validate(); err == nil {
		t.Fatal("expected error when cluster CIDRs unset, got nil")
	}
}

// validConfigYAML is a complete, well-formed operator config file (the new
// single mounted YAML replacing the ~17 CLI flags).
const validConfigYAML = `
metricsBindAddress: ":8080"
healthProbeBindAddress: ":8081"
leaderElect: true
storageClass: fast
traefikNamespace: traefik
traefikGroup: traefik.io
imagePullSecret: regcred
registryAllowlist:
  - docker.io/library
clusterCIDRs:
  - 10.0.0.0/16
  - 10.96.0.0/12
measureInterval: 2h
images:
  clone: ghcr.io/x/clone@sha256:aaa
  copier: ghcr.io/x/copier@sha256:bbb
  du: ghcr.io/x/du@sha256:ccc
  cleanup: ghcr.io/x/cleanup@sha256:ddd
  clock: ghcr.io/x/clock@sha256:eee
  nginx: ghcr.io/x/nginx:1.27
nodeImages:
  "18":
    image: ghcr.io/x/node18@sha256:fff
    runAsUser: 1000
phaseResources:
  cpu:
    request: "0.1"
    limit: "4"
  memory:
    setup: 512Mi
    fetch: 512Mi
    build: 2Gi
activeDeadlineSeconds: 1800
`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return p
}

func TestLoadConfig_ValidPopulatesOperatorConfig(t *testing.T) {
	cfg, mgr, err := LoadConfig(writeConfig(t, validConfigYAML))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if mgr.MetricsBindAddress != ":8080" || mgr.HealthProbeBindAddress != ":8081" || !mgr.LeaderElect {
		t.Fatalf("manager opts not populated: %+v", mgr)
	}
	if mgr.StorageClass != "fast" || mgr.TraefikNamespace != "traefik" {
		t.Fatalf("storageClass/traefikNamespace not populated: %+v", mgr)
	}
	if len(cfg.ClusterCIDRs) != 2 || cfg.TraefikGroup != "traefik.io" || cfg.ImagePullSecret != "regcred" {
		t.Fatalf("domain fields not populated: %+v", cfg)
	}
	if cfg.MeasureInterval.String() != "2h0m0s" {
		t.Fatalf("measureInterval = %v, want 2h", cfg.MeasureInterval)
	}
	if cfg.Images.Clone != "ghcr.io/x/clone@sha256:aaa" || cfg.Images.Nginx != "ghcr.io/x/nginx:1.27" {
		t.Fatalf("images not populated: %+v", cfg.Images)
	}
	ni, ok := cfg.NodeImages["18"]
	if !ok || ni.Image != "ghcr.io/x/node18@sha256:fff" || ni.RunAsUser == nil || *ni.RunAsUser != 1000 {
		t.Fatalf("nodeImages not populated: %+v", cfg.NodeImages)
	}
	if cfg.PhaseResourceDefaults.CPURequest.String() != "100m" || cfg.PhaseResourceDefaults.CPULimit.String() != "4" {
		t.Fatalf("cpu defaults wrong: %+v", cfg.PhaseResourceDefaults)
	}
	if cfg.PhaseResourceDefaults.MemorySetup.String() != "512Mi" || cfg.PhaseResourceDefaults.MemoryBuild.String() != "2Gi" {
		t.Fatalf("memory defaults wrong: %+v", cfg.PhaseResourceDefaults)
	}
	if cfg.ActiveDeadlineSeconds != 1800 {
		t.Fatalf("activeDeadlineSeconds = %d, want 1800", cfg.ActiveDeadlineSeconds)
	}
}

// Omitting the manager knobs must NOT silently disable the metrics/health
// servers or leader election — they fall back to the deleted flag defaults.
func TestLoadConfig_ManagerDefaultsWhenOmitted(t *testing.T) {
	body := `
clusterCIDRs: [10.0.0.0/8]
phaseResources:
  cpu: { request: "0.1", limit: "4" }
  memory: { setup: 512Mi, fetch: 512Mi, build: 2Gi }
activeDeadlineSeconds: 1800
`
	_, mgr, err := LoadConfig(writeConfig(t, body))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if mgr.MetricsBindAddress != ":8080" || mgr.HealthProbeBindAddress != ":8081" {
		t.Fatalf("bind addresses not defaulted: %+v", mgr)
	}
	if !mgr.LeaderElect {
		t.Fatal("leaderElect must default to true when omitted")
	}
	if mgr.TraefikNamespace != "traefik" {
		t.Fatalf("traefikNamespace not defaulted: %q", mgr.TraefikNamespace)
	}
}

// An explicit leaderElect: false must be honored (not overridden by the default).
func TestLoadConfig_LeaderElectFalseHonored(t *testing.T) {
	body := strings.Replace(validConfigYAML, "leaderElect: true", "leaderElect: false", 1)
	_, mgr, err := LoadConfig(writeConfig(t, body))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if mgr.LeaderElect {
		t.Fatal("explicit leaderElect: false must be honored")
	}
}

func TestLoadConfig_EmptyClusterCIDRsError(t *testing.T) {
	body := strings.Replace(validConfigYAML,
		"clusterCIDRs:\n  - 10.0.0.0/16\n  - 10.96.0.0/12", "clusterCIDRs: []", 1)
	if _, _, err := LoadConfig(writeConfig(t, body)); err == nil {
		t.Fatal("expected error for empty clusterCIDRs")
	}
}

func TestLoadConfig_MalformedQuantityError(t *testing.T) {
	body := strings.Replace(validConfigYAML, "build: 2Gi", "build: not-a-quantity", 1)
	if _, _, err := LoadConfig(writeConfig(t, body)); err == nil {
		t.Fatal("expected error for malformed resource quantity")
	}
}

func TestLoadConfig_NonPositiveDeadlineError(t *testing.T) {
	body := strings.Replace(validConfigYAML, "activeDeadlineSeconds: 1800", "activeDeadlineSeconds: 0", 1)
	if _, _, err := LoadConfig(writeConfig(t, body)); err == nil {
		t.Fatal("expected error for activeDeadlineSeconds <= 0")
	}
}
