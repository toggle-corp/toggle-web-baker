package controller

import (
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
)

// Volume + mount paths shared across the build pod.
const (
	workMountPath   = "/work"  // checkout + node_modules + build output (writable)
	cacheMountPath  = "/cache" // package-manager cache (and pnpm store / node_modules on pnpm)
	dataMountPath   = "/data"  // dataCache PVC (fetched data)
	outputMountPath = "/output"

	volWork   = "work"
	volCache  = "cache"
	volData   = "data"
	volOutput = "output"
	volTmp    = "tmp"
)

// hardenedSecurityContext is the per-container securityContext baked onto every
// build-pod container. readOnlyRootFilesystem is on; the writable surface is
// supplied exclusively via the explicit volumes (work, cache, tmp).
func hardenedSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		RunAsNonRoot:             ptr.To(true),
		ReadOnlyRootFilesystem:   ptr.To(true),
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

// phaseSecurityContext is the hardened context for a user-supplied phase
// container (setup / fetch / build), pinning runAsUser when the app sets one.
// runAsNonRoot alone is not enough for an image whose USER is a non-numeric
// name (e.g. cimg/node's `circleci`): the kubelet cannot verify a named user is
// non-root and admission fails, so the app supplies the numeric UID here.
func phaseSecurityContext(p bakerv1alpha1.PhaseSpec) *corev1.SecurityContext {
	sc := hardenedSecurityContext()
	if p.RunAsUser != nil {
		sc.RunAsUser = p.RunAsUser
	}
	return sc
}

// nginxUID is the UID/GID baked into docker.io/nginxinc/nginx-unprivileged.
const nginxUID int64 = 101

// nginxSecurityContext is the hardened context for the serving nginx container.
// It mirrors hardenedSecurityContext but pins runAsUser/runAsGroup to 101 so the
// unprivileged nginx image satisfies the pod's runAsNonRoot constraint (without
// an explicit user the kubelet cannot prove the image is non-root and admission
// fails). readOnlyRootFilesystem stays on; the writable surface is the explicit
// /tmp + /var/cache/nginx + /var/run emptyDir mounts.
func nginxSecurityContext() *corev1.SecurityContext {
	sc := hardenedSecurityContext()
	sc.RunAsUser = ptr.To(nginxUID)
	sc.RunAsGroup = ptr.To(nginxUID)
	return sc
}

// commandOrNoop returns cmd, or a no-op ["true"] when cmd is empty so an
// unspecified optional phase (setup/fetch) doesn't fall through to the
// base image's entrypoint.
func commandOrNoop(cmd []string) []string {
	if len(cmd) == 0 {
		return []string{"true"}
	}
	return cmd
}

// toEnvVars converts public spec EnvVars to corev1.EnvVar. secretKeyRef is
// structurally impossible here, so this can never carry a secret.
func toEnvVars(in []bakerv1alpha1.EnvVar) []corev1.EnvVar {
	out := make([]corev1.EnvVar, 0, len(in))
	for _, e := range in {
		ev := corev1.EnvVar{Name: e.Name, Value: e.Value}
		if e.ValueFrom != nil && e.ValueFrom.ConfigMapKeyRef != nil {
			ev.ValueFrom = &corev1.EnvVarSource{
				ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: e.ValueFrom.ConfigMapKeyRef.Name},
					Key:                  e.ValueFrom.ConfigMapKeyRef.Key,
				},
			}
		}
		out = append(out, ev)
	}
	return out
}

// toSecretEnvVars converts the fetch-only secret env. These are injected ONLY
// into the fetch container.
func toSecretEnvVars(in []bakerv1alpha1.EnvVarWithSecret) []corev1.EnvVar {
	out := make([]corev1.EnvVar, 0, len(in))
	for _, e := range in {
		out = append(out, corev1.EnvVar{
			Name: e.Name,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: e.ValueFrom.SecretKeyRef.Name},
					Key:                  e.ValueFrom.SecretKeyRef.Key,
				},
			},
		})
	}
	return out
}

// imageOr returns the phase image if set, else a fallback.
func imageOr(phase bakerv1alpha1.PhaseSpec, fallback string) string {
	if phase.Image != "" {
		return phase.Image
	}
	return fallback
}

// buildVolumesAndMounts returns the pod volumes plus the cache/work mounts,
// BRANCHING on packageManager. yarn: node_modules live on a per-run emptyDir
// (work), cache PVC holds only the yarn cache. pnpm: the pnpm store AND
// node_modules both live on the cache PVC (mounted RW), so the build phase
// mounts cache RW in both cases.
func buildVolumesAndMounts(app *bakerv1alpha1.FrontendApp) (volumes []corev1.Volume, cacheMount corev1.VolumeMount) {
	volumes = []corev1.Volume{
		// Per-run scratch: checkout + (yarn) node_modules + build output.
		{Name: volWork, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		// Writable /tmp so readOnlyRootFilesystem doesn't break tools.
		{Name: volTmp, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		// Cache PVC (yarn cache, or pnpm store + node_modules).
		{Name: volCache, VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: cacheePVCName(app)},
		}},
		// Fetched-data PVC.
		{Name: volData, VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: dataCachePVCName(app)},
		}},
	}
	cacheMount = corev1.VolumeMount{Name: volCache, MountPath: cacheMountPath}
	return volumes, cacheMount
}

