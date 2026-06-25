package domain

import (
	"fmt"
	"strings"
)

// PhaseImage is a user-overridable phase image (setup/fetch/build) to be
// validated against the registry allowlist. The clone and copier images are
// platform-locked and digest-pinned, so they are not user-supplied and not
// checked here.
type PhaseImage struct {
	Phase string
	Image string
}

// CheckImagesAllowed rejects any image that does not begin with one of the
// operator-configured allowlist prefixes. Allowlist entries should be written
// in the same fully-qualified form as the images they permit.
func CheckImagesAllowed(allowlist []string, images []PhaseImage) error {
	for _, pi := range images {
		if !imageAllowed(allowlist, pi.Image) {
			return fmt.Errorf("%s image %q is not on the registry allowlist", pi.Phase, pi.Image)
		}
	}
	return nil
}

func imageAllowed(allowlist []string, image string) bool {
	for _, prefix := range allowlist {
		if strings.HasPrefix(image, prefix) {
			return true
		}
	}
	return false
}
