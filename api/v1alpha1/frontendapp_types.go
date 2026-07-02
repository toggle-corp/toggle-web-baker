package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Annotation / condition / phase constants shared by the API and the controller.
const (
	// RebuildAnnotation carries the rebuild request token. The operator compares
	// its value against status.lastProcessedRebuild via domain.DecideBuild. It is
	// set by the manual-rebuild UI, by the CronJob clock tick, and seeded by the
	// operator on first reconcile (first-build bootstrap).
	RebuildAnnotation = "rebuild.baker.toggle-corp.com/requested-at"

	// RebuildByAnnotation carries the user who requested a MANUAL rebuild (set by
	// the console alongside RebuildAnnotation). Its presence lets the operator
	// classify a build's trigger as Manual vs Scheduled. The clock tick clears it
	// so a stale manual "by" can't mislabel a later scheduled build. This const
	// mirrors the console's view.AnnotationRebuildBy without importing it.
	RebuildByAnnotation = "rebuild.baker.toggle-corp.com/by"

	// FinalizerName guards a best-effort abort of an in-flight build Job on delete.
	FinalizerName = "baker.toggle-corp.com/finalizer"

	// DefaultSchedule is the clock CronJob's schedule when Spec.Schedule is empty.
	// It MUST match the +kubebuilder:default on Spec.Schedule (a magic comment
	// that cannot reference this const) and the console's view.DefaultSchedule.
	DefaultSchedule = "0 */12 * * *"

	// SpecHashAnnotation stamps the build-relevant spec hash onto the build Job at
	// CREATION time, so on success the operator records the hash of the spec the
	// build ACTUALLY ran — not the (possibly edited) live spec at observe-time.
	SpecHashAnnotation = "baker.toggle-corp.com/spec-hash"

	// CleanupCacheRequestedAtAnnotation carries a manual cache-cleanup request
	// token. The operator compares its value against status.cleanup.cache to
	// decide whether to launch a MODE=cache cleanup pod. It is set by the console
	// alongside CleanupCacheByAnnotation, mirroring the rebuild annotations.
	CleanupCacheRequestedAtAnnotation = "cleanup-cache.baker.toggle-corp.com/requested-at"

	// CleanupCacheByAnnotation carries the user who requested a manual cache
	// cleanup (set by the console alongside CleanupCacheRequestedAtAnnotation),
	// so the operator can attribute the action in status.cleanup.cache.
	CleanupCacheByAnnotation = "cleanup-cache.baker.toggle-corp.com/by"

	// CleanupReleasesRequestedAtAnnotation carries a manual release-prune request
	// token. The operator compares its value against status.cleanup.releases to
	// decide whether to launch a MODE=releases cleanup pod. It is set by the
	// console alongside CleanupReleasesByAnnotation, mirroring the rebuild
	// annotations.
	CleanupReleasesRequestedAtAnnotation = "cleanup-releases.baker.toggle-corp.com/requested-at"

	// CleanupReleasesByAnnotation carries the user who requested a manual release
	// prune (set by the console alongside CleanupReleasesRequestedAtAnnotation),
	// so the operator can attribute the action in status.cleanup.releases.
	CleanupReleasesByAnnotation = "cleanup-releases.baker.toggle-corp.com/by"
)

// Phase is the derived top-level lifecycle phase (computed from conditions).
type Phase string

const (
	PhaseAwaitingFirstBuild Phase = "AwaitingFirstBuild"
	PhaseBuilding           Phase = "Building"
	PhaseReady              Phase = "Ready"
	PhaseDegraded           Phase = "Degraded"
)

// Condition type names owned by the operator.
const (
	ConditionReady          = "Ready"
	ConditionBuildSucceeded = "BuildSucceeded"
	ConditionIngressReady   = "IngressReady"
	ConditionDegraded       = "Degraded"
)

