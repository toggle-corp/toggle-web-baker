package controller

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/robfig/cron/v3"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
	"github.com/toggle-corp/toggle-web-baker/internal/domain"
)

// PlatformImages are the platform-locked image refs the operator stamps onto
// the pods it creates. They are NOT user-supplied and not subject to the
// registry allowlist (the allowlist only covers setup/fetch/build images).
//
// The defaults below are digest-pinned (the fallback for non-helm runs). NOTE:
// the Helm chart currently supplies these via the -image-* flags as TAG refs
// (repository:appVersion), not digests — see docs/operator-security-invariants.md.
type PlatformImages struct {
	Clone   string // git clone initContainer
	Copier  string // main container that publishes the bundle to the output PVC
	Du      string // measurement Jobs (du over a mounted PVC)
	Cleanup string // (reserved) cache cleanup helper
	Clock   string // CronJob clock that patches the rebuild annotation
	Shim    string // phase wrapper that records per-phase peak memory
	Nginx   string // serving Deployment
}

// OperatorConfig holds all operator-level (process-wide) configuration that the
// reconciler consults. Most of these arrive as cmd/main.go flags.
type OperatorConfig struct {
	// RegistryAllowlist is passed straight to domain.CheckImagesAllowed.
	RegistryAllowlist []string

	// ClusterCIDRs are the pod+service CIDRs to EXCLUDE from build-pod egress.
	// MANDATORY: an empty value is a hard config error (Ready=False), because a
	// build pod with unrestricted egress could reach in-cluster services.
	ClusterCIDRs []string

	// TraefikGroup is the API group for the Traefik Middleware CRD (default
	// "traefik.io"). Configurable so older clusters using "traefik.containo.us"
	// still work.
	TraefikGroup string

	// ImagePullSecret, when set, is stamped onto every platform pod.
	ImagePullSecret string

	// MeasureInterval is the debounce floor between storage (du) measurements.
	// Measurement runs after a successful build, but at most once per interval so
	// rapid back-to-back rebuilds don't spawn redundant du Jobs. Defaults to 1h.
	MeasureInterval time.Duration

	// DefaultSchedule is the clock CronJob's cron expression when an app enables
	// scheduledBuilds without a schedule. Chart-owned; defaults to every 12h.
	DefaultSchedule string

	// DefaultWatchInterval is the commit watcher's poll interval when an app
	// enables watchCommits without an interval. Kept as the raw config string
	// (validated at startup via domain.WatchCron) so the reconciler feeds the
	// SAME value through the same parser — a time.Duration round-trip would
	// re-render "10m" as "10m0s" and quietly depend on two grammars agreeing.
	// Defaults to "10m".
	DefaultWatchInterval string

	Images PlatformImages

	// NodeImages maps a node MAJOR (decimal string key) to its managed image +
	// UID + optional HOME. It is chart-owned (values.yaml), arriving in the
	// operator config file. An app's spec.pipeline.nodeVersion is resolved against this
	// map; a version absent here fails the app at reconcile (ReasonUnknownNodeVersion).
	NodeImages map[string]domain.NodeImage

	// DefaultSetupCommands maps a package-manager name (yarn/pnpm) to the default
	// setup (install) argv the operator injects when an app omits its setup phase
	// command. It is chart-owned (values.yaml) and, after LoadConfig, ALWAYS
	// carries both known package managers: a file value overrides its key, a
	// missing key falls back to the compiled-in default (see
	// defaultSetupCommandFallbacks). Consumers should read it via
	// DefaultSetupCommand so the merge is never re-implemented.
	DefaultSetupCommands map[string][]string

	// PhaseResourceDefaults are the operator-owned resource defaults applied to
	// every phase container (setup/fetch/build). CPU request/limit are global
	// (same for all phases); the memory ceiling is per-phase, used when an app's
	// spec.pipeline.phases.<phase>.memoryLimit is unset (or malformed — see phaseResourceRequirements).
	PhaseResourceDefaults PhaseResourceDefaults

	// ActiveDeadlineSeconds is the operator default deadline bounding the whole
	// build Job when spec.pipeline.timeout is unset.
	ActiveDeadlineSeconds int64

	// GitAuth is the operator-global git credential (clone + watchCommits). It is
	// off unless BOTH SecretName and Hosts are set — see GitAuth.Enabled().
	GitAuth GitAuth
}