// commonMounts are the mounts every build container gets (writable scratch).
func commonMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{Name: volWork, MountPath: workMountPath},
		{Name: volTmp, MountPath: "/tmp"},
	}
}

// pkgManagerEnv returns the env that points the package manager at the cache
// volume, branching on the package manager.
//
// pnpm: the content-addressable store AND node_modules must live on the SAME
// filesystem (the cache PVC) so pnpm's hard-links work and persist across runs.
// pnpm honors npm_config_* keys, so npm_config_store_dir / npm_config_modules_dir
// place both on the cache PVC. (The previous NODE_MODULES_DIR key is bogus — no
// tool reads it.)
//
// yarn: node_modules live on the per-run emptyDir (work volume, set by
// WorkingDir); the cache PVC holds only the yarn download cache.
func pkgManagerEnv(app *bakerv1alpha1.FrontendApp) []corev1.EnvVar {
	switch app.Spec.PackageManager {
	case bakerv1alpha1.PackageManagerPnpm:
		return []corev1.EnvVar{
			{Name: "npm_config_store_dir", Value: cacheMountPath + "/pnpm-store"},
			// node_modules on the cache PVC (same FS as the store ⇒ hard-links work).
			{Name: "npm_config_modules_dir", Value: cacheMountPath + "/node_modules"},
		}
	default: // yarn
		return []corev1.EnvVar{
			{Name: "YARN_CACHE_FOLDER", Value: cacheMountPath + "/yarn"},
		}
	}
}