// Reasons surfaced on the Ready condition.
const (
	ReasonAwaitingFirstBuild  = "AwaitingFirstBuild"
	ReasonBuilding            = "Building"
	ReasonReady               = "Ready"
	ReasonInvalidStorageClass = "InvalidStorageClass"
	ReasonImageNotAllowed     = "ImageNotAllowed"
	ReasonUnknownNodeVersion  = "UnknownNodeVersion"
	ReasonInvalidStorage      = "InvalidStorage"
	ReasonInvalidSpec         = "InvalidSpec"
	ReasonMissingTLSSecret    = "MissingTLSSecret"
	ReasonConfigError         = "ConfigError"
	// ReasonBuildFailed is the generic failure reason on the BuildSucceeded /
	// Degraded conditions when a build fails for a non-OOM reason.
	ReasonBuildFailed = "BuildFailed"
	// ReasonOOMKilled is set on the BuildSucceeded/Degraded conditions when a
	// build container was OOMKilled, so the reason is visible in `kubectl` /
	// the conditions table — not just status.build.termination. It is bound to
	// TerminationReasonOOMKilled (the container reason detection keys on) so the
	// two cannot drift.
	ReasonOOMKilled = TerminationReasonOOMKilled
)

// TerminationReasonOOMKilled is the container terminated reason Kubernetes sets
// when the kubelet's OOM killer reaps a build container. It is the authoritative
// OOM signal (stronger than exit code 137, which can also be a plain SIGKILL).
const TerminationReasonOOMKilled = "OOMKilled"

// ConfigMapKeySelector selects a key from a ConfigMap in the app namespace.
type ConfigMapKeySelector struct {
	// +kubebuilder:validation:Required
	Name string `json:"name"`
	// +kubebuilder:validation:Required
	Key string `json:"key"`
}

// SecretKeySelector selects a key from a Secret in the app namespace.
type SecretKeySelector struct {
	// +kubebuilder:validation:Required
	Name string `json:"name"`
	// +kubebuilder:validation:Required
	Key string `json:"key"`
}

// EnvVarSource is the inline (NO $ref) source for a public EnvVar. Only a
// ConfigMap key reference is permitted; secretKeyRef is intentionally absent so
// secrets can never leak into setup/build env via this type.
type EnvVarSource struct {
	// +optional
	ConfigMapKeyRef *ConfigMapKeySelector `json:"configMapKeyRef,omitempty"`
}

// EnvVar is a PUBLIC environment variable (literal or ConfigMap-sourced). It can
// reach the built bundle, so it must never carry secrets.
type EnvVar struct {
	// +kubebuilder:validation:Required
	Name string `json:"name"`
	// +optional
	Value string `json:"value,omitempty"`
	// +optional
	ValueFrom *EnvVarSource `json:"valueFrom,omitempty"`
}

// EnvVarWithSecretSource is the inline source for a secret-backed env var.
type EnvVarWithSecretSource struct {
	// +kubebuilder:validation:Required
	SecretKeyRef SecretKeySelector `json:"secretKeyRef"`
}

// EnvVarWithSecret is a Secret-sourced environment variable, intended for the
// FETCH phase ONLY. The operator injects these solely into the fetch container.
type EnvVarWithSecret struct {
	// +kubebuilder:validation:Required
	Name string `json:"name"`
	// +kubebuilder:validation:Required
	ValueFrom EnvVarWithSecretSource `json:"valueFrom"`
}

