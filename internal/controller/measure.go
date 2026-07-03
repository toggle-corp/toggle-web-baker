package controller

import (
	"context"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
)

// Measurement-Job constants. The role label is DISTINCT from the build role so
// measurement pods are never mistaken for build pods (buildActive / the build
// pod Watches both key off role=build).
const (
	measureRole          = "measure"
	measureRoleLabel     = "baker.toggle-corp.com/role"
	measureVolumeLabel   = "baker.toggle-corp.com/measure-volume"
	measureContainerName = "du"
	measureTargetPath    = "/target"
)

// measureTarget describes one PVC to measure: the status.storage.sizes key it
// reports under, a k8s-name-safe job-name suffix, and the PVC/volume to mount.
type measureTarget struct {
	key     string // sizes key the console resolves (cache / dataCache)
	suffix  string // job-name suffix (RFC1123-safe)
	pvcName string
	volName string
}

// measureTargets are the two PVCs the copier never measures (it mounts only
// output + work). cache/dataCache change ONLY during a build, so they are
// measured right after one.
func measureTargets(app *bakerv1alpha1.FrontendApp) []measureTarget {
	return []measureTarget{
		{key: "cache", suffix: "cache", pvcName: cacheePVCName(app), volName: volCache},
		{key: "dataCache", suffix: "data", pvcName: dataCachePVCName(app), volName: volData},
	}
}

func measureJobName(app *bakerv1alpha1.FrontendApp, suffix string) string {
	return app.Name + "-measure-" + suffix
}

// measureLabelsFor selects all of an app's measurement Jobs/pods (role=measure).
func measureLabelsFor(app *bakerv1alpha1.FrontendApp) map[string]string {
	l := labelsFor(app)
	l[measureRoleLabel] = measureRole
	return l
}

// MeasureJob builds the du Job for ONE PVC: mount the target read-only at
// /target and let the du image write the integer byte count to its termination
// message. Single-target matches the du image's contract (one bare int out).
func (r *FrontendAppReconciler) MeasureJob(app *bakerv1alpha1.FrontendApp, m measureTarget) *batchv1.Job {
	labels := measureLabelsFor(app)
	labels[measureVolumeLabel] = m.key

	podSpec := corev1.PodSpec{
		RestartPolicy:                corev1.RestartPolicyNever,
		AutomountServiceAccountToken: ptr.To(false),
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot:   ptr.To(true),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		Containers: []corev1.Container{{
			Name:  measureContainerName,
			Image: r.Config.Images.Du,
			Env:   []corev1.EnvVar{{Name: "TARGET", Value: measureTargetPath}},
			VolumeMounts: []corev1.VolumeMount{
				{Name: m.volName, MountPath: measureTargetPath, ReadOnly: true},
			},
			SecurityContext: hardenedSecurityContext(),
		}},
		Volumes: []corev1.Volume{
			{Name: m.volName, VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: m.pvcName, ReadOnly: true},
			}},
		},
	}
	if r.Config.ImagePullSecret != "" {
		podSpec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: r.Config.ImagePullSecret}}
	}
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      measureJobName(app, m.suffix),
			Namespace: app.Namespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:          ptr.To(int32(0)),
			ActiveDeadlineSeconds: ptr.To(int64(600)),
			// Short TTL is a backstop; observeMeasurement deletes the Job once it
			// has read the result.
			TTLSecondsAfterFinished: ptr.To(int32(300)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       podSpec,
			},
		},
	}
}

// measureInterval is the debounce floor between storage measurements.
func (r *FrontendAppReconciler) measureInterval() time.Duration {
	if r.Config.MeasureInterval > 0 {
		return r.Config.MeasureInterval
	}
	return time.Hour
}

