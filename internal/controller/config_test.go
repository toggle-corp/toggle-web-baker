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

// When defaultSetupCommands is absent from the file, the effective map still
// carries BOTH package managers via the compiled-in fallbacks.
func TestLoadConfig_DefaultSetupCommandsFallbackWhenOmitted(t *testing.T) {
	cfg, _, err := LoadConfig(writeConfig(t, validConfigYAML))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	yarn := cfg.DefaultSetupCommand("yarn")
	if len(yarn) != 3 || yarn[0] != "yarn" || yarn[1] != "install" || yarn[2] != "--frozen-lockfile" {
		t.Fatalf("yarn fallback wrong: %v", yarn)
	}
	pnpm := cfg.DefaultSetupCommand("pnpm")
	if len(pnpm) != 3 || pnpm[0] != "pnpm" || pnpm[1] != "install" || pnpm[2] != "--frozen-lockfile" {
		t.Fatalf("pnpm fallback wrong: %v", pnpm)
	}
	// Effective map ALWAYS has both keys after load.
	if _, ok := cfg.DefaultSetupCommands["yarn"]; !ok {
		t.Fatal("effective map missing yarn key")
	}
	if _, ok := cfg.DefaultSetupCommands["pnpm"]; !ok {
		t.Fatal("effective map missing pnpm key")
	}
}

// A file value overrides its package-manager key; the untouched key still falls
// back (per-key merge, not all-or-nothing).
func TestLoadConfig_DefaultSetupCommandsOverridePerKey(t *testing.T) {
	body := validConfigYAML + `
defaultSetupCommands:
  yarn: ["yarn", "install", "--immutable"]
`
	cfg, _, err := LoadConfig(writeConfig(t, body))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	yarn := cfg.DefaultSetupCommand("yarn")
	if len(yarn) != 3 || yarn[2] != "--immutable" {
		t.Fatalf("yarn override not honored: %v", yarn)
	}
	// pnpm untouched -> fallback.
	pnpm := cfg.DefaultSetupCommand("pnpm")
	if len(pnpm) != 3 || pnpm[2] != "--frozen-lockfile" {
		t.Fatalf("pnpm should fall back when only yarn overridden: %v", pnpm)
	}
}

// An unknown package-manager key is an admin error surfaced at startup.
func TestLoadConfig_DefaultSetupCommandsUnknownKeyError(t *testing.T) {
	body := validConfigYAML + `
defaultSetupCommands:
  npm: ["npm", "ci"]
`
	if _, _, err := LoadConfig(writeConfig(t, body)); err == nil {
		t.Fatal("expected error for unknown package-manager key, got nil")
	}
}