// GitAuth is the operator-global git credential feature (design Q4/Q5). When
// enabled, the operator forwards the credential in SecretName ONLY to repos
// whose host is on the Hosts allowlist — an unconditional injection would leak
// the token to attacker-controlled hosts via the git askpass helper
// (credential-forwarding; see domain.RepoHostAllowed).
type GitAuth struct {
	// SecretName is the Secret in the OPERATOR's own namespace holding keys
	// username/password. The operator copies it (owned, labeled) into each App's
	// namespace where the short-lived build/watch pods mount it.
	SecretName string
	// Hosts is the exact-match host allowlist (e.g. ["github.com"]). A repo whose
	// host is absent here receives no credential (anonymous, fail-closed).
	Hosts []string
}

// Enabled reports whether the operator-global git credential is configured.
// SecretName is the sentinel: LoadConfig has already rejected a half-configured
// block (one field set, the other empty), so a non-empty SecretName here implies
// a non-empty Hosts allowlist too.
func (g GitAuth) Enabled() bool {
	return g.SecretName != ""
}

// PhaseResourceDefaults holds the parsed, startup-validated operator resource
// defaults for phase containers. Quantities are pre-parsed so the reconciler
// (and the malformed-user-input fallback) can rely on them always being valid.
type PhaseResourceDefaults struct {
	// CPURequest / CPULimit are global (identical for every phase container).
	CPURequest resource.Quantity
	CPULimit   resource.Quantity
	// Memory ceilings per phase (request is pinned == limit at apply time).
	MemorySetup resource.Quantity
	MemoryFetch resource.Quantity
	MemoryBuild resource.Quantity
}

// MemoryForPhase returns the per-phase default memory ceiling for a phase name
// ("setup"/"fetch"/"build"); it defaults to the build ceiling for any other name.
func (d PhaseResourceDefaults) MemoryForPhase(phase string) resource.Quantity {
	switch phase {
	case "setup":
		return d.MemorySetup
	case "fetch":
		return d.MemoryFetch
	default:
		return d.MemoryBuild
	}
}

// ManagerOptions are the process-wide controller-runtime manager settings that
// used to arrive as individual CLI flags. cmd/main.go maps these onto
// ctrl.Options and the reconciler's StorageClassName / TraefikNamespace.
type ManagerOptions struct {
	MetricsBindAddress     string
	HealthProbeBindAddress string
	LeaderElect            bool
	StorageClass           string
	TraefikNamespace       string
}

// FileConfig is the on-disk operator config schema (the single mounted YAML that
// replaces the ~17 CLI flags). It is decoded via sigs.k8s.io/yaml (YAML→JSON),
// so the json tags below drive the mapping. LoadConfig splits it into the
// reconciler-facing OperatorConfig + the manager-level ManagerOptions.
type FileConfig struct {
	// Manager (controller-runtime) options. LeaderElect is a pointer so an
	// omitted key defaults to true (matching the deleted --leader-elect flag)
	// rather than the bool zero value false, which would silently disable HA.
	MetricsBindAddress     string `json:"metricsBindAddress,omitempty"`
	HealthProbeBindAddress string `json:"healthProbeBindAddress,omitempty"`
	LeaderElect            *bool  `json:"leaderElect,omitempty"`

	// Domain fields.
	RegistryAllowlist []string `json:"registryAllowlist,omitempty"`
	ClusterCIDRs      []string `json:"clusterCIDRs,omitempty"`
	TraefikGroup      string   `json:"traefikGroup,omitempty"`
	TraefikNamespace  string   `json:"traefikNamespace,omitempty"`
	ImagePullSecret   string   `json:"imagePullSecret,omitempty"`
	StorageClass      string   `json:"storageClass,omitempty"`
	MeasureInterval   string   `json:"measureInterval,omitempty"`
	// Trigger defaults for apps that enable a trigger without tuning it.
	DefaultSchedule      string                      `json:"defaultSchedule,omitempty"`
	DefaultWatchInterval string                      `json:"defaultWatchInterval,omitempty"`
	Images               fileImages                  `json:"images,omitempty"`
	NodeImages           map[string]domain.NodeImage `json:"nodeImages,omitempty"`
	DefaultSetupCommands map[string][]string         `json:"defaultSetupCommands,omitempty"`

	// New knobs (were never flags): per-phase resource defaults + job deadline.
	PhaseResources        filePhaseResources `json:"phaseResources,omitempty"`
	ActiveDeadlineSeconds int64              `json:"activeDeadlineSeconds,omitempty"`

	// GitAuth is the optional operator-global git credential block. Absent =
	// feature off (anonymous git). See GitAuth / LoadConfig fail-closed checks.
	GitAuth fileGitAuth `json:"gitAuth,omitempty"`
}

