package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// gitCredHostEnv is the env var name that scopes the mounted credential to the
// repo's OWN host. The askpass helpers answer a git credential prompt ONLY when
// the prompted host matches this value (lowercase). Contract with the image
// side (parallel work): var name = GIT_CREDENTIAL_HOST, value = lowercase host,
// always set whenever a credential is mounted. This closes the submodule /
// redirect credential-harvest: a .gitmodules pointing at evil.com no longer
// gets the token because evil.com != the scoped host.
const gitCredHostEnv = "GIT_CREDENTIAL_HOST"

// gitCredHashAnnotation stamps a content hash of the synced copy's
// username\0password on the copy so reconcileGitCredential can skip a no-op SSA
// patch when the source is unchanged (see reconcileGitCredential). It is read
// from metadata only — never the data — so it stays cheap under the
// metadata-only Secret cache (see mapSecretToApps / SetupWithManager).
const gitCredHashAnnotation = "baker.toggle-corp.com/git-credential-hash"

// gitCredentialDecision is the per-App effective-credential outcome (design
// Q3/Q4/Q6). It is a pure function of the App spec + operator GitAuth config, so
// the mount wiring and the sync/cleanup steps all consult the SAME decision and
// cannot diverge. F1: Reconcile computes it ONCE (fail-static-adjusted) and
// threads it into BuildJob and the watch CronJob; decideGitCredential has
// exactly one production caller (reconcileGitCredential).
type gitCredentialDecision struct {
	// syncCopy is true when the operator must ensure a synced COPY of the global
	// source Secret in the app namespace (global path only). Never true for a
	// per-app override — that Secret is mounted directly.
	syncCopy bool
	// secretName is the Secret in the App's namespace to mount (the user's own
	// Secret for an override, or the synced-copy name for the global path). Empty
	// when nothing is mounted (anonymous).
	secretName string
}

// mounts reports whether a credential must be mounted into the clone/watch pods.
// It is derivable (true iff a secret name was resolved), so it is a method rather
// than a stored field that could drift from secretName.
func (d gitCredentialDecision) mounts() bool { return d.secretName != "" }

// decideGitCredential resolves the effective git credential for one App:
//   - spec.repoAuth set  → mount the USER's Secret directly (no copy, no host
//     allowlist — the user's own credential for their own repo, design Q4/Q6);
//   - else global enabled AND repo host allowlisted → ensure+mount a synced COPY
//     of the operator-global Secret (design Q3);
//   - else → anonymous (no mount, no copy). Any previously-synced copy is cleaned
//     up by the reconciler based on syncCopy==false.
//
// F1: this has exactly ONE production caller — reconcileGitCredential. The mount
// wiring (BuildJob, watchCronJob) takes the RESULT threaded through Reconcile so
// it can never diverge (e.g. the fail-static adjustment below).
func decideGitCredential(app *bakerv1alpha1.App, cfg GitAuth) gitCredentialDecision {
	if app.Spec.RepoAuth != nil {
		return gitCredentialDecision{syncCopy: false, secretName: app.Spec.RepoAuth.SecretRef.Name}
	}
	if cfg.Enabled() && domain.RepoHostAllowed(app.Spec.Repo, cfg.Hosts) {
		return gitCredentialDecision{syncCopy: true, secretName: gitCredentialSecretName(app)}
	}
	return gitCredentialDecision{}
}

// checkGitCredentialData is the SINGLE validity check for a git-credential
// Secret's data (design Q6/Q9-2): both username AND password must be present and
// non-empty. Shared by the startup check (ValidateGitAuthSecret), the per-app
// override validation (validateRepoAuthSecret), AND the sync path
// (getSourceGitCredential) so a source with missing/empty keys is treated
// identically to a MISSING source (fail-static — never propagate garbage into
// the synced copies). The error names neither the Secret nor its values; callers
// wrap it with the Secret name for a Degraded message but never the values.
func checkGitCredentialData(data map[string][]byte) error {
	for _, k := range []string{gitAuthUsernameKey, gitAuthPasswordKey} {
		if len(data[k]) == 0 {
			return fmt.Errorf("missing a non-empty %q key", k)
		}
	}
	return nil
}

