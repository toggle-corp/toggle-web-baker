package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	ctrlreconcile "sigs.k8s.io/controller-runtime/pkg/reconcile"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
	"github.com/toggle-corp/toggle-web-baker/internal/domain"
)

// gitCredMountPath is the read-only directory the clone/watch git-askpass
// helpers read {username,password} from (GIT_CREDENTIAL_DIR). Fixed by the
// image convention (see images/clone/README.md, images/clock/git-askpass.sh).
const gitCredMountPath = "/run/git-credential"

// volGitCred is the pod volume name for the git-credential Secret mount.
const volGitCred = "git-credential"

// gitCredentialDecision is the per-App effective-credential outcome (design
// Q3/Q4/Q6). It is a pure function of the App spec + operator GitAuth config, so
// the mount wiring and the sync/cleanup steps all consult the SAME decision and
// cannot diverge.
type gitCredentialDecision struct {
	// mount is true when a credential must be mounted into the clone/watch pods.
	mount bool
	// syncCopy is true when the operator must ensure a synced COPY of the global
	// source Secret in the app namespace (global path only). Never true for a
	// per-app override — that Secret is mounted directly.
	syncCopy bool
	// secretName is the Secret in the App's namespace to mount (the user's own
	// Secret for an override, or the synced-copy name for the global path). Empty
	// when mount is false.
	secretName string
}

// decideGitCredential resolves the effective git credential for one App:
//   - spec.repoAuth set  → mount the USER's Secret directly (no copy, no host
//     allowlist — the user's own credential for their own repo, design Q4/Q6);
//   - else global enabled AND repo host allowlisted → ensure+mount a synced COPY
//     of the operator-global Secret (design Q3);
//   - else → anonymous (no mount, no copy). Any previously-synced copy is cleaned
//     up by the reconciler based on syncCopy==false.
func decideGitCredential(app *bakerv1alpha1.App, cfg GitAuth) gitCredentialDecision {
	if app.Spec.RepoAuth != nil {
		return gitCredentialDecision{mount: true, syncCopy: false, secretName: app.Spec.RepoAuth.SecretRef.Name}
	}
	if cfg.Enabled() && domain.RepoHostAllowed(app.Spec.Repo, cfg.Hosts) {
		return gitCredentialDecision{mount: true, syncCopy: true, secretName: gitCredentialSecretName(app)}
	}
	return gitCredentialDecision{}
}

// gitCredentialVolume is the read-only Secret volume for the credential mount,
// projecting ONLY the username/password keys (never the whole Secret, so an
// over-broad user Secret can't leak extra keys into the pod).
func gitCredentialVolume(secretName string) corev1.Volume {
	return corev1.Volume{
		Name: volGitCred,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: secretName,
				Items: []corev1.KeyToPath{
					{Key: gitAuthUsernameKey, Path: gitAuthUsernameKey},
					{Key: gitAuthPasswordKey, Path: gitAuthPasswordKey},
				},
			},
		},
	}
}

// gitCredentialMount is the read-only /run/git-credential mount and
// gitCredentialEnv the matching GIT_CREDENTIAL_DIR env the clone initContainer
// and the watch CronJob pod add so their git-askpass helper finds the credential.
func gitCredentialMount() corev1.VolumeMount {
	return corev1.VolumeMount{Name: volGitCred, MountPath: gitCredMountPath, ReadOnly: true}
}

func gitCredentialEnv() corev1.EnvVar {
	return corev1.EnvVar{Name: "GIT_CREDENTIAL_DIR", Value: gitCredMountPath}
}

// syncedGitCredential builds the per-app synced COPY of the operator-global
// credential Secret: a labeled, owned child (GC'd with the app, drift-corrected
// through the same upsert machinery as other children). Its data is copied
// verbatim from the source Secret in the operator namespace (username/password
// only). NEVER logged — the caller passes the source data straight through.
func (r *AppReconciler) syncedGitCredential(app *bakerv1alpha1.App, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      gitCredentialSecretName(app),
			Namespace: app.Namespace,
			Labels:    labelsFor(app),
		},
		Type: corev1.SecretTypeBasicAuth,
		Data: map[string][]byte{
			gitAuthUsernameKey: data[gitAuthUsernameKey],
			gitAuthPasswordKey: data[gitAuthPasswordKey],
		},
	}
}

// validateRepoAuthSecret checks the per-app override Secret exists in the app
// namespace with non-empty username AND password (design Q6/Q9-2). It returns an
// error whose message names the Secret ONLY — never its values — suitable for a
// Degraded condition. No-op (nil) when spec.repoAuth is absent.
func (r *AppReconciler) validateRepoAuthSecret(ctx context.Context, app *bakerv1alpha1.App) error {
	if app.Spec.RepoAuth == nil {
		return nil
	}
	name := app.Spec.RepoAuth.SecretRef.Name
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: app.Namespace, Name: name}, &secret); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return fmt.Errorf("repoAuth secret %q not found in namespace %q", name, app.Namespace)
		}
		return err
	}
	for _, k := range []string{gitAuthUsernameKey, gitAuthPasswordKey} {
		if len(secret.Data[k]) == 0 {
			return fmt.Errorf("repoAuth secret %q is missing a non-empty %q key", name, k)
		}
	}
	return nil
}