type fileGitAuth struct {
	SecretName string   `json:"secretName,omitempty"`
	Hosts      []string `json:"hosts,omitempty"`
}

type fileImages struct {
	Clone   string `json:"clone,omitempty"`
	Copier  string `json:"copier,omitempty"`
	Du      string `json:"du,omitempty"`
	Cleanup string `json:"cleanup,omitempty"`
	Clock   string `json:"clock,omitempty"`
	Shim    string `json:"shim,omitempty"`
	Nginx   string `json:"nginx,omitempty"`
}

type filePhaseResources struct {
	CPU    filePhaseCPU    `json:"cpu,omitempty"`
	Memory filePhaseMemory `json:"memory,omitempty"`
}

type filePhaseCPU struct {
	Request string `json:"request,omitempty"`
	Limit   string `json:"limit,omitempty"`
}

type filePhaseMemory struct {
	Setup string `json:"setup,omitempty"`
	Fetch string `json:"fetch,omitempty"`
	Build string `json:"build,omitempty"`
}

// LoadConfig reads and validates the operator config file, returning the
// reconciler-facing OperatorConfig plus the manager-level options. It fails
// closed: an invalid file (empty clusterCIDRs, unparseable quantity, non-positive
// deadline, bad measureInterval) is a hard error the caller surfaces before the
// manager starts.
func LoadConfig(path string) (OperatorConfig, ManagerOptions, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return OperatorConfig{}, ManagerOptions{}, fmt.Errorf("read operator config %q: %w", path, err)
	}
	var fc FileConfig
	if err := yaml.Unmarshal(raw, &fc); err != nil {
		return OperatorConfig{}, ManagerOptions{}, fmt.Errorf("parse operator config %q: %w", path, err)
	}

	// measureInterval: empty defaults to 1h; a non-empty value MUST parse.
	interval := time.Hour
	if fc.MeasureInterval != "" {
		interval, err = time.ParseDuration(fc.MeasureInterval)
		if err != nil {
			return OperatorConfig{}, ManagerOptions{}, fmt.Errorf("measureInterval %q is not a valid duration: %w", fc.MeasureInterval, err)
		}
	}

	// Trigger defaults fail loud at startup: a broken chart value would otherwise
	// surface per-app at CronJob creation time.
	defaultSchedule := defaultStr(fc.DefaultSchedule, "0 */12 * * *")
	if _, err := cron.ParseStandard(defaultSchedule); err != nil {
		return OperatorConfig{}, ManagerOptions{}, fmt.Errorf("defaultSchedule %q is not a valid cron expression: %w", fc.DefaultSchedule, err)
	}
	defaultWatch := defaultStr(fc.DefaultWatchInterval, "10m")
	if _, err := domain.WatchCron(defaultWatch); err != nil {
		return OperatorConfig{}, ManagerOptions{}, fmt.Errorf("defaultWatchInterval: %w", err)
	}

	// Every resource quantity MUST parse (they are load-bearing: the per-phase
	// memory defaults are the fallback for malformed user input, so they can
	// never be allowed to be absent/invalid).
	prd := PhaseResourceDefaults{}
	for _, q := range []struct {
		name string
		raw  string
		dst  *resource.Quantity
	}{
		{"phaseResources.cpu.request", fc.PhaseResources.CPU.Request, &prd.CPURequest},
		{"phaseResources.cpu.limit", fc.PhaseResources.CPU.Limit, &prd.CPULimit},
		{"phaseResources.memory.setup", fc.PhaseResources.Memory.Setup, &prd.MemorySetup},
		{"phaseResources.memory.fetch", fc.PhaseResources.Memory.Fetch, &prd.MemoryFetch},
		{"phaseResources.memory.build", fc.PhaseResources.Memory.Build, &prd.MemoryBuild},
	} {
		parsed, perr := resource.ParseQuantity(q.raw)
		if perr != nil {
			return OperatorConfig{}, ManagerOptions{}, fmt.Errorf("%s %q is not a valid resource quantity: %w", q.name, q.raw, perr)
		}
		*q.dst = parsed
	}

	if fc.ActiveDeadlineSeconds <= 0 {
		return OperatorConfig{}, ManagerOptions{}, fmt.Errorf("activeDeadlineSeconds must be > 0, got %d", fc.ActiveDeadlineSeconds)
	}

	// gitAuth is fail-closed like clusterCIDRs: a half-configured block (one field
	// set, the other empty) is a hard error rather than a silently-degraded
	// feature — a secretName with no host allowlist would leave the operator
	// unable to decide which repos may receive the credential.
	switch {
	case fc.GitAuth.SecretName != "" && len(fc.GitAuth.Hosts) == 0:
		return OperatorConfig{}, ManagerOptions{}, fmt.Errorf("gitAuth.secretName is set but gitAuth.hosts is empty (host allowlist is mandatory to avoid credential forwarding)")
	case fc.GitAuth.SecretName == "" && len(fc.GitAuth.Hosts) > 0:
		return OperatorConfig{}, ManagerOptions{}, fmt.Errorf("gitAuth.hosts is set but gitAuth.secretName is empty (no credential to forward)")
	}

	// defaultSetupCommands: reject an unknown package manager or an explicitly
	// broken (empty) command at startup, before the merge fills in fallbacks.
	if err := validateDefaultSetupCommands(fc.DefaultSetupCommands); err != nil {
		return OperatorConfig{}, ManagerOptions{}, err
	}

	cfg := OperatorConfig{
		RegistryAllowlist:    fc.RegistryAllowlist,
		ClusterCIDRs:         fc.ClusterCIDRs,
		TraefikGroup:         fc.TraefikGroup,
		ImagePullSecret:      fc.ImagePullSecret,
		MeasureInterval:      interval,
		DefaultSchedule:      defaultSchedule,
		DefaultWatchInterval: defaultWatch,
		Images: PlatformImages{
			Clone:   fc.Images.Clone,
			Copier:  fc.Images.Copier,
			Du:      fc.Images.Du,
			Cleanup: fc.Images.Cleanup,
			Clock:   fc.Images.Clock,
			Shim:    fc.Images.Shim,
			Nginx:   fc.Images.Nginx,
		},
		NodeImages:            fc.NodeImages,
		DefaultSetupCommands:  mergeSetupCommands(fc.DefaultSetupCommands),
		PhaseResourceDefaults: prd,
		ActiveDeadlineSeconds: fc.ActiveDeadlineSeconds,
		GitAuth: GitAuth{
			SecretName: fc.GitAuth.SecretName,
			Hosts:      fc.GitAuth.Hosts,
		},
	}
	cfg.Defaults()
	// Validate LAST so Defaults() has filled TraefikGroup etc. This enforces the
	// mandatory clusterCIDRs invariant (fail-closed) among other checks.
	if err := cfg.Validate(); err != nil {
		return OperatorConfig{}, ManagerOptions{}, err
	}
	// Semantic node-image checks (numeric key, non-empty image, non-root UID),
	// run directly on the decoded map.
	if err := validateNodeImages(cfg.NodeImages); err != nil {
		return OperatorConfig{}, ManagerOptions{}, err
	}

	// Restore the deleted CLI-flag defaults so an omitted key can't silently
	// disable the metrics/health servers (the Deployment's probes hit :8081) or
	// leader election. clusterCIDRs stays mandatory (no safe default); these do.
	mgr := ManagerOptions{
		MetricsBindAddress:     defaultStr(fc.MetricsBindAddress, ":8080"),
		HealthProbeBindAddress: defaultStr(fc.HealthProbeBindAddress, ":8081"),
		LeaderElect:            fc.LeaderElect == nil || *fc.LeaderElect,
		StorageClass:           fc.StorageClass,
		TraefikNamespace:       defaultStr(fc.TraefikNamespace, "traefik"),
	}
	return cfg, mgr, nil
}