// PhaseSpec describes one pipeline phase container (setup / fetch / build). The
// public-env / no-secretKeyRef boundary holds STRUCTURALLY: EnvVarSource has no
// secretKeyRef field, so a CEL rule would be tautological — none is declared.
type PhaseSpec struct {
	// +optional
	Image string `json:"image,omitempty"`
	// +optional
	Command []string `json:"command,omitempty"`
	// +optional
	// +listType=atomic
	Env []EnvVar `json:"env,omitempty"`
	// RunAsUser pins this phase container's numeric UID. The build pod sets
	// runAsNonRoot WITHOUT a UID, so an image whose USER is a non-numeric name
	// (e.g. cimg/node's `circleci`) is rejected at admission — the kubelet
	// cannot verify a named user is non-root (CreateContainerConfigError). Set
	// this to the image's numeric non-root UID to satisfy the constraint. Must
	// be > 0 (non-root).
	// +kubebuilder:validation:Minimum=1
	// +optional
	RunAsUser *int64 `json:"runAsUser,omitempty"`
	// MemoryLimit is the per-phase container memory ceiling (a k8s quantity, e.g.
	// "2Gi"). When omitted the operator supplies a per-phase default (operator
	// config owns the defaults, NOT the CRD — no kubebuilder default here). The
	// memory REQUEST is always pinned equal to the limit (incompressible ⇒
	// Guaranteed QoS), so a heavy build cannot OOM the node with a low request.
	// Shared by setup/fetch/build via PhaseSpec.
	// +optional
	MemoryLimit string `json:"memoryLimit,omitempty"`
}