// gitCredentialHash is the content hash stamped on a synced copy (F5): sha256
// over username\0password, hex. NUL-delimited so ("ab","c") and ("a","bc") hash
// differently. Never logged (it is a one-way digest, but the values still feed
// it, so treat it as sensitive-adjacent and keep it out of logs).
func gitCredentialHash(data map[string][]byte) string {
	h := sha256.New()
	h.Write(data[gitAuthUsernameKey])
	h.Write([]byte{0})
	h.Write(data[gitAuthPasswordKey])
	return hex.EncodeToString(h.Sum(nil))
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

// addGitCredential is the ONE shared helper (F7) that wires the effective
// credential into a pod that clones/polls the repo: it adds the read-only Secret
// volume, and on the container selected by name adds the /run/git-credential
// mount plus the GIT_CREDENTIAL_DIR + host-scoping GIT_CREDENTIAL_HOST env.
// Used by both mount sites (BuildJob's clone initContainer and watchCronJob's
// clock container) so the env/mount/volume trio can never drift between them.
//
// host is domain.RepoHost(spec.repo). The caller has already verified d.mounts();
// an EMPTY host means RepoHost errored (an override with an unparseable repo) —
// we then do NOT mount (anonymous, fail-closed): mounting a credential we cannot
// scope to a known host would re-open the very harvest GIT_CREDENTIAL_HOST closes.
func addGitCredential(pod *corev1.PodSpec, containerName, secretName, host string) {
	if host == "" {
		return // fail-closed: no confidently-scoped host ⇒ no credential.
	}
	pod.Volumes = append(pod.Volumes, gitCredentialVolume(secretName))
	env := []corev1.EnvVar{
		{Name: "GIT_CREDENTIAL_DIR", Value: gitCredMountPath},
		{Name: gitCredHostEnv, Value: host},
	}
	mount := corev1.VolumeMount{Name: volGitCred, MountPath: gitCredMountPath, ReadOnly: true}
	for i := range pod.Containers {
		if containerName != "" && pod.Containers[i].Name != containerName {
			continue
		}
		pod.Containers[i].Env = append(pod.Containers[i].Env, env...)
		pod.Containers[i].VolumeMounts = append(pod.Containers[i].VolumeMounts, mount)
	}
	for i := range pod.InitContainers {
		if containerName != "" && pod.InitContainers[i].Name != containerName {
			continue
		}
		pod.InitContainers[i].Env = append(pod.InitContainers[i].Env, env...)
		pod.InitContainers[i].VolumeMounts = append(pod.InitContainers[i].VolumeMounts, mount)
	}
}

// syncedGitCredential builds the per-app synced COPY of the operator-global
// credential Secret: a labeled, owned child (GC'd with the app, drift-corrected
// through the same upsert machinery as other children). Its data is copied
// verbatim from the source Secret in the operator namespace (username/password
// only). NEVER logged — the caller passes the source data straight through. The
// content-hash annotation (F5) lets a later reconcile skip a no-op SSA patch.
func (r *AppReconciler) syncedGitCredential(app *bakerv1alpha1.App, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        gitCredentialSecretName(app),
			Namespace:   app.Namespace,
			Labels:      labelsFor(app),
			Annotations: map[string]string{gitCredHashAnnotation: gitCredentialHash(data)},
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
// Degraded condition. No-op (nil) when spec.repoAuth is absent. Reads via the
// uncached APIReader (F4): the metadata-only Secret cache carries no data.
func (r *AppReconciler) validateRepoAuthSecret(ctx context.Context, app *bakerv1alpha1.App) error {
	if app.Spec.RepoAuth == nil {
		return nil
	}
	name := app.Spec.RepoAuth.SecretRef.Name
	var secret corev1.Secret
	if err := r.dataReader().Get(ctx, types.NamespacedName{Namespace: app.Namespace, Name: name}, &secret); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return fmt.Errorf("repoAuth secret %q not found in namespace %q", name, app.Namespace)
		}
		return err
	}
	if err := checkGitCredentialData(secret.Data); err != nil {
		return fmt.Errorf("repoAuth secret %q is %w", name, err)
	}
	return nil
}