// defaultStr returns v, or def when v is empty.
func defaultStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// ParseNodeImages decodes a node-image map from a JSON string and validates it.
// An empty string yields no entries (the feature is simply unused); malformed
// JSON is a hard error so a bad value fails loudly at operator startup.
func ParseNodeImages(s string) (map[string]domain.NodeImage, error) {
	if s == "" {
		return nil, nil
	}
	var m map[string]domain.NodeImage
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, fmt.Errorf("parse node-images JSON: %w", err)
	}
	if err := validateNodeImages(m); err != nil {
		return nil, err
	}
	return m, nil
}

// validateNodeImages fails loud at startup on a semantically broken entry rather
// than emitting a build pod that can't schedule (empty image) or fails
// runAsNonRoot admission (missing / root UID). These images are operator-run, so
// a bad entry is an admin error to surface immediately, not per-app at build time.
func validateNodeImages(m map[string]domain.NodeImage) error {
	for major, ni := range m {
		if _, err := strconv.Atoi(major); err != nil {
			return fmt.Errorf("node-images: key %q is not a numeric node major", major)
		}
		if ni.Image == "" {
			return fmt.Errorf("node-images: entry %q has an empty image", major)
		}
		if ni.RunAsUser == nil {
			return fmt.Errorf("node-images: entry %q must set runAsUser (the image's numeric non-root UID)", major)
		}
		if *ni.RunAsUser < 1 {
			return fmt.Errorf("node-images: entry %q runAsUser must be >= 1 (non-root), got %d", major, *ni.RunAsUser)
		}
	}
	return nil
}