// BuildJob is the SINGLE SOURCE OF TRUTH for the build pod. initContainers are
// [clone, setup, fetch, build]; the MAIN container is the copier. The build
// container NEVER mounts the output PVC; secrets go ONLY to fetch.
func (r *FrontendAppReconciler) BuildJob(app *bakerv1alpha1.FrontendApp, token string) *batchv1.Job {
	volumes, cacheMount := buildVolumesAndMounts(app)
	base := commonMounts()
	pmEnv := pkgManagerEnv(app)

	publicEnv := toEnvVars(app.Spec.BuildArgs)

	// clone: platform image, no caches needed beyond work. SUBMODULES is set
	// ONLY when the app opts in; the entrypoint defaults to no submodule
	// recursion when the env is absent.
	cloneEnv := []corev1.EnvVar{
		{Name: "REPO", Value: app.Spec.Repo},
		{Name: "REF", Value: app.Spec.Ref},
		{Name: "SRC_DIR", Value: workMountPath},
	}
	if app.Spec.Submodules {
		cloneEnv = append(cloneEnv, corev1.EnvVar{Name: "SUBMODULES", Value: "1"})
	}
	clone := corev1.Container{
		Name:            "clone",
		Image:           r.Config.Images.Clone,
		Env:             cloneEnv,
		VolumeMounts:    base,
		SecurityContext: hardenedSecurityContext(),
	}

	// setup: install deps. Mounts cache (RW for pnpm store / yarn cache).
	setupMounts := append(append([]corev1.VolumeMount{}, base...), cacheMount)
	setup := corev1.Container{
		Name:            "setup",
		Image:           imageOr(app.Spec.Setup, r.Config.Images.Clone),
		Command:         commandOrNoop(app.Spec.Setup.Command),
		WorkingDir:      workMountPath,
		Env:             append(append([]corev1.EnvVar{}, pmEnv...), toEnvVars(app.Spec.Setup.Env)...),
		VolumeMounts:    setupMounts,
		SecurityContext: phaseSecurityContext(app.Spec.Setup),
	}

	// fetch: the ONLY container that receives secrets. Writes to /data.
	fetchEnv := append([]corev1.EnvVar{}, toEnvVars(app.Spec.Fetch.Env)...)
	fetchEnv = append(fetchEnv, toSecretEnvVars(app.Spec.Secrets)...)
	fetchMounts := append(append([]corev1.VolumeMount{}, base...),
		corev1.VolumeMount{Name: volData, MountPath: dataMountPath})
	fetch := corev1.Container{
		Name:            "fetch",
		Image:           imageOr(app.Spec.Fetch, r.Config.Images.Clone),
		Command:         commandOrNoop(app.Spec.Fetch.Command),
		WorkingDir:      workMountPath,
		Env:             fetchEnv,
		VolumeMounts:    fetchMounts,
		SecurityContext: phaseSecurityContext(app.Spec.Fetch),
	}

	// build: public buildArgs + NODE_OPTIONS etc. Mounts cache RW (both PMs) and
	// data RO. NEVER mounts the output PVC.
	buildEnv := append([]corev1.EnvVar{}, pmEnv...)
	buildEnv = append(buildEnv, publicEnv...)
	buildEnv = append(buildEnv, toEnvVars(app.Spec.Build.Env)...)
	buildMounts := append(append([]corev1.VolumeMount{}, base...),
		cacheMount,
		corev1.VolumeMount{Name: volData, MountPath: dataMountPath, ReadOnly: true})
	build := corev1.Container{
		Name:            "build",
		Image:           imageOr(app.Spec.Build, r.Config.Images.Clone),
		Command:         app.Spec.Build.Command,
		WorkingDir:      workMountPath,
		Env:             buildEnv,
		VolumeMounts:    buildMounts,
		Resources:       buildResourceRequirements(app),
		SecurityContext: phaseSecurityContext(app.Spec.Build),
	}

	// copier (MAIN): the only container mounting the output PVC. Publishes the
	// built bundle and emits build-derived status as a termination message.
	outputDir := app.Spec.OutputDir
	if outputDir == "" {
		outputDir = "dist"
	}
	copier := corev1.Container{
		Name:  "copier",
		Image: r.Config.Images.Copier,
		Env: []corev1.EnvVar{
			{Name: "BUILD_TOKEN", Value: token},
			{Name: "WORKSPACE", Value: workMountPath},
			{Name: "OUTPUT_ROOT", Value: outputMountPath},
			{Name: "OUTPUT_DIR", Value: outputDir},
			{Name: "KEEP_RELEASES", Value: fmt.Sprintf("%d", app.Spec.KeepReleases)},
		},
		VolumeMounts: append(append([]corev1.VolumeMount{}, base...),
			corev1.VolumeMount{Name: volOutput, MountPath: outputMountPath}),
		TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
		SecurityContext:          hardenedSecurityContext(),
	}

	// Output PVC volume is added ONLY for the copier (build never sees it).
	volumes = append(volumes, corev1.Volume{Name: volOutput, VolumeSource: corev1.VolumeSource{
		PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: outputPVCName(app)},
	}})

	podSpec := corev1.PodSpec{
		RestartPolicy:                corev1.RestartPolicyNever,
		AutomountServiceAccountToken: ptr.To(false),
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot:   ptr.To(true),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		InitContainers: []corev1.Container{clone, setup, fetch, build},
		Containers:     []corev1.Container{copier},
		Volumes:        volumes,
	}
	if r.Config.ImagePullSecret != "" {
		podSpec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: r.Config.ImagePullSecret}}
	}

	deadline := app.Spec.Resources.ActiveDeadlineSeconds
	if deadline == 0 {
		deadline = 1800
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      buildJobName(app, token),
			Namespace: app.Namespace,
			Labels:    buildLabelsFor(app),
			Annotations: map[string]string{
				bakerv1alpha1.RebuildAnnotation: token,
				// Stamp the spec hash the build runs with, so on success we record
				// THIS hash (not the live spec, which may be edited mid-flight).
				bakerv1alpha1.SpecHashAnnotation: buildSpecFrom(app).Hash(),
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:          ptr.To(int32(0)),
			ActiveDeadlineSeconds: ptr.To(deadline),
			// TTL is set on SUCCESS only (failed jobs are retained); the
			// reconciler stamps the TTL when it observes success.
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: buildLabelsFor(app)},
				Spec:       podSpec,
			},
		},
	}
}

func buildResourceRequirements(app *bakerv1alpha1.FrontendApp) corev1.ResourceRequirements {
	limits := corev1.ResourceList{}
	requests := corev1.ResourceList{}
	const defaultMemLimit = "6Gi"
	memLimit := app.Spec.Resources.Build.MemoryLimit
	if memLimit == "" {
		memLimit = defaultMemLimit
	}
	q, err := resource.ParseQuantity(memLimit)
	if err != nil {
		// A malformed memoryLimit must NOT yield a pod with no memory limit (that
		// would let an untrusted build OOM the node). Fall back to the documented
		// default instead.
		q = resource.MustParse(defaultMemLimit)
	}
	limits[corev1.ResourceMemory] = q
	if app.Spec.Resources.Build.CPURequest != "" {
		if q, err := resource.ParseQuantity(app.Spec.Resources.Build.CPURequest); err == nil {
			requests[corev1.ResourceCPU] = q
		}
	}
	if app.Spec.Resources.Build.MemoryRequest != "" {
		if q, err := resource.ParseQuantity(app.Spec.Resources.Build.MemoryRequest); err == nil {
			requests[corev1.ResourceMemory] = q
		}
	}
	rr := corev1.ResourceRequirements{Limits: limits}
	if len(requests) > 0 {
		rr.Requests = requests
	}
	return rr
}

// buildJobName derives a deterministic, token-suffixed Job name so each rebuild
// token maps to a distinct Job (failed jobs of prior tokens are retained).
func buildJobName(app *bakerv1alpha1.FrontendApp, token string) string {
	return app.Name + "-build-" + shortToken(token)
}
