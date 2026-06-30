package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
	"github.com/toggle-corp/toggle-web-baker/internal/domain"
)

// Cleanup-Job constants. The role label is DISTINCT from the build role so
// cleanup pods are never mistaken for build pods (buildActive / the build pod
// Watch key off role=build). The mode label disambiguates cache vs releases.
const (
	cleanupRole          = "cleanup"
	cleanupRoleLabel     = "baker.toggle-corp.com/role"
	cleanupModeLabel     = "baker.toggle-corp.com/cleanup-mode"
	cleanupContainerName = "cleanup"

	cleanupModeCache    = "cache"
	cleanupModeReleases = "releases"
)

// cleanupSecurityContext runs the prune as root with ONLY DAC_OVERRIDE +
// FOWNER. The cache/output PVCs are written by the BUILD image's user (e.g.
// cimg/node's uid 3434, dirs mode 0755 group root), so the prune must unlink
// files it does not own. fsGroup can't help: kind's local-path (and any
// hostPath-backed storage) ignores it, and a non-root uid can't hold an added
// capability effectively (no ambient caps in k8s). DAC_OVERRIDE+FOWNER as root
// is storage- and build-uid-agnostic; everything else stays dropped, the pod
// has no service-account token, and it mounts only the one PVC it prunes.
func cleanupSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		RunAsUser:                ptr.To(int64(0)),
		RunAsNonRoot:             ptr.To(false),
		AllowPrivilegeEscalation: ptr.To(false),
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
			Add:  []corev1.Capability{"DAC_OVERRIDE", "FOWNER"},
		},
	}
}

func cleanupJobName(app *bakerv1alpha1.FrontendApp, mode string) string {
	return app.Name + "-cleanup-" + mode
}

// cleanupLabelsFor selects all of an app's cleanup Jobs/pods (role=cleanup).
func cleanupLabelsFor(app *bakerv1alpha1.FrontendApp) map[string]string {
	l := labelsFor(app)
	l[cleanupRoleLabel] = cleanupRole
	return l
}

// CleanupResult is the cleanup image's termination-log JSON contract. The cache
// and releases modes emit overlapping shapes; the optional fields are populated
// per mode (action distinguishes them).
type CleanupResult struct {
	Action    string `json:"action"`
	Reason    string `json:"reason,omitempty"`
	Before    int64  `json:"before"`
	After     int64  `json:"after"`
	Reclaimed int64  `json:"reclaimed"`
	Threshold int64  `json:"threshold,omitempty"`
	Kept      int    `json:"kept,omitempty"`
	Deleted   int    `json:"deleted,omitempty"`
}

// CleanupJob builds the helper Job for ONE cleanup mode, mirroring BuildJob's
// hardening (no SA token, runAsNonRoot, dropped caps) and the du Job's
// termination-message contract (the image writes JSON to /dev/termination-log).
//
// MODE=cache mounts the cache PVC RW at /cache and forces the prune with
// CLEANUP_THRESHOLD_BYTES=0. MODE=releases mounts the output PVC at the output
// root and prunes <OUTPUT_ROOT>/releases, keeping spec.keepReleases plus the
// currently/previously served release dirs (PROTECTED_RELEASES).
func (r *FrontendAppReconciler) CleanupJob(app *bakerv1alpha1.FrontendApp, mode string) *batchv1.Job {
	labels := cleanupLabelsFor(app)
	labels[cleanupModeLabel] = mode

	var (
		env     []corev1.EnvVar
		mounts  []corev1.VolumeMount
		volumes []corev1.Volume
	)
	switch mode {
	case cleanupModeReleases:
		env = []corev1.EnvVar{
			{Name: "MODE", Value: cleanupModeReleases},
			{Name: "RELEASES_DIR", Value: outputMountPath + "/releases"},
			{Name: "KEEP_RELEASES", Value: strconv.Itoa(app.Spec.KeepReleases)},
			{Name: "PROTECTED_RELEASES", Value: protectedReleases(app)},
		}
		mounts = []corev1.VolumeMount{{Name: volOutput, MountPath: outputMountPath}}
		volumes = []corev1.Volume{{Name: volOutput, VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: outputPVCName(app)},
		}}}
	default: // cache
		env = []corev1.EnvVar{
			{Name: "MODE", Value: cleanupModeCache},
			{Name: "PACKAGE_MANAGER", Value: string(app.Spec.PackageManager)},
			// Manual force: prune regardless of the configured threshold.
			{Name: "CLEANUP_THRESHOLD_BYTES", Value: "0"},
		}
		mounts = []corev1.VolumeMount{{Name: volCache, MountPath: cacheMountPath}}
		volumes = []corev1.Volume{{Name: volCache, VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: cacheePVCName(app)},
		}}}
	}

	podSpec := corev1.PodSpec{
		RestartPolicy:                corev1.RestartPolicyNever,
		AutomountServiceAccountToken: ptr.To(false),
		SecurityContext: &corev1.PodSecurityContext{
			// Root is required at the container level (see cleanupSecurityContext);
			// keep the pod-level seccomp profile and let the container pin the rest.
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		Containers: []corev1.Container{{
			Name:                     cleanupContainerName,
			Image:                    r.Config.Images.Cleanup,
			Env:                      env,
			VolumeMounts:             mounts,
			TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
			SecurityContext:          cleanupSecurityContext(),
		}},
		Volumes: volumes,
	}
	if r.Config.ImagePullSecret != "" {
		podSpec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: r.Config.ImagePullSecret}}
	}
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cleanupJobName(app, mode),
			Namespace: app.Namespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:          ptr.To(int32(0)),
			ActiveDeadlineSeconds: ptr.To(int64(600)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       podSpec,
			},
		},
	}
}

