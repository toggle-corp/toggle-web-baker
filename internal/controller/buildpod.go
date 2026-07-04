package controller

import (
	"fmt"
	"sort"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
	"github.com/toggle-corp/toggle-web-baker/internal/domain"
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
	volShim   = "shim"

	// shimMountPath is where the peak-memory wrapper binary lands (a tiny
	// emptyDir shared from the shim-install init container into every user
	// phase, mounted read-only there).
	shimMountPath = "/baker"
	shimBinary    = shimMountPath + "/shim"
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

// resolvedSecurityContext is the hardened context for a phase container,
// pinning the resolved runAsUser (the managed node image's UID, or a BYO phase's
// own runAsUser). runAsNonRoot alone is not enough for an image whose USER is a
// non-numeric name (e.g. cimg/node's `circleci`): the kubelet cannot verify a
// named user is non-root and admission fails, so a numeric UID is pinned here.
func resolvedSecurityContext(rp domain.ResolvedPhase) *corev1.SecurityContext {
	sc := hardenedSecurityContext()
	if rp.RunAsUser != nil {
		sc.RunAsUser = rp.RunAsUser
	}
	return sc
}

// withHome injects HOME when the resolution supplies one (managed node phases,
// where HOME must point at a writable path under readOnlyRootFilesystem). It is
// appended LAST so the operator-managed HOME is authoritative over any HOME a
// managed phase's own env might set (the kubelet applies env in order; the last
// assignment wins). BYO and clone-fallback phases inject nothing (Home == "") —
// the app owns its own env there.
func withHome(rp domain.ResolvedPhase, env []corev1.EnvVar) []corev1.EnvVar {
	if rp.Home == "" {
		return env
	}
	return append(env, corev1.EnvVar{Name: "HOME", Value: rp.Home})
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

// clockUID is the non-root UID pinned on the clock CronJob container. The stock
// registry.k8s.io/kubectl image runs as root, so runAsNonRoot alone fails
// admission (the kubelet can't prove a root image is non-root) exactly as it
// does for nginx above; pinning a numeric non-root UID satisfies the gate. 65532
// is the conventional distroless "nonroot" UID. No RunAsGroup: the clock writes
// nothing (readOnlyRootFilesystem) and only READS the world-readable SA token.
const clockUID int64 = 65532

// clockSecurityContext is the hardened context for the clock CronJob container,
// pinning runAsUser to clockUID so the root kubectl image passes runAsNonRoot.
func clockSecurityContext() *corev1.SecurityContext {
	sc := hardenedSecurityContext()
	sc.RunAsUser = ptr.To(clockUID)
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

// shimWrap prefixes a phase command with the peak-memory shim: the shim execs
// the command verbatim (argv/env/cwd/uid unchanged, signals forwarded), then
// appends the container cgroup's memory.peak to the termination log so the
// operator can record the phase's TRUE peak memory (sampling can never observe
// a maximum). See images/shim.
func shimWrap(cmd []string) []string {
	return append([]string{shimBinary, "--"}, cmd...)
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

// envMapVars converts a phase's literal-only envMap to corev1.EnvVar entries,
// SORTED BY KEY so the container's env ordering is deterministic. Callers append
// these AFTER the phase's array env (toEnvVars) — a key can never appear in both
// (rejected at admission), so no dedupe is needed here.
func envMapVars(in map[string]string) []corev1.EnvVar {
	if len(in) == 0 {
		return nil
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]corev1.EnvVar, 0, len(in))
	for _, k := range keys {
		out = append(out, corev1.EnvVar{Name: k, Value: in[k]})
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

// resolvePhase computes the effective image/UID/HOME for one phase, applying the
// spec.pipeline.nodeVersion mapping and per-phase BYO overrides (see domain.ResolvePhase).
func (r *AppReconciler) resolvePhase(app *bakerv1alpha1.App, phase bakerv1alpha1.PhaseSpec) domain.ResolvedPhase {
	return domain.ResolvePhase(phase.Image, phase.RunAsUser, app.Spec.Pipeline.NodeVersion, r.Config.NodeImages, r.Config.Images.Clone)
}

// buildVolumesAndMounts returns the pod volumes plus the cache/work mounts,
// BRANCHING on packageManager. yarn: node_modules live on a per-run emptyDir
// (work), cache PVC holds only the yarn cache. pnpm: the pnpm store AND
// node_modules both live on the cache PVC (mounted RW), so the build phase
// mounts cache RW in both cases.
func buildVolumesAndMounts(app *bakerv1alpha1.App) (volumes []corev1.Volume, cacheMount corev1.VolumeMount) {
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
func pkgManagerEnv(app *bakerv1alpha1.App) []corev1.EnvVar {
	switch app.Spec.Pipeline.PackageManager {
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
// [shim-install, clone, setup, fetch, build]; the MAIN container is the copier.
// The build container NEVER mounts the output PVC; secrets go ONLY to fetch.
// User phases (setup/fetch/build) are wrapped by the peak-memory shim; clone
// and copier are platform entrypoints and stay unwrapped.
func (r *AppReconciler) BuildJob(app *bakerv1alpha1.App, token string, gitCred gitCredentialDecision) *batchv1.Job {
	volumes, cacheMount := buildVolumesAndMounts(app)
	base := commonMounts()
	pmEnv := pkgManagerEnv(app)

	// shim-install: places the static wrapper binary on the shim emptyDir
	// BEFORE any user phase runs (init containers are ordered). scratch image;
	// the binary self-copies (`shim install`).
	volumes = append(volumes, corev1.Volume{Name: volShim, VolumeSource: corev1.VolumeSource{
		EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: resource.NewQuantity(16*1024*1024, resource.BinarySI)},
	}})
	shimInstall := corev1.Container{
		Name:            "shim-install",
		Image:           r.Config.Images.Shim,
		Command:         []string{"/shim", "install", shimBinary},
		VolumeMounts:    []corev1.VolumeMount{{Name: volShim, MountPath: shimMountPath}},
		SecurityContext: hardenedSecurityContext(),
	}
	// User phases mount the binary read-only.
	shimMount := corev1.VolumeMount{Name: volShim, MountPath: shimMountPath, ReadOnly: true}

	// Resolve each phase's effective image/UID/HOME (nodeVersion mapping + BYO
	// overrides). HOME is injected only for managed node phases. Resolved BEFORE
	// clone because clone is pinned to the build phase's UID (below).
	// setup is resolved through effectiveSetup: it honors skip, injects the
	// managed-toolchain default install command when setup is omitted, and reports
	// whether a setup container exists at all (setupOn). The injected command is
	// applied here only — never written back to the spec — so it can't leak into
	// the staleness hash (buildSpecFrom reads the spec as-written).
	setupSpec, setupOn := effectiveSetup(app, r.Config)
	setupR := r.resolvePhase(app, setupSpec)
	fetchR := r.resolvePhase(app, app.Spec.Pipeline.Phases.Fetch.PhaseSpec)
	buildR := r.resolvePhase(app, app.Spec.Pipeline.Phases.Build.PhaseSpec)

	// clone: platform image, no caches needed beyond work. SUBMODULES is set
	// ONLY when the app opts in; the entrypoint defaults to no submodule
	// recursion when the env is absent.
	cloneEnv := []corev1.EnvVar{
		{Name: "REPO", Value: app.Spec.Repo},
		{Name: "REF", Value: app.Spec.Ref},
		{Name: "SRC_DIR", Value: workMountPath},
	}
	if app.Spec.Pipeline.Submodules {
		cloneEnv = append(cloneEnv, corev1.EnvVar{Name: "SUBMODULES", Value: "1"})
	}
	// clone runs as the BUILD phase's resolved UID (the managed node image's UID,
	// or a BYO build.runAsUser) — NOT the clone image's own default. Rationale:
	// clone writes the checkout into /work, so the checkout (and every subdir git
	// creates, mode 755) is owned by whoever clone runs as. The build phases then
	// need to WRITE into that tree — e.g. graphql-codegen emitting a gitignored
	// src/generated/, or any in-tree codegen — which fails with EACCES if clone
	// owns it under a different UID. Pinning clone to the build UID makes the
	// checkout writable by the phases. When the build phase has no resolved UID
	// (BYO image without runAsUser) clone keeps its image default (nil == no
	// override). This deliberately drops the older "checkout is read-only input
	// to the toolchain" posture in favour of supporting in-tree codegen.
	// Git credential (design Q3/Q4/Q6/Q7): the effective credential is mounted
	// into clone AFTER the podSpec is assembled, via the shared addGitCredential
	// helper (host-scoped) — see below. Anonymous (empty decision) adds nothing.
	clone := corev1.Container{
		Name:            "clone",
		Image:           r.Config.Images.Clone,
		Env:             cloneEnv,
		VolumeMounts:    base,
		SecurityContext: resolvedSecurityContext(buildR),
	}

	// setup: install deps. Mounts cache (RW for pnpm store / yarn cache).
	setupMounts := append(append([]corev1.VolumeMount{}, base...), cacheMount, shimMount)
	setup := corev1.Container{
		Name:            "setup",
		Image:           setupR.Image,
		Command:         shimWrap(commandOrNoop(setupSpec.Command)),
		WorkingDir:      workMountPath,
		Env:             withHome(setupR, append(append(append([]corev1.EnvVar{}, pmEnv...), toEnvVars(setupSpec.Env)...), envMapVars(setupSpec.EnvMap)...)),
		VolumeMounts:    setupMounts,
		Resources:       phaseResourceRequirements(r.Config, "setup", setupSpec.MemoryLimit),
		SecurityContext: resolvedSecurityContext(setupR),
	}

	// fetch: the ONLY container that receives secrets. Writes to /data.
	fetchEnv := append([]corev1.EnvVar{}, toEnvVars(app.Spec.Pipeline.Phases.Fetch.Env)...)
	fetchEnv = append(fetchEnv, envMapVars(app.Spec.Pipeline.Phases.Fetch.EnvMap)...)
	fetchEnv = append(fetchEnv, toSecretEnvVars(app.Spec.Pipeline.Phases.Fetch.Secrets)...)
	fetchMounts := append(append([]corev1.VolumeMount{}, base...),
		corev1.VolumeMount{Name: volData, MountPath: dataMountPath}, shimMount)
	fetch := corev1.Container{
		Name:            "fetch",
		Image:           fetchR.Image,
		Command:         shimWrap(commandOrNoop(app.Spec.Pipeline.Phases.Fetch.Command)),
		WorkingDir:      workMountPath,
		Env:             withHome(fetchR, fetchEnv),
		VolumeMounts:    fetchMounts,
		Resources:       phaseResourceRequirements(r.Config, "fetch", app.Spec.Pipeline.Phases.Fetch.MemoryLimit),
		SecurityContext: resolvedSecurityContext(fetchR),
	}

	// build: public build.env + NODE_OPTIONS etc. Mounts cache RW (both PMs) and
	// data RO. NEVER mounts the output PVC.
	buildEnv := append([]corev1.EnvVar{}, pmEnv...)
	buildEnv = append(buildEnv, toEnvVars(app.Spec.Pipeline.Phases.Build.Env)...)
	buildEnv = append(buildEnv, envMapVars(app.Spec.Pipeline.Phases.Build.EnvMap)...)
	buildMounts := append(append([]corev1.VolumeMount{}, base...),
		cacheMount,
		corev1.VolumeMount{Name: volData, MountPath: dataMountPath, ReadOnly: true},
		shimMount)
	build := corev1.Container{
		Name:            "build",
		Image:           buildR.Image,
		Command:         shimWrap(app.Spec.Pipeline.Phases.Build.Command),
		WorkingDir:      workMountPath,
		Env:             withHome(buildR, buildEnv),
		VolumeMounts:    buildMounts,
		Resources:       phaseResourceRequirements(r.Config, "build", app.Spec.Pipeline.Phases.Build.MemoryLimit),
		SecurityContext: resolvedSecurityContext(buildR),
	}

	// copier (MAIN): the only container mounting the output PVC. Publishes the
	// built bundle and emits build-derived status as a termination message.
	outputDir := app.Spec.Pipeline.Phases.Build.OutputDir
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

	// Ordered init containers: [shim-install, clone, (setup), fetch, build] —
	// setup is present only when it runs (skip:true, or an omitted setup under a
	// BYO toolchain, drops it).
	inits := []corev1.Container{shimInstall, clone}
	if setupOn {
		inits = append(inits, setup)
	}
	inits = append(inits, fetch, build)

	podSpec := corev1.PodSpec{
		RestartPolicy:                corev1.RestartPolicyNever,
		AutomountServiceAccountToken: ptr.To(false),
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot:   ptr.To(true),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		InitContainers: inits,
		Containers:     []corev1.Container{copier},
		Volumes:        volumes,
	}
	if r.Config.ImagePullSecret != "" {
		podSpec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: r.Config.ImagePullSecret}}
	}

	// Git credential (design Q3/Q4/Q6/Q7): wire the threaded decision into the
	// clone initContainer via the shared helper. Host-scoped to the repo's own
	// host (GIT_CREDENTIAL_HOST) so a submodule/redirect to another host can't
	// harvest the token. host="" (unparseable override repo) fails closed inside
	// addGitCredential (anonymous). Anonymous decision (no secret) adds nothing.
	if gitCred.mounts() {
		host, _ := domain.RepoHost(app.Spec.Repo)
		addGitCredential(&podSpec, "clone", gitCred.secretName, host)
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
			ActiveDeadlineSeconds: ptr.To(r.buildDeadlineSeconds(app)),
			// TTL is set on SUCCESS only (failed jobs are retained); the
			// reconciler stamps the TTL when it observes success.
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: buildLabelsFor(app)},
				Spec:       podSpec,
			},
		},
	}
}

// buildDeadlineSeconds derives the build Job's activeDeadlineSeconds.
// pipeline.timeout is a duration; the k8s Job wants whole seconds. Anything
// non-positive — unset (nil), a sub-second value that truncates to 0, or a
// negative duration — falls back to the operator-config default rather than
// producing an invalid (<0) or absent deadline the apiserver would reject.
// CEL rejects non-positive values at admission; this guards objects admitted
// before that rule existed.
func (r *AppReconciler) buildDeadlineSeconds(app *bakerv1alpha1.App) int64 {
	deadline := int64(0)
	if t := app.Spec.Pipeline.Timeout; t != nil {
		deadline = int64(t.Seconds())
	}
	if deadline <= 0 {
		deadline = r.Config.ActiveDeadlineSeconds
	}
	return deadline
}

// phaseResourceRequirements computes the resource requirements for one phase
// container (setup/fetch/build). The memory ceiling is the user's per-phase
// memoryLimit when it is non-empty AND parses; otherwise it is the operator's
// per-phase default. SECURITY INVARIANT: a malformed user memoryLimit must NEVER
// yield a container with no memory limit (an untrusted build could then OOM the
// node) — it falls back to the per-phase operator default, which is validated at
// startup and so always parses. Memory request is pinned == limit (memory is
// incompressible ⇒ Guaranteed QoS; avoids a low-request/high-limit node OOM).
// CPU request/limit are the global operator defaults (same for all phases).
func phaseResourceRequirements(cfg OperatorConfig, phaseName, userMemLimit string) corev1.ResourceRequirements {
	memCeiling := cfg.PhaseResourceDefaults.MemoryForPhase(phaseName)
	if userMemLimit != "" {
		if q, err := resource.ParseQuantity(userMemLimit); err == nil {
			memCeiling = q
		}
	}
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceMemory: memCeiling,
			corev1.ResourceCPU:    cfg.PhaseResourceDefaults.CPURequest,
		},
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: memCeiling,
			corev1.ResourceCPU:    cfg.PhaseResourceDefaults.CPULimit,
		},
	}
}

// buildJobName derives a deterministic, token-suffixed Job name so each rebuild
// token maps to a distinct Job (failed jobs of prior tokens are retained).
func buildJobName(app *bakerv1alpha1.App, token string) string {
	return app.Name + "-build-" + shortToken(token)
}