// reconcileGitCredential converges the per-app git-credential state per the
// effective-credential decision and returns that (fail-static-adjusted) decision
// for the mount wiring (F1 — Reconcile threads it into BuildJob + watchCronJob).
// Global-sync path: ensure a drift-corrected owned copy of the source Secret;
// otherwise remove any previously-synced copy (owned-child cleanup). Per-app
// override: no copy is touched (the user's Secret is mounted directly).
// Source-Secret-deleted-at-runtime — OR present-but-invalid — is fail-static
// (design Q9-3): keep the existing copy, do not degrade, log + Event on the
// missing↔present transition only (F6, not per-reconcile spam).
func (r *AppReconciler) reconcileGitCredential(ctx context.Context, app *bakerv1alpha1.App) (gitCredentialDecision, error) {
	d := decideGitCredential(app, r.Config.GitAuth)
	if !d.syncCopy {
		// Not on the global-sync path (override or anonymous): reclaim a stale copy.
		// F4: sweep with ONE metadata existence+ownership check (no data reads, and
		// crucially NOT a typed corev1.Secret cache Get, which would spin up a
		// full-object Secret informer — the leak F4 removes). The sweep still runs
		// even when gitAuth is fully disabled + no override: a stale copy can linger
		// from a previously-enabled config, and this is the only path that reclaims it.
		if err := r.deleteOwnedSyncedCopy(ctx, app); err != nil {
			return d, err
		}
		return d, nil
	}

	data, present, err := r.getSourceGitCredential(ctx)
	if err != nil {
		return d, err
	}
	if !present {
		// Fail-static (design Q9-3): the source Secret was deleted OR turned invalid
		// (missing/empty keys) at runtime — treated identically (F2): do NOT delete
		// or blank the existing copy and do NOT degrade the app. The short-lived pods
		// keep using the last-synced credential. Log loudly + emit an Event ON THE
		// TRANSITION ONLY (F6) so an operator notices without per-reconcile storm;
		// the next operator restart hard-fails via the startup check. A missing copy
		// means anonymous until the source returns.
		// F6: fire the loud Error log + Warning Event ONCE per outage, gated on the
		// missing→present transition, not once per App per reconcile (which stormed
		// the log + Event stream). Design intent (Q9-3: "log loudly + Event") is
		// preserved; the spam is not.
		if r.gitSourceMissing.CompareAndSwap(false, true) {
			log.FromContext(ctx).Error(nil, "gitAuth source Secret is absent or invalid at runtime; keeping existing per-app copies (fail-static). Restore it and restart the operator.",
				"sourceSecret", r.Config.GitAuth.SecretName, "operatorNamespace", r.OperatorNamespace)
			if r.Recorder != nil {
				r.Recorder.Eventf(app, corev1.EventTypeWarning, "GitAuthSourceMissing",
					"operator-global git credential Secret %q is absent or invalid; using last-synced copy (fail-static)", r.Config.GitAuth.SecretName)
			}
		}
		// Report the decision honestly: mount only if a copy actually exists.
		if !r.existingSyncedCopy(ctx, app) {
			return gitCredentialDecision{}, nil
		}
		return d, nil
	}
	// Source is present and valid: clear the outage flag (log recovery once at Info).
	if r.gitSourceMissing.CompareAndSwap(true, false) {
		log.FromContext(ctx).Info("gitAuth source Secret recovered; resuming credential sync",
			"sourceSecret", r.Config.GitAuth.SecretName, "operatorNamespace", r.OperatorNamespace)
	}

	// F5: skip a no-op SSA patch when the existing copy already carries this
	// content hash. The existing copy's annotation is read from METADATA only
	// (via the cache-backed PartialObjectMetadata Get) so this stays cheap and
	// never reads the copy's data. hotloop write QPS matters: SSA-patching every
	// reconcile churns resourceVersion and re-enqueues via the owned-child watch.
	want := gitCredentialHash(data)
	if have, ok := r.existingSyncedCopyHash(ctx, app); ok && have == want {
		return d, nil
	}

	secret := r.syncedGitCredential(app, data)
	if err := r.upsert(ctx, app, secret, func() {}); err != nil {
		return d, err
	}
	return d, nil
}