// BuildPhaseSpec is the build phase: a PhaseSpec plus the output directory the
// copier publishes. build carries more than setup/fetch, so it has its own type
// (setup/fetch stay plain PhaseSpec).
// +kubebuilder:validation:XValidation:rule="!has(self.outputDir) || self.outputDir.split('/').all(s, s != '' && s != '.' && s != '..')",message="build.outputDir must be a relative path with no empty, '.' or '..' segments"
type BuildPhaseSpec struct {
	PhaseSpec `json:",inline"`
	// OutputDir is the subdir of the workspace holding the built bundle (the
	// copier's OUTPUT_DIR; defaults to "dist" when empty). Must be a safe relative
	// path. Two layers: the RE2 pattern restricts the character set (rejecting
	// spaces/shell metachars and a leading "/"), and a CEL rule on the spec
	// rejects any empty, "." or ".." path SEGMENT (RE2 has no lookaround and can't
	// do a per-segment check). The segment rule also blocks the "." whole-dir
	// footgun (which would publish the entire workspace) and trailing/duplicate
	// slashes, while still allowing dotted names like "assets..min".
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9_.][a-zA-Z0-9_./-]*$`
	// +kubebuilder:validation:MaxLength=256
	// +optional
	OutputDir string `json:"outputDir,omitempty"`
}

// FetchPhaseSpec is the fetch phase: a PhaseSpec plus the Secret-sourced env it
// alone may consume. Secrets live here (not spec-wide) so the "secrets are
// fetch-only" boundary is STRUCTURAL — no other phase type can carry them.
// +kubebuilder:validation:XValidation:rule="!has(self.secrets) || size(self.secrets) == 0 || (has(self.command) && size(self.command) > 0)",message="secrets require a fetch.command to consume them"
type FetchPhaseSpec struct {
	PhaseSpec `json:",inline"`
	// Secrets are Secret-sourced env injected into the FETCH phase ONLY.
	// +optional
	// +listType=atomic
	Secrets []EnvVarWithSecret `json:"secrets,omitempty"`
}

// PhasesSpec is the ordered build pipeline: setup → fetch → build. setup and
// fetch are optional (an app may install/fetch nothing); build is required (it
// produces the served bundle). The operator runs them in this fixed order
// regardless of map/field order.
type PhasesSpec struct {
	// +optional
	Setup PhaseSpec `json:"setup,omitempty"`
	// +optional
	Fetch FetchPhaseSpec `json:"fetch,omitempty"`
	// +kubebuilder:validation:Required
	Build BuildPhaseSpec `json:"build"`
}

// PipelineSpec groups HOW the app is built: the toolchain (nodeVersion /
// packageManager / submodules), the whole-pipeline timeout, and the ordered
// phases. It deliberately excludes source identity (repo/ref) and scheduling,
// which stay top-level on the spec.
// +kubebuilder:validation:XValidation:rule="has(self.phases.build.command) && size(self.phases.build.command) > 0",message="pipeline.phases.build.command is required"
// +kubebuilder:validation:XValidation:rule="has(self.nodeVersion) || has(self.phases.build.image)",message="build needs an image: set nodeVersion or build.image under pipeline"
type PipelineSpec struct {
	// NodeVersion selects an operator-managed node toolchain by MAJOR version
	// (e.g. 18). The operator resolves it to a digest-pinned image + numeric UID +
	// writable HOME, so the app need not set image/runAsUser. A phase may still
	// override with its own image (fully BYO for that phase). Available majors are
	// operator/chart config; an unknown version fails the app at reconcile. Omit
	// to supply build.image yourself.
	// +kubebuilder:validation:Minimum=1
	// +optional
	NodeVersion int `json:"nodeVersion,omitempty"`
	// +kubebuilder:default=yarn
	// +optional
	PackageManager PackageManager `json:"packageManager,omitempty"`
	// +optional
	Submodules bool `json:"submodules,omitempty"`

	// Timeout bounds the WHOLE build pipeline (all phases) as a Go duration
	// string (e.g. "1h", "90m", "1h30m"; max unit is hours — no days). When unset
	// or zero the operator supplies the default from its config (NO kubebuilder
	// default here — operator config owns it).
	// +optional
	Timeout metav1.Duration `json:"timeout,omitempty"`

	// +kubebuilder:validation:Required
	Phases PhasesSpec `json:"phases"`
}

// PackageManager selects the JS package manager (drives the volume layout).
// +kubebuilder:validation:Enum=yarn;pnpm
type PackageManager string

const (
	PackageManagerYarn PackageManager = "yarn"
	PackageManagerPnpm PackageManager = "pnpm"
)

// TLSConfig configures TLS termination at the Ingress.
type TLSConfig struct {
	// +kubebuilder:validation:Required
	SecretName string `json:"secretName"`
}

// IngressConfig describes the public ingress for the served bundle.
type IngressConfig struct {
	// +optional
	ClassName *string `json:"className,omitempty"`
	// +kubebuilder:validation:Required
	Host string `json:"host"`
	// +optional
	TLS *TLSConfig `json:"tls,omitempty"`
}

// AuthSecretRef points at a Secret key holding a bcrypt/htpasswd line.
type AuthSecretRef struct {
	// +kubebuilder:validation:Required
	Name string `json:"name"`
	// +kubebuilder:validation:Required
	Key string `json:"key"`
}

// AuthConfig configures optional HTTP basic auth. Exactly one of passwordHash or
// secretRef must be set.
// +kubebuilder:validation:XValidation:rule="(has(self.passwordHash) ? 1 : 0) + (has(self.secretRef) ? 1 : 0) == 1",message="auth requires exactly one of passwordHash or secretRef"
type AuthConfig struct {
	// +optional
	PasswordHash *string `json:"passwordHash,omitempty"`
	// +optional
	SecretRef *AuthSecretRef `json:"secretRef,omitempty"`
}

// CacheThresholds are absolute-byte thresholds for the regenerable cache volume.
type CacheThresholds struct {
	// +optional
	CleanupBytes int64 `json:"cleanupBytes,omitempty"`
	// +optional
	AlertBytes int64 `json:"alertBytes,omitempty"`
}

// DataCacheThresholds adds a per-run delta budget on top of cache thresholds.
type DataCacheThresholds struct {
	// +optional
	CleanupBytes int64 `json:"cleanupBytes,omitempty"`
	// +optional
	AlertBytes int64 `json:"alertBytes,omitempty"`
	// +optional
	RunDeltaBytes int64 `json:"runDeltaBytes,omitempty"`
}

// OutputThresholds bound the served-bundle volume.
type OutputThresholds struct {
	// +optional
	AlertBytes int64 `json:"alertBytes,omitempty"`
	// +optional
	CapBytes int64 `json:"capBytes,omitempty"`
}

// NodeStorage describes node-level headroom expectations.
type NodeStorage struct {
	// +optional
	FreeSpaceHeadroomBytes int64 `json:"freeSpaceHeadroomBytes,omitempty"`
}

// StorageConfig groups the per-volume absolute-byte thresholds. The operator
// also calls domain.ValidateStorage at reconcile time (cleanup < alert < cap).
// +kubebuilder:validation:XValidation:rule="!has(self.cache) || !has(self.cache.cleanupBytes) || !has(self.cache.alertBytes) || self.cache.cleanupBytes < self.cache.alertBytes",message="cache.cleanupBytes must be < cache.alertBytes"
// +kubebuilder:validation:XValidation:rule="!has(self.dataCache) || !has(self.dataCache.cleanupBytes) || !has(self.dataCache.alertBytes) || self.dataCache.cleanupBytes < self.dataCache.alertBytes",message="dataCache.cleanupBytes must be < dataCache.alertBytes"
// +kubebuilder:validation:XValidation:rule="!has(self.output) || !has(self.output.alertBytes) || !has(self.output.capBytes) || self.output.alertBytes < self.output.capBytes",message="output.alertBytes must be < output.capBytes"
type StorageConfig struct {
	// +optional
	Cache CacheThresholds `json:"cache,omitempty"`
	// +optional
	DataCache DataCacheThresholds `json:"dataCache,omitempty"`
	// +optional
	Output OutputThresholds `json:"output,omitempty"`
	// +optional
	Node NodeStorage `json:"node,omitempty"`
}

// FrontendAppSpec is the desired state: operational tunables for one app.
type FrontendAppSpec struct {
	// +kubebuilder:validation:Required
	Repo string `json:"repo"`
	// +kubebuilder:default="HEAD"
	// +optional
	Ref string `json:"ref,omitempty"`

	// +kubebuilder:default="0 */12 * * *"
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// Pipeline is HOW the app is built: toolchain, timeout, and the ordered
	// setup/fetch/build phases. Required — every app must build something.
	// +kubebuilder:validation:Required
	Pipeline PipelineSpec `json:"pipeline"`

	// +kubebuilder:default=0
	// +optional
	KeepReleases int `json:"keepReleases,omitempty"`

	// +kubebuilder:validation:Required
	Ingress IngressConfig `json:"ingress"`
	// +optional
	Auth *AuthConfig `json:"auth,omitempty"`

	// +optional
	Storage StorageConfig `json:"storage,omitempty"`
}

// BuildResult is the terminal outcome of a build pod.
type BuildResult string

const (
	BuildResultSucceeded BuildResult = "Succeeded"
	BuildResultFailed    BuildResult = "Failed"
	// BuildResultAborted is a build that did not run to a clean pass/fail: it
	// was superseded, evicted, or otherwise terminated mid-flight.
	BuildResultAborted BuildResult = "Aborted"
)

// StepStatus is the state of one ordered step in a build's pipeline. Pending
// renders greyed (not yet reached); the others map to their obvious icons.
type StepStatus string

const (
	StepStatusPending   StepStatus = "Pending"
	StepStatusRunning   StepStatus = "Running"
	StepStatusSucceeded StepStatus = "Succeeded"
	StepStatusFailed    StepStatus = "Failed"
	StepStatusAborted   StepStatus = "Aborted"
)

// Canonical pipeline step names, in flow order. setup and fetch are emitted
// only when the app defines those phases; release is SYNTHETIC — it is not a
// build container but the operator's release-pointer flip after copier succeeds.
const (
	StepClone   = "clone"
	StepSetup   = "setup"
	StepFetch   = "fetch"
	StepBuild   = "build"
	StepCopier  = "copier"
	StepRelease = "release"
)

// BuildStep is one entry of a build's ordered per-step timeline. Only steps
// that actually apply to the app appear (no phantom skipped slots).
type BuildStep struct {
	// +kubebuilder:validation:Required
	Name string `json:"name"`
	// +kubebuilder:validation:Required
	Status StepStatus `json:"status"`
	// +optional
	Message string `json:"message,omitempty"`
}

// BuildTrigger records why a build ran, for the history list.
type BuildTrigger string

const (
	BuildTriggerScheduled  BuildTrigger = "Scheduled"
	BuildTriggerManual     BuildTrigger = "Manual"
	BuildTriggerSpecChange BuildTrigger = "SpecChange"
)

// BuildPhase is the lifecycle of the current/last build pod.
type BuildPhase string

const (
	BuildPhasePending  BuildPhase = "Pending"
	BuildPhaseRunning  BuildPhase = "Running"
	BuildPhaseComplete BuildPhase = "Complete"
)

// BuildStatus is the unified per-build record. It mirrors the current/last
// build Job in status.build, and the SAME shape is reused for every entry of
// status.buildHistory — one type, one renderer.
type BuildStatus struct {
	// +optional
	Phase BuildPhase `json:"phase,omitempty"`
	// +optional
	Result BuildResult `json:"result,omitempty"`
	// +optional
	JobName string `json:"jobName,omitempty"`
	// PodName is the build pod for this Job, persisted so the read-only console
	// (which can get but not list pods) can fetch logs, and so a Loki query can
	// be scoped by pod label.
	// +optional
	PodName string `json:"podName,omitempty"`
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`
	// +optional
	Attempts int `json:"attempts,omitempty"`
	// Trigger records why this build ran.
	// +optional
	Trigger BuildTrigger `json:"trigger,omitempty"`
	// TriggeredBy is the user who requested a manual build (empty for scheduled).
	// +optional
	TriggeredBy string `json:"triggeredBy,omitempty"`
	// Steps is the ordered per-step timeline (only applicable steps).
	// +optional
	// +listType=atomic
	Steps []BuildStep `json:"steps,omitempty"`
	// FailedStep names the step whose failure ended the build, when Result is
	// Failed or Aborted.
	// +optional
	FailedStep string `json:"failedStep,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// +optional
	LogsRef string `json:"logsRef,omitempty"`
	// Termination records how a build container abnormally terminated (currently
	// OOMKilled), captured from the failed pod's container state at terminal
	// observe so the fact survives the pod being reaped. Nil unless a container
	// terminated with a non-empty reason.
	// +optional
	Termination *BuildTermination `json:"termination,omitempty"`
}

// BuildTermination captures a build container's abnormal termination, derived
// once at terminal observe from the failed pod's container state and persisted
// on status.build (so it survives the pod being evicted/reaped). OOMKilled is
// the first case; the shape generalizes to any container terminated reason.
type BuildTermination struct {
	// Reason is the container's terminated reason (e.g. "OOMKilled").
	// +optional
	Reason string `json:"reason,omitempty"`
	// Container is the build step/container that terminated (e.g. "build").
	// +optional
	Container string `json:"container,omitempty"`
	// ExitCode is the container's exit code (137 for an OOM kill).
	// +optional
	ExitCode int32 `json:"exitCode,omitempty"`
	// MemoryLimit is the memory limit that container ran with, as the Kubernetes
	// quantity string it was configured with (e.g. "512Mi"). Read from the pod
	// spec so it reflects the build that actually ran, not a later spec edit.
	// +optional
	MemoryLimit string `json:"memoryLimit,omitempty"`
	// FinishedAt is when the container terminated.
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
}

// ReleaseStatus tracks the served release pointers.
type ReleaseStatus struct {
	// +optional
	Current string `json:"current,omitempty"`
	// +optional
	Previous string `json:"previous,omitempty"`
	// +optional
	ServingSince *metav1.Time `json:"servingSince,omitempty"`
}

// StorageStatus records the most recent du measurement.
type StorageStatus struct {
	// +optional
	MeasuredAt *metav1.Time `json:"measuredAt,omitempty"`
	// +optional
	Sizes map[string]int64 `json:"sizes,omitempty"`
	// +optional
	LastRunDeltas map[string]int64 `json:"lastRunDeltas,omitempty"`
	// +optional
	ThresholdState string `json:"thresholdState,omitempty"`
}

// CleanupActionStatus is the per-action record for one cleanup kind (cache or
// release prune). Phase tracks the lifecycle of the helper pod; RequestedAt
// mirrors the triggering annotation so the operator can detect a fresh request.
type CleanupActionStatus struct {
	// RequestedAt mirrors the cleanup request annotation's token.
	// +optional
	RequestedAt string `json:"requestedAt,omitempty"`
	// RequestedBy is the user who requested the cleanup.
	// +optional
	RequestedBy string `json:"requestedBy,omitempty"`
	// Phase is the lifecycle of the cleanup helper: Pending|Running|Succeeded|Failed.
	// +optional
	Phase string `json:"phase,omitempty"`
	// LastCompleted is when the cleanup helper last finished.
	// +optional
	LastCompleted string `json:"lastCompleted,omitempty"`
	// ReclaimedBytes is the space reclaimed by the last completed cleanup.
	// +optional
	ReclaimedBytes int64 `json:"reclaimedBytes,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
}