// defaultSetupCommandFallbacks are the compiled-in per-package-manager setup
// (install) commands. They are the fallback for non-helm runs and for any
// package-manager key the operator config file omits; the Helm chart supplies
// the same values via values.yaml. The keys here are the ONLY package managers
// the operator recognises — an unknown key in the config file is rejected.
// Keyed off the API enum constants so a new PackageManager value fails the
// enum-coverage test in config_test.go instead of silently injecting a noop
// setup command (nil from DefaultSetupCommand → commandOrNoop's ["true"]).
var defaultSetupCommandFallbacks = map[string][]string{
	string(bakerv1alpha1.PackageManagerYarn): {"yarn", "install", "--frozen-lockfile"},
	string(bakerv1alpha1.PackageManagerPnpm): {"pnpm", "install", "--frozen-lockfile"},
}

// mergeSetupCommands returns the effective per-package-manager setup commands:
// every known package manager is present, a file entry overriding its key and a
// missing key falling back to the compiled-in default. The returned slices are
// fresh copies so callers can't mutate the fallback tables. It assumes the file
// map has already passed validateDefaultSetupCommands.
func mergeSetupCommands(file map[string][]string) map[string][]string {
	out := make(map[string][]string, len(defaultSetupCommandFallbacks))
	for pm, fallback := range defaultSetupCommandFallbacks {
		src := fallback
		if v, ok := file[pm]; ok {
			src = v
		}
		out[pm] = append([]string(nil), src...)
	}
	return out
}

