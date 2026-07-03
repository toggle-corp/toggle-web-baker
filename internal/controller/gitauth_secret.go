package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// gitAuthUsernameKey / gitAuthPasswordKey are the well-known keys the operator
// expects on the git-credential Secret. They match the mount convention the
// clone/clock images document (GIT_CREDENTIAL_DIR/{username,password}).
const (
	gitAuthUsernameKey = "username"
	gitAuthPasswordKey = "password"
)

// ValidateGitAuthSecret is the design-Q5 startup hard-fail check. When gitAuth
// is enabled it Gets the source Secret in the operator's own namespace and
// errors if it is missing or lacks a non-empty username/password — fail-closed,
// so the operator refuses to start rather than silently degrade to anonymous git
// (which would defeat the throttle-avoidance the credential exists for). When
// gitAuth is disabled it is a no-op returning nil.
//
// Error messages name the Secret ONLY, never its data: a startup log line must
// not leak the credential.
func ValidateGitAuthSecret(ctx context.Context, c client.Reader, namespace string, ga GitAuth) error {
	if !ga.Enabled() {
		return nil
	}
	if namespace == "" {
		return fmt.Errorf("gitAuth is enabled but the operator namespace is empty (POD_NAMESPACE must be set via the downward API)")
	}
	var secret corev1.Secret
	key := types.NamespacedName{Namespace: namespace, Name: ga.SecretName}
	if err := c.Get(ctx, key, &secret); err != nil {
		return fmt.Errorf("gitAuth secret %q in namespace %q: %w", ga.SecretName, namespace, err)
	}
	// Shared data check (F2): same non-empty username+password rule as the
	// override validation and the sync path. The wrapped error is value-free.
	if err := checkGitCredentialData(secret.Data); err != nil {
		return fmt.Errorf("gitAuth secret %q in namespace %q is %w", ga.SecretName, namespace, err)
	}
	return nil
}
