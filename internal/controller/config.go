package controller

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"

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

	Images PlatformImages

	// NodeImages maps a node MAJOR (decimal string key) to its managed image +
	// UID + optional HOME. It is chart-owned (values.yaml), arriving in the
	// operator config file. An app's spec.nodeVersion is resolved against this
	// map; a version absent here fails the app at reconcile (ReasonUnknownNodeVersion).
	NodeImages map[string]domain.NodeImage

	// PhaseResourceDefaults are the operator-owned resource defaults applied to
	// every phase container (setup/fetch/build). CPU request/limit are global
	// (same for all phases); the memory ceiling is per-phase, used when an app's
	// spec.<phase>.memoryLimit is unset (or malformed — see phaseResourceRequirements).
	PhaseResourceDefaults PhaseResourceDefaults

	// ActiveDeadlineSeconds is the operator default deadline bounding the whole
	// build Job when spec.activeDeadlineSeconds is unset.
	ActiveDeadlineSeconds int64
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
	// Manager (controller-runtime) options.
	MetricsBindAddress     string `json:"metricsBindAddress,omitempty"`
	HealthProbeBindAddress string `json:"healthProbeBindAddress,omitempty"`
	LeaderElect            bool   `json:"leaderElect,omitempty"`

	// Domain fields.
	RegistryAllowlist []string                    `json:"registryAllowlist,omitempty"`
	ClusterCIDRs      []string                    `json:"clusterCIDRs,omitempty"`
	TraefikGroup      string                      `json:"traefikGroup,omitempty"`
	TraefikNamespace  string                      `json:"traefikNamespace,omitempty"`
	ImagePullSecret   string                      `json:"imagePullSecret,omitempty"`
	StorageClass      string                      `json:"storageClass,omitempty"`
	MeasureInterval   string                      `json:"measureInterval,omitempty"`
	Images            fileImages                  `json:"images,omitempty"`
	NodeImages        map[string]domain.NodeImage `json:"nodeImages,omitempty"`

	// New knobs (were never flags): per-phase resource defaults + job deadline.
	PhaseResources        filePhaseResources `json:"phaseResources,omitempty"`
	ActiveDeadlineSeconds int64              `json:"activeDeadlineSeconds,omitempty"`
}

type fileImages struct {
	Clone   string `json:"clone,omitempty"`
	Copier  string `json:"copier,omitempty"`
	Du      string `json:"du,omitempty"`
	Cleanup string `json:"cleanup,omitempty"`
	Clock   string `json:"clock,omitempty"`
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

	cfg := OperatorConfig{
		RegistryAllowlist: fc.RegistryAllowlist,
		ClusterCIDRs:      fc.ClusterCIDRs,
		TraefikGroup:      fc.TraefikGroup,
		ImagePullSecret:   fc.ImagePullSecret,
		MeasureInterval:   interval,
		Images: PlatformImages{
			Clone:   fc.Images.Clone,
			Copier:  fc.Images.Copier,
			Du:      fc.Images.Du,
			Cleanup: fc.Images.Cleanup,
			Clock:   fc.Images.Clock,
			Nginx:   fc.Images.Nginx,
		},
		NodeImages:            fc.NodeImages,
		PhaseResourceDefaults: prd,
		ActiveDeadlineSeconds: fc.ActiveDeadlineSeconds,
	}
	cfg.Defaults()
	// Validate LAST so Defaults() has filled TraefikGroup etc. This enforces the
	// mandatory clusterCIDRs invariant (fail-closed) among other checks.
	if err := cfg.Validate(); err != nil {
		return OperatorConfig{}, ManagerOptions{}, err
	}
	// Reuse the same semantic checks the JSON flag path applied to node images.
	if _, err := ParseNodeImages(nodeImagesToJSON(cfg.NodeImages)); err != nil {
		return OperatorConfig{}, ManagerOptions{}, err
	}

	mgr := ManagerOptions{
		MetricsBindAddress:     fc.MetricsBindAddress,
		HealthProbeBindAddress: fc.HealthProbeBindAddress,
		LeaderElect:            fc.LeaderElect,
		StorageClass:           fc.StorageClass,
		TraefikNamespace:       fc.TraefikNamespace,
	}
	return cfg, mgr, nil
}

// nodeImagesToJSON re-marshals the unmarshaled node-image map so LoadConfig can
// reuse ParseNodeImages' semantic validation (numeric key, non-empty image,
// non-root UID) without duplicating it here.
func nodeImagesToJSON(m map[string]domain.NodeImage) string {
	if len(m) == 0 {
		return ""
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// ParseNodeImages validates a node-image map supplied as a JSON string.
// An empty string yields no entries (the feature is simply unused); malformed
// JSON is a hard error so a bad chart value fails loudly at operator startup.
func ParseNodeImages(s string) (map[string]domain.NodeImage, error) {
	if s == "" {
		return nil, nil
	}
	var m map[string]domain.NodeImage
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, fmt.Errorf("parse -node-images: %w", err)
	}
	// Fail loud at startup on a semantically broken entry rather than emitting a
	// build pod that can't schedule (empty image) or fails runAsNonRoot admission
	// (missing / root UID). These images are operator-run, so a bad entry is an
	// admin error to surface immediately, not per-app at build time.
	for major, ni := range m {
		if _, err := strconv.Atoi(major); err != nil {
			return nil, fmt.Errorf("node-images: key %q is not a numeric node major", major)
		}
		if ni.Image == "" {
			return nil, fmt.Errorf("node-images: entry %q has an empty image", major)
		}
		if ni.RunAsUser == nil {
			return nil, fmt.Errorf("node-images: entry %q must set runAsUser (the image's numeric non-root UID)", major)
		}
		if *ni.RunAsUser < 1 {
			return nil, fmt.Errorf("node-images: entry %q runAsUser must be >= 1 (non-root), got %d", major, *ni.RunAsUser)
		}
	}
	return m, nil
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
	if c.Images.Nginx == "" {
		// nginx-unprivileged listens on 8080 and runs as UID/GID 101, so the pod's
		// runAsNonRoot securityContext is satisfied without runAsUser=0 admission
		// failures (the stock docker.io/library/nginx image starts as root).
		c.Images.Nginx = "docker.io/nginxinc/nginx-unprivileged:1.27-alpine"
	}
}