// deleteOwnedSyncedCopy reclaims a stale synced-copy Secret (F4). It reads
// PartialObjectMetadata against the cache for the existence + controller-owner
// check — NOT a typed corev1.Secret Get, which would spin up a second full-object
// Secret informer (caching every cluster Secret's data — the leak the
// metadata-only watch removes). Only deletes what THIS app controller-owns; a
// same-named foreign Secret is left alone. DO NOT "simplify" this back to the
// shared deleteOwnedChild (that Gets a typed *corev1.Secret).
func (r *AppReconciler) deleteOwnedSyncedCopy(ctx context.Context, app *bakerv1alpha1.App) error {
	var meta metav1.PartialObjectMetadata
	meta.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))
	key := types.NamespacedName{Name: gitCredentialSecretName(app), Namespace: app.Namespace}
	if err := r.Get(ctx, key, &meta); err != nil {
		return client.IgnoreNotFound(err)
	}
	if ref := metav1.GetControllerOf(&meta); ref == nil || ref.UID != app.UID {
		return nil
	}
	gone := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}
	return client.IgnoreNotFound(r.Delete(ctx, gone))
}

// existingSyncedCopy reports whether a synced-copy Secret currently exists in the
// app namespace (used by the fail-static path to decide whether to still mount).
// It needs existence only, so it reads PartialObjectMetadata against the cache:
// with the metadata-only Secret watch registered (SetupWithManager), a typed
// corev1.Secret cache Get would spin up a SECOND, full-object Secret informer
// (caching every cluster Secret's data — the very leak F4 removes). A metadata
// Get reuses the metadata informer. DO NOT "simplify" this to a typed Get.
func (r *AppReconciler) existingSyncedCopy(ctx context.Context, app *bakerv1alpha1.App) bool {
	var meta metav1.PartialObjectMetadata
	meta.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))
	err := r.Get(ctx, types.NamespacedName{Name: gitCredentialSecretName(app), Namespace: app.Namespace}, &meta)
	return err == nil
}

// existingSyncedCopyHash returns the content-hash annotation on the current
// synced copy, if the copy exists. Reads PartialObjectMetadata against the cache
// (same cache-informer reasoning as existingSyncedCopy — a typed Secret Get
// would create a full-object informer). Never touches the copy's data.
func (r *AppReconciler) existingSyncedCopyHash(ctx context.Context, app *bakerv1alpha1.App) (string, bool) {
	var meta metav1.PartialObjectMetadata
	meta.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))
	if err := r.Get(ctx, types.NamespacedName{Name: gitCredentialSecretName(app), Namespace: app.Namespace}, &meta); err != nil {
		return "", false
	}
	return meta.Annotations[gitCredHashAnnotation], true
}