// protectedReleases is the comma-separated set of release dir names that must
// never be pruned (the currently + previously served releases). Empties are
// omitted so the cleanup image doesn't see a bogus "" entry / trailing comma.
func protectedReleases(app *bakerv1alpha1.FrontendApp) string {
	var p []string
	if c := app.Status.Release.Current; c != "" {
		p = append(p, c)
	}
	if prev := app.Status.Release.Previous; prev != "" {
		p = append(p, prev)
	}
	return strings.Join(p, ",")
}

// cleanupAction is one (annotation, by-annotation, status-accessor) tuple,
// letting the reconcile loop drive cache + releases through identical logic.
type cleanupAction struct {
	mode        string
	requestedAt string // requested-at annotation value
	requestedBy string // by annotation value
}

func cleanupActionsFor(app *bakerv1alpha1.FrontendApp) []cleanupAction {
	return []cleanupAction{
		{
			mode:        cleanupModeCache,
			requestedAt: app.Annotations[bakerv1alpha1.CleanupCacheRequestedAtAnnotation],
			requestedBy: app.Annotations[bakerv1alpha1.CleanupCacheByAnnotation],
		},
		{
			mode:        cleanupModeReleases,
			requestedAt: app.Annotations[bakerv1alpha1.CleanupReleasesRequestedAtAnnotation],
			requestedBy: app.Annotations[bakerv1alpha1.CleanupReleasesByAnnotation],
		},
	}
}

// actionStatus returns a pointer to the per-mode CleanupActionStatus, creating
// the Cleanup container + the per-mode record on first use.
func actionStatus(app *bakerv1alpha1.FrontendApp, mode string) *bakerv1alpha1.CleanupActionStatus {
	if app.Status.Cleanup == nil {
		app.Status.Cleanup = &bakerv1alpha1.CleanupStatus{}
	}
	switch mode {
	case cleanupModeReleases:
		if app.Status.Cleanup.Releases == nil {
			app.Status.Cleanup.Releases = &bakerv1alpha1.CleanupActionStatus{}
		}
		return app.Status.Cleanup.Releases
	default:
		if app.Status.Cleanup.Cache == nil {
			app.Status.Cleanup.Cache = &bakerv1alpha1.CleanupActionStatus{}
		}
		return app.Status.Cleanup.Cache
	}
}

// cleanupActive reports whether ANY cleanup Job for this app is still running.
func (r *FrontendAppReconciler) cleanupActive(ctx context.Context, app *bakerv1alpha1.FrontendApp) (bool, error) {
	jobs := &batchv1.JobList{}
	if err := r.List(ctx, jobs, client.InNamespace(app.Namespace), client.MatchingLabels(cleanupLabelsFor(app))); err != nil {
		return false, err
	}
	for i := range jobs.Items {
		if jobFinished(&jobs.Items[i]) == nil {
			return true, nil
		}
	}
	return false, nil
}

// reconcileCleanup is the cleanup chokepoint: for each action it first observes
// any finished Job (recording the result + advancing the processed marker), then
// decides whether to start a new Job. Serialization (against builds + each
// other) goes through domain.DecideCleanup; build requests take precedence.
func (r *FrontendAppReconciler) reconcileCleanup(ctx context.Context, app *bakerv1alpha1.FrontendApp, buildActive bool) error {
	// Observe finished cleanup Jobs first, so a just-completed action frees the
	// serialization slot for the other action in this same reconcile.
	if err := r.observeCleanup(ctx, app); err != nil {
		return err
	}
	// startedThisPass guards the second action: a cleanup Job Created for the
	// first action is not yet visible to the cached List in cleanupActive within
	// the same reconcile, so without this flag both actions could start at once
	// and run two prune Jobs concurrently.
	startedThisPass := false
	for _, a := range cleanupActionsFor(app) {
		st := actionStatus(app, a.mode)
		active, err := r.cleanupActive(ctx, app)
		if err != nil {
			return err
		}
		switch domain.DecideCleanup(a.requestedAt, st.RequestedAt, buildActive, active || startedThisPass) {
		case domain.StartCleanup:
			if err := r.startCleanup(ctx, app, a); err != nil {
				return err
			}
			startedThisPass = true
		case domain.WaitCleanup:
			// Record the pending intent (RequestedBy) WITHOUT advancing the processed
			// marker, so DecideCleanup keeps returning Wait/Start until it runs.
			st.Phase = "Pending"
			if a.requestedBy != "" {
				st.RequestedBy = a.requestedBy
			}
		case domain.NoCleanup:
			// nothing to do
		}
	}
	return nil
}

