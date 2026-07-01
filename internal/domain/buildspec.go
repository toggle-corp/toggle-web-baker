package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// PhaseSpec is the build-relevant configuration of a single pipeline phase.
type PhaseSpec struct {
	Image   string   `json:"image"`
	Command []string `json:"command"`
	// RunAsUser changes the container's runtime UID, so a change to it changes
	// the build environment and must mark the app stale.
	RunAsUser *int64 `json:"runAsUser,omitempty"`
}

// BuildSpec is the build-relevant subset of a FrontendApp spec. Changing any
// field here changes the artifact, so a difference from the last deployed hash
// marks the app stale. Fields NOT included here (storage thresholds, ingress
// host, keepReleases) deliberately cannot affect staleness.
type BuildSpec struct {
	Repo           string            `json:"repo"`
	Ref            string            `json:"ref"`
	PackageManager string            `json:"packageManager"`
	// NodeVersion is the user-selected major (0 when unset). It is the SPEC field,
	// not the operator-resolved image digest: a patch bump (same major, new
	// digest) must NOT change the hash — it rolls out on the next scheduled build,
	// not as an immediate SpecChange. Only a major change (or a manual image
	// override) alters the hash.
	NodeVersion int `json:"nodeVersion"`
	Setup          PhaseSpec         `json:"setup"`
	Fetch          PhaseSpec         `json:"fetch"`
	Build          PhaseSpec         `json:"build"`
	BuildArgs      map[string]string `json:"buildArgs"`
	SecretRefs     []string          `json:"secretRefs"`
}

// IsStale reports whether the current build-relevant spec differs from the
// last successfully deployed one (lastBuiltSpecHash, recorded on copier
// success). An empty lastBuiltHash means the app has never been deployed, so
// it is stale (AwaitingFirstBuild). Staleness is surfaced in status only; it
// never triggers a build and is excluded from the ArgoCD health verdict.
func IsStale(current BuildSpec, lastBuiltHash string) bool {
	return current.Hash() != lastBuiltHash
}

// Hash returns a deterministic content hash of the build-relevant spec.
// encoding/json marshals map keys in sorted order, so buildArgs ordering does
// not affect the result. Empty collections are normalized to nil first so that
// a CR omitting a field and the API server materializing it as an empty
// map/slice hash identically (otherwise staleness would flip-flop forever).
func (b BuildSpec) Hash() string {
	data, _ := json.Marshal(b.normalized())
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (b BuildSpec) normalized() BuildSpec {
	if len(b.BuildArgs) == 0 {
		b.BuildArgs = nil
	}
	if len(b.SecretRefs) == 0 {
		b.SecretRefs = nil
	}
	b.Setup = b.Setup.normalized()
	b.Fetch = b.Fetch.normalized()
	b.Build = b.Build.normalized()
	return b
}

func (p PhaseSpec) normalized() PhaseSpec {
	if len(p.Command) == 0 {
		p.Command = nil
	}
	return p
}