// CleanupStatus groups the per-action cleanup records. Each is operator-written
// in response to a cleanup-cache / cleanup-releases annotation request.
type CleanupStatus struct {
	// +optional
	Cache *CleanupActionStatus `json:"cache,omitempty"`
	// +optional
	Releases *CleanupActionStatus `json:"releases,omitempty"`
}

// ManualTrigger records the last manual rebuild request.
type ManualTrigger struct {
	// +optional
	TriggeredBy string `json:"triggeredBy,omitempty"`
	// +optional
	Time *metav1.Time `json:"time,omitempty"`
}

// FrontendAppStatus is the operator-owned observed state.
type FrontendAppStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	Phase Phase `json:"phase,omitempty"`
	// +optional
	NodeName string `json:"nodeName,omitempty"`
	// +optional
	URL string `json:"url,omitempty"`
	// +optional
	SpecStale bool `json:"specStale,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// +optional
	Build BuildStatus `json:"build,omitempty"`

	// BuildHistory is a newest-first ring buffer of recent terminal builds
	// (Jobs that ran). The operator caps it; CEL bounds it defensively.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=5
	BuildHistory []BuildStatus `json:"buildHistory,omitempty"`

	// +optional
	LastProcessedRebuild string `json:"lastProcessedRebuild,omitempty"`
	// +optional
	LastBuiltSpecHash string `json:"lastBuiltSpecHash,omitempty"`

	// +optional
	LastBuildTime *metav1.Time `json:"lastBuildTime,omitempty"`
	// +optional
	LastSuccessfulBuildTime *metav1.Time `json:"lastSuccessfulBuildTime,omitempty"`
	// +optional
	NextScheduledBuildTime *metav1.Time `json:"nextScheduledBuildTime,omitempty"`

	// +optional
	Release ReleaseStatus `json:"release,omitempty"`
	// +optional
	Storage StorageStatus `json:"storage,omitempty"`
	// +optional
	LastManualTrigger ManualTrigger `json:"lastManualTrigger,omitempty"`

	// Cleanup records the per-action cleanup state (cache prune / release prune).
	// +optional
	Cleanup *CleanupStatus `json:"cleanup,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=fapp
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.url`
// +kubebuilder:printcolumn:name="Stale",type=boolean,JSONPath=`.status.specStale`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// FrontendApp is the Schema for the frontendapps API.
type FrontendApp struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FrontendAppSpec   `json:"spec,omitempty"`
	Status FrontendAppStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FrontendAppList contains a list of FrontendApp.
type FrontendAppList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FrontendApp `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FrontendApp{}, &FrontendAppList{})
}