// validateDefaultSetupCommands fails loud at startup on an admin error rather
// than injecting a broken command deep in the build pipeline: an unknown
// package-manager key, or an explicitly-set entry that is empty or whose argv[0]
// is empty (a command that could never install anything).
func validateDefaultSetupCommands(m map[string][]string) error {
	for pm, argv := range m {
		if _, ok := defaultSetupCommandFallbacks[pm]; !ok {
			return fmt.Errorf("defaultSetupCommands: key %q is not a known package manager (want one of yarn/pnpm)", pm)
		}
		if len(argv) == 0 {
			return fmt.Errorf("defaultSetupCommands: entry %q has an empty command", pm)
		}
		if argv[0] == "" {
			return fmt.Errorf("defaultSetupCommands: entry %q has an empty argv[0]", pm)
		}
	}
	return nil
}

// DefaultSetupCommand returns the effective default setup (install) command for
// the given package manager. After LoadConfig the effective map always carries
// the known package managers, so this returns a non-nil argv for yarn/pnpm; an
// unknown package manager yields nil.
func (c OperatorConfig) DefaultSetupCommand(pm string) []string {
	return c.DefaultSetupCommands[pm]
}

// MetadataIP is the link-local cloud metadata endpoint, always denied to build
// pods regardless of ClusterCIDRs (baked default).
const MetadataIP = "169.254.169.254/32"

// Validate enforces the mandatory operator config. ClusterCIDRs has NO default
// by design: refusing to run without it is safer than guessing.
func (c *OperatorConfig) Validate() error {
	if len(c.ClusterCIDRs) == 0 {
		return fmt.Errorf("cluster pod/service CIDRs are mandatory (build-pod egress cannot be locked down without them)")
	}
	for _, cidr := range c.ClusterCIDRs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("cluster CIDR %q is not valid CIDR notation: %w", cidr, err)
		}
	}
	// MetadataIP is a baked default; validate it too so a bad edit fails loudly.
	if _, _, err := net.ParseCIDR(MetadataIP); err != nil {
		return fmt.Errorf("metadata CIDR %q is not valid CIDR notation: %w", MetadataIP, err)
	}
	if c.TraefikGroup == "" {
		return fmt.Errorf("traefik group must not be empty")
	}
	return nil
}

// Defaults fills in non-mandatory defaults.
func (c *OperatorConfig) Defaults() {
	if c.TraefikGroup == "" {
		c.TraefikGroup = "traefik.io"
	}
	if c.MeasureInterval <= 0 {
		c.MeasureInterval = time.Hour
	}
	if c.DefaultSchedule == "" {
		c.DefaultSchedule = "0 */12 * * *"
	}
	if c.DefaultWatchInterval == "" {
		c.DefaultWatchInterval = "10m"
	}
	if c.Images.Clone == "" {
		c.Images.Clone = "ghcr.io/toggle-corp/toggle-web-baker-clone@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	}
	if c.Images.Copier == "" {
		c.Images.Copier = "ghcr.io/toggle-corp/toggle-web-baker-copier@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	}
	if c.Images.Du == "" {
		c.Images.Du = "ghcr.io/toggle-corp/toggle-web-baker-du@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	}
	if c.Images.Cleanup == "" {
		c.Images.Cleanup = "ghcr.io/toggle-corp/toggle-web-baker-cleanup@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	}
	if c.Images.Clock == "" {
		c.Images.Clock = "ghcr.io/toggle-corp/toggle-web-baker-clock@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	}
	if c.Images.Shim == "" {
		c.Images.Shim = "ghcr.io/toggle-corp/toggle-web-baker-shim@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	}
	if c.Images.Nginx == "" {
		// nginx-unprivileged listens on 8080 and runs as UID/GID 101, so the pod's
		// runAsNonRoot securityContext is satisfied without runAsUser=0 admission
		// failures (the stock docker.io/library/nginx image starts as root).
		c.Images.Nginx = "docker.io/nginxinc/nginx-unprivileged:1.27-alpine"
	}
}
