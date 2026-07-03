package domain

import "strconv"

// NodeImage is one operator-managed node toolchain entry, keyed by node MAJOR
// version in the operator's map. It is OPERATOR config (chart values), not part
// of the App spec: whoever sets it is a cluster admin, so these images
// are allowlist-exempt exactly like the other platform images.
type NodeImage struct {
	// Image is the managed node image for this major (a full ref; digest-pinned
	// or tag-pinned per the chart's platform-image convention).
	Image string `json:"image"`
	// RunAsUser is the image's numeric non-root UID. The operator pins it so the
	// build pod's runAsNonRoot constraint is satisfied without the app author
	// ever writing runAsUser.
	RunAsUser *int64 `json:"runAsUser,omitempty"`
	// Home optionally overrides the writable HOME injected for phases using this
	// image. Empty means DefaultNodeHome.
	Home string `json:"home,omitempty"`
}

// DefaultNodeHome is the writable HOME injected for managed node phases when the
// mapping entry does not override it. It points at the per-run work emptyDir,
// which is writable under the build pod's readOnlyRootFilesystem.
const DefaultNodeHome = "/work"

// ResolvedPhase is the effective container config for one pipeline phase after
// applying the nodeVersion mapping and any per-phase BYO override.
type ResolvedPhase struct {
	Image     string
	RunAsUser *int64
	// Home is the HOME env value to inject, or "" to inject none (BYO override or
	// clone fallback — the app owns its own env there).
	Home string
}

// LookupNodeImage resolves a node MAJOR to its managed entry. nodeVersion 0
// (unset) never resolves. The map is keyed by the major as a decimal string.
func LookupNodeImage(nodeImages map[string]NodeImage, nodeVersion int) (NodeImage, bool) {
	if nodeVersion == 0 {
		return NodeImage{}, false
	}
	ni, ok := nodeImages[strconv.Itoa(nodeVersion)]
	return ni, ok
}

// ResolvePhase computes the effective image/UID/HOME for one phase. The rule:
//
//   - explicit per-phase image  -> BYO: that image + the app's runAsUser, no
//     HOME injection (an arbitrary image's UID/HOME are the app's concern).
//   - else mapped nodeVersion   -> managed image + its UID + writable HOME.
//   - else                      -> cloneFallback (setup/fetch no-ops), no HOME.
//
// cloneFallback is the platform clone image. Callers pass it for every phase;
// the build phase is guaranteed an image upstream (CEL + the unknown-version
// reconcile gate), so it never falls through to the clone image in practice.
func ResolvePhase(userImage string, userRunAsUser *int64, nodeVersion int, nodeImages map[string]NodeImage, cloneFallback string) ResolvedPhase {
	if userImage != "" {
		return ResolvedPhase{Image: userImage, RunAsUser: userRunAsUser}
	}
	if ni, ok := LookupNodeImage(nodeImages, nodeVersion); ok {
		home := ni.Home
		if home == "" {
			home = DefaultNodeHome
		}
		return ResolvedPhase{Image: ni.Image, RunAsUser: ni.RunAsUser, Home: home}
	}
	return ResolvedPhase{Image: cloneFallback}
}
