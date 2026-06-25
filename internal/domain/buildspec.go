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
}

// BuildSpec is the build-relevant subset of a FrontendApp spec. Changing any
// field here changes the artifact, so a difference from the last deployed hash
// marks the app stale. Fields NOT included here (storage thresholds, ingress
// host, keepReleases) deliberately cannot affect staleness.
type BuildSpec struct {
	Repo           string            `json:"repo"`
	Ref            string            `json:"ref"`
	PackageManager string            `json:"packageManager"`
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
// not affect the result.
func (b BuildSpec) Hash() string {
	data, _ := json.Marshal(b)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
