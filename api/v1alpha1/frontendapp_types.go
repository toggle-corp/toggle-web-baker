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

	// FinalizerName guards a best-effort abort of an in-flight build Job on delete.
	FinalizerName = "baker.toggle-corp.com/finalizer"

	// SpecHashAnnotation stamps the build-relevant spec hash onto the build Job at
	// CREATION time, so on success the operator records the hash of the spec the
	// build ACTUALLY ran — not the (possibly edited) live spec at observe-time.
	SpecHashAnnotation = "baker.toggle-corp.com/spec-hash"
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
	ReasonInvalidStorage      = "InvalidStorage"
	ReasonInvalidSpec         = "InvalidSpec"
	ReasonMissingTLSSecret    = "MissingTLSSecret"
	ReasonConfigError         = "ConfigError"
)

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

// BuildResources configures the build container's resource constraints.
type BuildResources struct {
	// +kubebuilder:default="6Gi"
	// +optional
	MemoryLimit string `json:"memoryLimit,omitempty"`
	// +optional
	CPURequest string `json:"cpuRequest,omitempty"`
	// +optional
	MemoryRequest string `json:"memoryRequest,omitempty"`
}

// ResourcesConfig groups resource and deadline tunables.
type ResourcesConfig struct {
	// +optional
	Build BuildResources `json:"build,omitempty"`
	// +kubebuilder:default=1800
	// +optional
	ActiveDeadlineSeconds int64 `json:"activeDeadlineSeconds,omitempty"`
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
// +kubebuilder:validation:XValidation:rule="has(self.build) && has(self.build.command) && size(self.build.command) > 0",message="build.command is required"
// +kubebuilder:validation:XValidation:rule="!has(self.secrets) || size(self.secrets) == 0 || (has(self.fetch) && has(self.fetch.command) && size(self.fetch.command) > 0)",message="secrets require a fetch.command to consume them"
type FrontendAppSpec struct {
	// +kubebuilder:validation:Required
	Repo string `json:"repo"`
	// +kubebuilder:default="HEAD"
	// +optional
	Ref string `json:"ref,omitempty"`
	// +kubebuilder:default=yarn
	// +optional
	PackageManager PackageManager `json:"packageManager,omitempty"`
	// +optional
	Submodules bool `json:"submodules,omitempty"`

	// +kubebuilder:default="0 */12 * * *"
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// +optional
	Setup PhaseSpec `json:"setup,omitempty"`
	// +optional
	Fetch PhaseSpec `json:"fetch,omitempty"`
	// +optional
	Build PhaseSpec `json:"build,omitempty"`

	// +optional
	OutputDir string `json:"outputDir,omitempty"`
	// +kubebuilder:default=0
	// +optional
	KeepReleases int `json:"keepReleases,omitempty"`

	// BuildArgs are PUBLIC, ConfigMap-sourced env that may reach the bundle.
	// +optional
	// +listType=atomic
	BuildArgs []EnvVar `json:"buildArgs,omitempty"`

	// Secrets are Secret-sourced env injected into the FETCH phase ONLY.
	// +optional
	// +listType=atomic
	Secrets []EnvVarWithSecret `json:"secrets,omitempty"`

	// +kubebuilder:validation:Required
	Ingress IngressConfig `json:"ingress"`
	// +optional
	Auth *AuthConfig `json:"auth,omitempty"`

	// +optional
	Resources ResourcesConfig `json:"resources,omitempty"`
	// +optional
	Storage StorageConfig `json:"storage,omitempty"`
}

// BuildResult is the terminal outcome of a build pod.
type BuildResult string

const (
	BuildResultSucceeded BuildResult = "Succeeded"
	BuildResultFailed    BuildResult = "Failed"
)

// BuildPhase is the lifecycle of the current/last build pod.
type BuildPhase string

const (
	BuildPhasePending  BuildPhase = "Pending"
	BuildPhaseRunning  BuildPhase = "Running"
	BuildPhaseComplete BuildPhase = "Complete"
)

// BuildStatus mirrors the current/last build Job.
type BuildStatus struct {
	// +optional
	Phase BuildPhase `json:"phase,omitempty"`
	// +optional
	Result BuildResult `json:"result,omitempty"`
	// +optional
	JobName string `json:"jobName,omitempty"`
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`
	// +optional
	Attempts int `json:"attempts,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// +optional
	LogsRef string `json:"logsRef,omitempty"`
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
	DataFreshness string `json:"dataFreshness,omitempty"`
	// +optional
	Release ReleaseStatus `json:"release,omitempty"`
	// +optional
	Storage StorageStatus `json:"storage,omitempty"`
	// +optional
	LastManualTrigger ManualTrigger `json:"lastManualTrigger,omitempty"`
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