// mapSecretToApps is the rotation informer's map function (design Q7). It turns
// a Secret change into the set of Apps to re-reconcile:
//   - the GLOBAL source Secret (operator namespace + configured name) → ALL Apps.
//     We enqueue all rather than filtering because the per-app effective-credential
//     decision is cheap and re-derived in Reconcile anyway. The over-enqueue is
//     bounded by the App count and each spurious reconcile is a fast no-op (the F5
//     content-hash skip elides even the SSA apply of an unchanged copy).
//   - a Secret in an app namespace that an App's spec.repoAuth references, OR a
//     synced-copy child (name == <app>-git-credential in the app's own namespace)
//     → the owning/referencing App(s). This drives drift-correction of a copy and
//     Degraded-recovery when a broken override Secret is fixed.
//
// F4: the global-source match is TWO string compares checked BEFORE any List, so
// the common helm-release-Secret churn (which matches nothing) costs no List. Only
// an app-namespace Secret triggers a List, scoped to that one namespace.
func (r *AppReconciler) mapSecretToApps(ctx context.Context, secret client.Object) []ctrlreconcile.Request {
	// Under the metadata-only watch (F4) the runtime object is a
	// *metav1.PartialObjectMetadata; we only ever read name/namespace, so any
	// client.Object works (tests pass a *corev1.Secret).

	// Global source Secret changed → enqueue every App. Cheap prefilter first.
	if r.Config.GitAuth.Enabled() && secret.GetNamespace() == r.OperatorNamespace && secret.GetName() == r.Config.GitAuth.SecretName {
		apps := &bakerv1alpha1.AppList{}
		if err := r.List(ctx, apps); err != nil {
			log.FromContext(ctx).Error(err, "gitCred informer: failed to list Apps for source-secret mapping", "secret", secret.GetName())
			return nil
		}
		reqs := make([]ctrlreconcile.Request, 0, len(apps.Items))
		for i := range apps.Items {
			a := &apps.Items[i]
			reqs = append(reqs, ctrlreconcile.Request{NamespacedName: types.NamespacedName{Namespace: a.Namespace, Name: a.Name}})
		}
		return reqs
	}

	// A Secret in an app namespace: List only that namespace, and enqueue any App
	// that references it via repoAuth or that owns a synced copy of that name.
	apps := &bakerv1alpha1.AppList{}
	if err := r.List(ctx, apps, client.InNamespace(secret.GetNamespace())); err != nil {
		log.FromContext(ctx).Error(err, "gitCred informer: failed to list Apps for secret mapping", "secret", secret.GetName())
		return nil
	}
	var reqs []ctrlreconcile.Request
	for i := range apps.Items {
		a := &apps.Items[i]
		matchesOverride := a.Spec.RepoAuth != nil && a.Spec.RepoAuth.SecretRef.Name == secret.GetName()
		if matchesOverride || secret.GetName() == gitCredentialSecretName(a) {
			reqs = append(reqs, ctrlreconcile.Request{NamespacedName: types.NamespacedName{Namespace: a.Namespace, Name: a.Name}})
		}
	}
	return reqs
}

// dataReader is the uncached reader for Secret DATA reads (F4). The Secret watch
// is registered metadata-only, so the manager cache holds no Secret data; a
// cache Get would return empty data (or spin up a full informer). All paths that
// need the credential VALUES (source sync, override validation) route here. Set
// from mgr.GetAPIReader() in cmd/main.go; tests set it to the same fake client.
func (r *AppReconciler) dataReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	// Fallback for code paths/tests that never set APIReader: the cached client.
	return r.Client
}

// getSourceGitCredential reads the operator-global source Secret from the
// operator's own namespace via the uncached APIReader (F4). Returns (data, true,
// nil) when the source is present AND valid; (_, false, nil) when the source is
// absent OR invalid (missing/empty keys — treated identically to missing, F2:
// the caller keeps any existing copy and does NOT degrade, design Q9-3 fail-
// static); a non-NotFound error propagates.
func (r *AppReconciler) getSourceGitCredential(ctx context.Context) (map[string][]byte, bool, error) {
	var src corev1.Secret
	key := types.NamespacedName{Namespace: r.OperatorNamespace, Name: r.Config.GitAuth.SecretName}
	if err := r.dataReader().Get(ctx, key, &src); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return nil, false, nil
		}
		return nil, false, err
	}
	if err := checkGitCredentialData(src.Data); err != nil {
		// Present but invalid: same as missing (fail-static). Never log the error
		// verbatim near the values — checkGitCredentialData's message is value-free.
		return nil, false, nil
	}
	return src.Data, true, nil
}