// reconcileGitCredential converges the per-app git-credential state per the
// effective-credential decision and returns that decision for the mount wiring.
// Global-sync path: ensure a drift-corrected owned copy of the source Secret;
// otherwise remove any previously-synced copy (owned-child cleanup, mirroring
// the disabled-trigger CronJob sweep). Per-app override: no copy is touched (the
// user's Secret is mounted directly). Source-Secret-deleted-at-runtime is
// fail-static (design Q9-3): keep the existing copy, do not degrade, log + Event.
func (r *AppReconciler) reconcileGitCredential(ctx context.Context, app *bakerv1alpha1.App) (gitCredentialDecision, error) {
	d := decideGitCredential(app, r.Config.GitAuth)
	if !d.syncCopy {
		// Not on the global-sync path (override or anonymous): reclaim a stale copy.
		gone := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: gitCredentialSecretName(app), Namespace: app.Namespace}}
		if err := r.deleteOwnedChild(ctx, app, gone); err != nil {
			return d, err
		}
		return d, nil
	}

	data, present, err := r.getSourceGitCredential(ctx)
	if err != nil {
		return d, err
	}
	if !present {
		// Fail-static (design Q9-3): the source Secret was deleted at runtime. Do
		// NOT delete or blank the existing copy and do NOT degrade the app — the
		// short-lived pods keep using the last-synced credential. Log loudly + emit
		// an Event so an operator notices; the next operator restart hard-fails via
		// the startup check. A missing copy simply means anonymous until the source
		// returns (no copy to preserve).
		log.FromContext(ctx).Error(nil, "gitAuth source Secret is absent at runtime; keeping existing per-app copies (fail-static). Restore it and restart the operator.",
			"sourceSecret", r.Config.GitAuth.SecretName, "operatorNamespace", r.OperatorNamespace)
		if r.Recorder != nil {
			r.Recorder.Eventf(app, corev1.EventTypeWarning, "GitAuthSourceMissing",
				"operator-global git credential Secret %q is absent; using last-synced copy (fail-static)", r.Config.GitAuth.SecretName)
		}
		// Report the decision honestly: mount only if a copy actually exists.
		if _, ok := r.existingSyncedCopy(ctx, app); !ok {
			return gitCredentialDecision{}, nil
		}
		return d, nil
	}

	secret := r.syncedGitCredential(app, data)
	if err := r.upsert(ctx, app, secret, func() {}); err != nil {
		return d, err
	}
	return d, nil
}

// existingSyncedCopy reports whether a synced-copy Secret currently exists in the
// app namespace (used by the fail-static path to decide whether to still mount).
func (r *AppReconciler) existingSyncedCopy(ctx context.Context, app *bakerv1alpha1.App) (*corev1.Secret, bool) {
	var s corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: gitCredentialSecretName(app), Namespace: app.Namespace}, &s); err != nil {
		return nil, false
	}
	return &s, true
}

// mapSecretToApps is the rotation informer's map function (design Q7). It turns
// a Secret change into the set of Apps to re-reconcile:
//   - the GLOBAL source Secret (operator namespace + configured name) → ALL Apps.
//     We enqueue all rather than filtering to the global-credential users because
//     the per-app effective-credential decision is cheap and re-derived in
//     Reconcile anyway; listing+filtering here would duplicate that logic and
//     could drift from it. The over-enqueue is bounded by the App count and each
//     spurious reconcile is a fast no-op (SSA apply of an unchanged copy).
//   - a Secret in an app namespace that an App's spec.repoAuth references, OR a
//     synced-copy child (name == <app>-git-credential in the app's own namespace)
//     → the owning/referencing App(s). This drives drift-correction of a copy and
//     Degraded-recovery when a broken override Secret is fixed.
//
// A Secret matching nothing enqueues nothing.
func (r *AppReconciler) mapSecretToApps(ctx context.Context, obj client.Object) []ctrlreconcile.Request {
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return nil
	}

	apps := &bakerv1alpha1.AppList{}
	if err := r.List(ctx, apps); err != nil {
		log.FromContext(ctx).Error(err, "gitCred informer: failed to list Apps for secret mapping", "secret", secret.Name)
		return nil
	}

	// Global source Secret changed → enqueue every App.
	if r.Config.GitAuth.Enabled() && secret.Namespace == r.OperatorNamespace && secret.Name == r.Config.GitAuth.SecretName {
		reqs := make([]ctrlreconcile.Request, 0, len(apps.Items))
		for i := range apps.Items {
			a := &apps.Items[i]
			reqs = append(reqs, ctrlreconcile.Request{NamespacedName: types.NamespacedName{Namespace: a.Namespace, Name: a.Name}})
		}
		return reqs
	}

	// A Secret in an app namespace: enqueue any App that references it via
	// repoAuth or that owns a synced copy of that name.
	var reqs []ctrlreconcile.Request
	for i := range apps.Items {
		a := &apps.Items[i]
		if a.Namespace != secret.Namespace {
			continue
		}
		if a.Spec.RepoAuth != nil && a.Spec.RepoAuth.SecretRef.Name == secret.Name {
			reqs = append(reqs, ctrlreconcile.Request{NamespacedName: types.NamespacedName{Namespace: a.Namespace, Name: a.Name}})
			continue
		}
		if secret.Name == gitCredentialSecretName(a) {
			reqs = append(reqs, ctrlreconcile.Request{NamespacedName: types.NamespacedName{Namespace: a.Namespace, Name: a.Name}})
		}
	}
	return reqs
}

// getSourceGitCredential reads the operator-global source Secret from the
// operator's own namespace. Returns (data, true, nil) on success; (_, false,
// nil) when the source is absent (design Q9-3 fail-static: the caller keeps any
// existing copy and does NOT degrade); a non-NotFound error propagates.
func (r *AppReconciler) getSourceGitCredential(ctx context.Context) (map[string][]byte, bool, error) {
	var src corev1.Secret
	key := types.NamespacedName{Namespace: r.OperatorNamespace, Name: r.Config.GitAuth.SecretName}
	if err := r.Get(ctx, key, &src); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return nil, false, nil
		}
		return nil, false, err
	}
	return src.Data, true, nil
}