// An explicitly-set entry that is empty (or whose argv[0] is empty) is a hard
// startup error — an empty command can never install anything.
func TestLoadConfig_DefaultSetupCommandsEmptyArgvError(t *testing.T) {
	cases := map[string]string{
		"empty argv": `
defaultSetupCommands:
  yarn: []
`,
		"empty argv[0]": `
defaultSetupCommands:
  yarn: ["", "install"]
`,
	}
	for name, extra := range cases {
		if _, _, err := LoadConfig(writeConfig(t, validConfigYAML+extra)); err == nil {
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
defaultSchedule: "30 */6 * * *"
defaultWatchInterval: 15m
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
	if cfg.DefaultSchedule != "30 */6 * * *" {
		t.Fatalf("defaultSchedule = %q, want 30 */6 * * *", cfg.DefaultSchedule)
	}
	if cfg.DefaultWatchInterval != "15m" {
		t.Fatalf("defaultWatchInterval = %v, want 15m", cfg.DefaultWatchInterval)
	}
}

// Clone-retry knobs fall back to the compiled defaults (3 attempts, 2s base
// backoff) when omitted, and are honored when set.
func TestLoadConfig_CloneRetryDefaultsWhenOmitted(t *testing.T) {
	body := `
clusterCIDRs: [10.0.0.0/8]
phaseResources:
  cpu: { request: "0.1", limit: "4" }
  memory: { setup: 512Mi, fetch: 512Mi, build: 2Gi }
activeDeadlineSeconds: 1800
`
	cfg, _, err := LoadConfig(writeConfig(t, body))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.CloneRetries != 3 {
		t.Fatalf("cloneRetries = %d, want 3", cfg.CloneRetries)
	}
	if cfg.CloneRetryBaseDelay != 2 {
		t.Fatalf("cloneRetryBaseDelay = %d, want 2", cfg.CloneRetryBaseDelay)
	}
}

func TestLoadConfig_CloneRetryOverride(t *testing.T) {
	body := `
clusterCIDRs: [10.0.0.0/8]
phaseResources:
  cpu: { request: "0.1", limit: "4" }
  memory: { setup: 512Mi, fetch: 512Mi, build: 2Gi }
activeDeadlineSeconds: 1800
cloneRetries: 5
cloneRetryBaseDelay: 4
`
	cfg, _, err := LoadConfig(writeConfig(t, body))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.CloneRetries != 5 {
		t.Fatalf("cloneRetries = %d, want 5", cfg.CloneRetries)
	}
	if cfg.CloneRetryBaseDelay != 4 {
		t.Fatalf("cloneRetryBaseDelay = %d, want 4", cfg.CloneRetryBaseDelay)
	}
}

// History-retention defaults fall back to keepRecent 5 / keepFailed 10 when
// omitted, and are honored when set.
func TestLoadConfig_HistoryDefaultsWhenOmitted(t *testing.T) {
	body := `
clusterCIDRs: [10.0.0.0/8]
phaseResources:
  cpu: { request: "0.1", limit: "4" }
  memory: { setup: 512Mi, fetch: 512Mi, build: 2Gi }
activeDeadlineSeconds: 1800
`
	cfg, _, err := LoadConfig(writeConfig(t, body))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.HistoryKeepRecent != 5 {
		t.Fatalf("historyKeepRecent = %d, want 5", cfg.HistoryKeepRecent)
	}
	if cfg.HistoryKeepFailed != 10 {
		t.Fatalf("historyKeepFailed = %d, want 10", cfg.HistoryKeepFailed)
	}
}

func TestLoadConfig_HistoryOverride(t *testing.T) {
	body := `
clusterCIDRs: [10.0.0.0/8]
phaseResources:
  cpu: { request: "0.1", limit: "4" }
  memory: { setup: 512Mi, fetch: 512Mi, build: 2Gi }
activeDeadlineSeconds: 1800
historyKeepRecent: 8
historyKeepFailed: 20
`
	cfg, _, err := LoadConfig(writeConfig(t, body))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.HistoryKeepRecent != 8 || cfg.HistoryKeepFailed != 20 {
		t.Fatalf("history = %d/%d, want 8/20", cfg.HistoryKeepRecent, cfg.HistoryKeepFailed)
	}
}

// The scheduled-failure alert threshold default is 3 when omitted, honored when set.
func TestLoadConfig_ScheduledAlertThreshold(t *testing.T) {
	base := `
clusterCIDRs: [10.0.0.0/8]
phaseResources:
  cpu: { request: "0.1", limit: "4" }
  memory: { setup: 512Mi, fetch: 512Mi, build: 2Gi }
activeDeadlineSeconds: 1800
`
	cfg, _, err := LoadConfig(writeConfig(t, base))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.ScheduledAlertThreshold != 3 {
		t.Fatalf("scheduledAlertThreshold = %d, want 3 (default)", cfg.ScheduledAlertThreshold)
	}
	cfg, _, err = LoadConfig(writeConfig(t, base+"scheduledAlertThreshold: 6\n"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.ScheduledAlertThreshold != 6 {
		t.Fatalf("scheduledAlertThreshold = %d, want 6", cfg.ScheduledAlertThreshold)
	}
}

// Omitted trigger defaults fall back to the documented values: every 12 hours
// for scheduled builds, 10m for the commit-watch poll.
func TestLoadConfig_TriggerDefaultsWhenOmitted(t *testing.T) {
	body := `
clusterCIDRs: [10.0.0.0/8]
phaseResources:
  cpu: { request: "0.1", limit: "4" }
  memory: { setup: 512Mi, fetch: 512Mi, build: 2Gi }
activeDeadlineSeconds: 1800
`
	cfg, _, err := LoadConfig(writeConfig(t, body))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.DefaultSchedule != "0 */12 * * *" {
		t.Fatalf("defaultSchedule = %q, want 0 */12 * * *", cfg.DefaultSchedule)
	}
	if cfg.DefaultWatchInterval != "10m" {
		t.Fatalf("defaultWatchInterval = %v, want 10m", cfg.DefaultWatchInterval)
	}
}

// A defaultWatchInterval the watcher CronJob cannot express must fail at
// startup, not at reconcile time.
func TestLoadConfig_BadTriggerDefaultsError(t *testing.T) {
	base := `
clusterCIDRs: [10.0.0.0/8]
phaseResources:
  cpu: { request: "0.1", limit: "4" }
  memory: { setup: 512Mi, fetch: 512Mi, build: 2Gi }
activeDeadlineSeconds: 1800
`
	for _, extra := range []string{
		"defaultWatchInterval: 30s",
		"defaultWatchInterval: bogus",
		"defaultSchedule: not-a-cron",
	} {
		if _, _, err := LoadConfig(writeConfig(t, base+extra+"\n")); err == nil {
			t.Fatalf("LoadConfig with %q: expected error, got nil", extra)
		}
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

// gitAuth is optional; an absent block leaves the feature off (Enabled() false),
// which the operator treats as today's anonymous git behavior.
func TestLoadConfig_GitAuthAbsentIsDisabled(t *testing.T) {
	cfg, _, err := LoadConfig(writeConfig(t, validConfigYAML))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.GitAuth.Enabled() {
		t.Fatalf("gitAuth must be disabled when absent, got %+v", cfg.GitAuth)
	}
}

// A well-formed gitAuth block populates the SecretName + Hosts and reads as enabled.
func TestLoadConfig_GitAuthPopulated(t *testing.T) {
	body := validConfigYAML + `
gitAuth:
  secretName: baker-git-credential
  hosts: ["github.com", "gitlab.com"]
`
	cfg, _, err := LoadConfig(writeConfig(t, body))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.GitAuth.Enabled() {
		t.Fatal("gitAuth must be enabled when secretName+hosts set")
	}
	if cfg.GitAuth.SecretName != "baker-git-credential" {
		t.Fatalf("secretName = %q", cfg.GitAuth.SecretName)
	}
	if len(cfg.GitAuth.Hosts) != 2 || cfg.GitAuth.Hosts[0] != "github.com" || cfg.GitAuth.Hosts[1] != "gitlab.com" {
		t.Fatalf("hosts = %v", cfg.GitAuth.Hosts)
	}
}

// Fail-closed like clusterCIDRs: a half-configured gitAuth (one field set, the
// other empty) is a hard startup error rather than a silently-degraded feature.
func TestLoadConfig_GitAuthHalfConfiguredError(t *testing.T) {
	cases := map[string]string{
		"secretName without hosts": `
gitAuth:
  secretName: baker-git-credential
`,
		"hosts without secretName": `
gitAuth:
  hosts: ["github.com"]
`,
		"secretName with empty hosts list": `
gitAuth:
  secretName: baker-git-credential
  hosts: []
`,
	}
	for name, extra := range cases {
		if _, _, err := LoadConfig(writeConfig(t, validConfigYAML+extra)); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

// Every packageManager value the CRD admits must have a compiled-in default
// setup command: DefaultSetupCommand returning nil degrades to a silent noop
// setup container (commandOrNoop's ["true"]), so enum growth without a matching
// fallback entry must fail HERE, not in a user's build.
func TestDefaultSetupCommands_CoverCRDPackageManagerEnum(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "crd", "baker.toggle-corp.com_apps.yaml"))
	if err != nil {
		t.Fatalf("read CRD: %v", err)
	}
	lines := strings.Split(string(raw), "\n")
	var enum []string
	for i, l := range lines {
		if strings.TrimSpace(l) != "packageManager:" {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			s := strings.TrimSpace(lines[j])
			switch {
			case s == "enum:":
				continue
			case strings.HasPrefix(s, "- "):
				enum = append(enum, strings.TrimPrefix(s, "- "))
			case strings.HasPrefix(s, "type:"):
				j = len(lines)
			}
			if strings.HasPrefix(s, "type:") {
				break
			}
		}
		break
	}
	if len(enum) == 0 {
		t.Fatal("could not locate the packageManager enum in the generated CRD")
	}
	for _, pm := range enum {
		if len(defaultSetupCommandFallbacks[pm]) == 0 {
			t.Errorf("CRD enum value %q has no compiled-in default setup command", pm)
		}
	}
}
