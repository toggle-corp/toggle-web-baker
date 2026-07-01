package controller

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"time"

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
	// UID + optional HOME. It is chart-owned (values.yaml), arriving as the single
	// JSON -node-images flag. An app's spec.nodeVersion is resolved against this
	// map; a version absent here fails the app at reconcile (ReasonUnknownNodeVersion).
	NodeImages map[string]domain.NodeImage
}

// ParseNodeImages decodes the -node-images JSON flag into the node-image map.
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