// startCleanup creates the cleanup Job and marks the action Running, recording
// the processed requested-at token (so DecideCleanup won't re-fire it).
func (r *FrontendAppReconciler) startCleanup(ctx context.Context, app *bakerv1alpha1.FrontendApp, a cleanupAction) error {
	job := r.CleanupJob(app, a.mode)
	if err := controllerutil.SetControllerReference(app, job, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	st := actionStatus(app, a.mode)
	st.RequestedAt = a.requestedAt
	st.RequestedBy = a.requestedBy
	st.Phase = "Running"
	return nil
}

// observeCleanup reads finished cleanup Jobs, writes the per-action terminal
// status from the termination-log JSON, and GCs the processed Job (mirroring the
// du Job lifecycle). The processed marker is already at the requested token
// (stamped at start), so this only flips Phase Running->Succeeded/Failed.
func (r *FrontendAppReconciler) observeCleanup(ctx context.Context, app *bakerv1alpha1.FrontendApp) error {
	jobs := &batchv1.JobList{}
	if err := r.List(ctx, jobs, client.InNamespace(app.Namespace), client.MatchingLabels(cleanupLabelsFor(app))); err != nil {
		return err
	}
	for i := range jobs.Items {
		job := &jobs.Items[i]
		cond := jobFinished(job)
		if cond == nil {
			continue
		}
		mode := job.Labels[cleanupModeLabel]
		if mode == "" {
			continue
		}
		st := actionStatus(app, mode)
		st.LastCompleted = r.now().UTC().Format("2006-01-02T15:04:05Z07:00")
		if cond.Type == batchv1.JobComplete {
			res, msg := r.readCleanupResult(ctx, app, job)
			st.Phase = "Succeeded"
			st.ReclaimedBytes = res.Reclaimed
			st.Message = msg
		} else {
			st.Phase = "Failed"
			st.ReclaimedBytes = 0
			st.Message = "cleanup failed: " + cond.Message
		}
		policy := metav1.DeletePropagationBackground
		_ = r.Delete(ctx, job, &client.DeleteOptions{PropagationPolicy: &policy})
	}
	return nil
}

// readCleanupResult parses the cleanup container's termination-message JSON
// (selecting the pod owned by THIS Job, mirroring readMeasurement) and returns
// the parsed result plus a short human summary for status.cleanup.<action>.Message.
func (r *FrontendAppReconciler) readCleanupResult(ctx context.Context, app *bakerv1alpha1.FrontendApp, job *batchv1.Job) (CleanupResult, string) {
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(app.Namespace), client.MatchingLabels(cleanupLabelsFor(app))); err != nil {
		return CleanupResult{}, ""
	}
	var (
		term *corev1.ContainerStateTerminated
		when metav1.Time
	)
	for i := range pods.Items {
		p := &pods.Items[i]
		if !ownedByJob(p, job) {
			continue
		}
		for j := range p.Status.ContainerStatuses {
			cs := &p.Status.ContainerStatuses[j]
			if cs.Name != cleanupContainerName || cs.State.Terminated == nil {
				continue
			}
			if term == nil || cs.State.Terminated.FinishedAt.After(when.Time) {
				term = cs.State.Terminated
				when = cs.State.Terminated.FinishedAt
			}
		}
	}
	if term == nil {
		return CleanupResult{}, "cleanup completed"
	}
	var res CleanupResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(term.Message)), &res); err != nil {
		log.FromContext(ctx).Error(err, "failed to parse cleanup termination message", "job", job.Name)
		return CleanupResult{}, "cleanup completed"
	}
	return res, summarizeCleanup(res)
}

// summarizeCleanup renders a short human message from a cleanup result.
func summarizeCleanup(res CleanupResult) string {
	switch res.Action {
	case "skip":
		if res.Reason != "" {
			return "skip: " + res.Reason
		}
		return "skip"
	case "release-prune":
		return fmt.Sprintf("release-prune: kept %d, deleted %d, reclaimed %s", res.Kept, res.Deleted, humanBytes(res.Reclaimed))
	default: // cache prune
		return fmt.Sprintf("cache prune: reclaimed %s", humanBytes(res.Reclaimed))
	}
}

// humanBytes renders a byte count in IEC units (B, KiB, MiB, GiB, TiB).
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(n)/float64(div), "KMGT"[exp])
}