// maybeStartMeasurement spawns the per-PVC du Jobs after a successful build,
// debounced by the measure interval. The copier refreshes MeasuredAt on EVERY
// build, so the decision uses prevMeasuredAt captured BEFORE the copier
// termination was applied — otherwise the debounce would always trip. A build
// must NOT be active (RWO contention: the build mounts cache RW).
func (r *FrontendAppReconciler) maybeStartMeasurement(ctx context.Context, app *bakerv1alpha1.FrontendApp, prevMeasuredAt *metav1.Time) error {
	if prevMeasuredAt != nil && r.now().Sub(prevMeasuredAt.Time) < r.measureInterval() {
		return nil
	}
	if active, _, err := r.buildActive(ctx, app); err != nil || active {
		return err
	}
	for _, m := range measureTargets(app) {
		job := r.MeasureJob(app, m)
		if err := controllerutil.SetControllerReference(app, job, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
	}
	return nil
}

// observeMeasurement reads finished du Jobs and MERGES their byte counts into
// status.storage.sizes (keyed cache / dataCache), preserving the copier's
// output/source entries. A read Job is deleted so the result is recorded exactly
// once (a re-read would needlessly re-stamp MeasuredAt). Failed Jobs are skipped
// (left for their TTL).
func (r *FrontendAppReconciler) observeMeasurement(ctx context.Context, app *bakerv1alpha1.FrontendApp) error {
	jobs := &batchv1.JobList{}
	if err := r.List(ctx, jobs, client.InNamespace(app.Namespace), client.MatchingLabels(measureLabelsFor(app))); err != nil {
		return err
	}
	for i := range jobs.Items {
		job := &jobs.Items[i]
		if cond := jobFinished(job); cond == nil || cond.Type != batchv1.JobComplete {
			continue
		}
		key := job.Labels[measureVolumeLabel]
		if key == "" {
			continue
		}
		bytes, ok := r.readMeasurement(ctx, app, job)
		if !ok {
			continue
		}
		r.recordSize(app, key, bytes)
		policy := metav1.DeletePropagationBackground
		_ = r.Delete(ctx, job, &client.DeleteOptions{PropagationPolicy: &policy})
	}
	return nil
}

// readMeasurement parses the bare integer byte count from the du pod's
// termination message. It selects only the pod owned by THIS Job (mirroring
// applyCopierTermination) and the most recently terminated du container.
func (r *FrontendAppReconciler) readMeasurement(ctx context.Context, app *bakerv1alpha1.FrontendApp, job *batchv1.Job) (int64, bool) {
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(app.Namespace), client.MatchingLabels(measureLabelsFor(app))); err != nil {
		return 0, false
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
			if cs.Name != measureContainerName || cs.State.Terminated == nil {
				continue
			}
			if term == nil || cs.State.Terminated.FinishedAt.After(when.Time) {
				term = cs.State.Terminated
				when = cs.State.Terminated.FinishedAt
			}
		}
	}
	if term == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(term.Message), 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// recordPVCCapacities reads the app's three PVCs' bound capacities into
// status.storage.capacities (keyed cache / dataCache / output — the same keys
// the sizes map uses) so the console can draw storage fill bars against the
// REAL provisioned size when no spec.storage cap applies; outputTotal in
// particular has no spec cap but is physically bounded by the output PVC.
// Best-effort: an unbound or unreadable PVC simply leaves its key absent.
func (r *FrontendAppReconciler) recordPVCCapacities(ctx context.Context, app *bakerv1alpha1.FrontendApp) {
	targets := map[string]string{
		"cache":     cacheePVCName(app),
		"dataCache": dataCachePVCName(app),
		"output":    outputPVCName(app),
	}
	caps := map[string]int64{}
	for key, name := range targets {
		pvc := &corev1.PersistentVolumeClaim{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: app.Namespace, Name: name}, pvc); err != nil {
			continue
		}
		if q, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok && q.Value() > 0 {
			caps[key] = q.Value()
		}
	}
	if len(caps) > 0 {
		app.Status.Storage.Capacities = caps
	}
}

// recordSize merges one measured size into status.storage.sizes, records the
// delta from the prior measurement (so the console can show growth), and
// refreshes MeasuredAt. It never clobbers other keys.
func (r *FrontendAppReconciler) recordSize(app *bakerv1alpha1.FrontendApp, key string, bytes int64) {
	if app.Status.Storage.Sizes == nil {
		app.Status.Storage.Sizes = map[string]int64{}
	}
	if prev, ok := app.Status.Storage.Sizes[key]; ok {
		if app.Status.Storage.LastRunDeltas == nil {
			app.Status.Storage.LastRunDeltas = map[string]int64{}
		}
		app.Status.Storage.LastRunDeltas[key] = bytes - prev
	}
	app.Status.Storage.Sizes[key] = bytes
	app.Status.Storage.MeasuredAt = ptr.To(metav1.NewTime(r.now()))
}
