package controller

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	ctrlreconcile "sigs.k8s.io/controller-runtime/pkg/reconcile"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
	"github.com/toggle-corp/toggle-web-baker/internal/domain"
	"github.com/toggle-corp/toggle-web-baker/internal/observability"
)

// +kubebuilder:rbac:groups=baker.toggle-corp.com,resources=frontendapps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=baker.toggle-corp.com,resources=frontendapps/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=baker.toggle-corp.com,resources=frontendapps/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=cronjobs;jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims;services;configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=persistentvolumes,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses;networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=traefik.io,resources=middlewares,verbs=get;list;watch;create;update;patch;delete

// FrontendAppReconciler reconciles a FrontendApp object.
type FrontendAppReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Config OperatorConfig

	// StorageClassName is the WaitForFirstConsumer SC backing all three PVCs.
	StorageClassName string
	// TraefikNamespace is the namespace of the Traefik controller (for the nginx
	// NetworkPolicy ingress rule).
	TraefikNamespace string

	// Clock is injected for testability (defaults to time.Now).
	Clock func() time.Time

	// Sentry reports platform-fault terminal failures. A nil Reporter is a
	// fully disabled reporter (all methods are nil-receiver-safe).
	Sentry *observability.Reporter

	// Metrics records the FrontendApp metric set. The Recorder's zero value is
	// fully usable (it lazily initializes its state under its own mutex).
	Metrics Recorder
}

func (r *FrontendAppReconciler) now() time.Time {
	if r.Clock != nil {
		return r.Clock()
	}
	return time.Now()
}

// Reconcile is the controller entrypoint.
func (r *FrontendAppReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	app := &bakerv1alpha1.FrontendApp{}
	if err := r.Get(ctx, req.NamespacedName, app); err != nil {
		if apierrors.IsNotFound(err) {
			r.Metrics.ForgetApp(req.Namespace, req.Name)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Deletion: run the bounded best-effort finalizer.
	if !app.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, app)
	}
	if !controllerutil.ContainsFinalizer(app, bakerv1alpha1.FinalizerName) {
		controllerutil.AddFinalizer(app, bakerv1alpha1.FinalizerName)
		if err := r.Update(ctx, app); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Export the app's series from its as-loaded status BEFORE any validation
	// or infra step can error out, so a persistently erroring reconcile still
	// leaves the app visible to alerting instead of blind (no series at all).
	// The step-11 / fail() records below overwrite this with the fresh status.
	r.Metrics.RecordApp(app, r.buildDeadlineSeconds(app), alertThresholdsFrom(app))

	app.Status.ObservedGeneration = app.Generation

	// 1. Validate operator config (mandatory cluster CIDRs etc.).
	if err := r.Config.Validate(); err != nil {
		return r.fail(ctx, app, bakerv1alpha1.ReasonConfigError, err.Error())
	}

	// 2. Registry allowlist: reject disallowed phase images (reconcile-time).
	if err := domain.CheckImagesAllowed(r.Config.RegistryAllowlist, phaseImages(app)); err != nil {
		return r.fail(ctx, app, bakerv1alpha1.ReasonImageNotAllowed, err.Error())
	}

	// 2b. nodeVersion must resolve against the operator's node-image map. The map
	// is admin/chart config (not spec), so this cannot be a CEL rule; the message
	// routes the fix to a cluster admin rather than implying a spec edit.
	if err := r.validateNodeVersion(app); err != nil {
		return r.fail(ctx, app, bakerv1alpha1.ReasonUnknownNodeVersion, err.Error())
	}

	// 3. Storage threshold ordering (operator-side, mirrors the CEL markers).
	if err := domain.ValidateStorage(storageConfigFrom(app)); err != nil {
		return r.fail(ctx, app, bakerv1alpha1.ReasonInvalidStorage, err.Error())
	}

	// 4. StorageClass must be WaitForFirstConsumer.
	if err := r.validateStorageClass(ctx); err != nil {
		return r.fail(ctx, app, bakerv1alpha1.ReasonInvalidStorageClass, err.Error())
	}

	// 5. specStale (DETECT-ONLY).
	current := buildSpecFrom(app)
	app.Status.SpecStale = domain.IsStale(current, app.Status.LastBuiltSpecHash)

	// 6. Ensure infra children (PVCs, build args CM, clock RBAC+CronJob,
	// NetworkPolicies). nginx + Service + Ingress are created ONLY after the
	// first successful deploy.
	if err := r.ensureInfra(ctx, app); err != nil {
		return ctrl.Result{}, err
	}

	// 7. First-build bootstrap: seed the rebuild annotation while awaiting the
	// first build and no token exists yet, so the first build fires immediately.
	if r.phaseOf(app) == bakerv1alpha1.PhaseAwaitingFirstBuild &&
		app.Annotations[bakerv1alpha1.RebuildAnnotation] == "" {
		if err := r.seedRebuild(ctx, app); err != nil {
			return ctrl.Result{}, err
		}
		// Requeue: the annotation update re-triggers reconcile.
		return ctrl.Result{Requeue: true}, nil
	}

	// 8. Build decision via the domain chokepoint.
	requested := app.Annotations[bakerv1alpha1.RebuildAnnotation]
	active, activeJob, err := r.buildActive(ctx, app)
	if err != nil {
		return ctrl.Result{}, err
	}
	switch domain.DecideBuild(requested, app.Status.LastProcessedRebuild, active) {
	case domain.StartBuild:
		if err := r.startBuild(ctx, app, requested); err != nil {
			return ctrl.Result{}, err
		}
		// A build is now in flight this reconcile. Reflect it so cleanup (9c)
		// serializes behind it; `active` was sampled before the Create and would
		// otherwise let a cleanup Job race the freshly started build pod.
		active = true
	case domain.DeferBuild:
		logger.Info("deferring build; one already active", "activeJob", activeJob)
	case domain.NoBuild:
		// nothing to start
	}

	// 9. Observe in-flight / finished build and write build-derived status.
	if err := r.observeBuild(ctx, app); err != nil {
		return ctrl.Result{}, err
	}

	// 9b. Observe finished du measurement Jobs and merge cache/dataCache sizes
	// into status.storage.sizes (alongside the copier's output/outputTotal
	// entries). The harvested Jobs are GC'd only after step 11 persists the
	// recorded sizes.
	measured, err := r.observeMeasurement(ctx, app)
	if err != nil {
		return ctrl.Result{}, err
	}

	// 9b2. Refresh the provisioned PVC capacities the console draws the storage
	// fill bars against (best-effort; bound capacities change only on resize).
	r.recordPVCCapacities(ctx, app)

	// 9c. On-demand cleanup (cache prune / release prune). Observe finished
	// cleanup Jobs, then start any fresh request — serialized against the build
	// (which takes precedence) and against each other via domain.DecideCleanup.
	// Harvested Jobs are GC'd with the measure Jobs after step 11.
	cleaned, err := r.reconcileCleanup(ctx, app, active)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Recompute the storage threshold badge from the merged sizes vs spec.storage.
	// AFTER reconcileCleanup so a prune's size writeback (observeCleanup) is
	// reflected in the same reconcile rather than lagging one requeue behind.
	app.Status.Storage.ThresholdState = domain.EvaluateThresholdState(app.Status.Storage.Sizes, storageConfigFrom(app))

	// 10. nginx + Service + Ingress, ONLY after a successful deploy.
	if r.hasSucceededOnce(app) {
		if err := r.ensureServing(ctx, app); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 11. Derive phase from conditions and persist status.
	r.refreshPhase(app)
	r.Metrics.RecordApp(app, r.buildDeadlineSeconds(app), alertThresholdsFrom(app))
	if err := r.Status().Update(ctx, app); err != nil {
		return ctrl.Result{}, err
	}

	// 12. GC the harvested measure/cleanup Jobs ONLY now that their results are
	// durably in status. Deleting before the write lost the result with the Job
	// whenever the status Update conflicted: the measure debounce then blocked a
	// re-measure for a whole interval, and a cleanup action could never be
	// re-observed. Best-effort: a failed delete leaves the Job to be re-observed
	// idempotently (recordSize skips identical re-reads) and re-deleted next
	// reconcile.
	r.deleteJobs(ctx, append(measured, cleaned...))
	return ctrl.Result{RequeueAfter: 10 * time.Minute}, nil
}

// deleteJobs background-deletes the given Jobs. Best-effort — a failure never
// fails the reconcile (each caller has a re-observe path that converges on the
// next reconcile) — but non-NotFound errors are logged for visibility: while a
// harvested Job lingers, maybeStartMeasurement's Create is a silent
// AlreadyExists no-op and the cleanup same-mode hold stays engaged, so a
// persistently failing delete quietly stalls both without any other signal.
func (r *FrontendAppReconciler) deleteJobs(ctx context.Context, jobs []*batchv1.Job) {
	policy := metav1.DeletePropagationBackground
	for _, j := range jobs {
		if err := r.Delete(ctx, j, &client.DeleteOptions{PropagationPolicy: &policy}); err != nil && !apierrors.IsNotFound(err) {
			log.FromContext(ctx).Error(err, "failed to delete harvested job (will retry next reconcile)", "job", j.Name)
		}
	}
}

// ---- decision helpers (factored for fake-client unit tests) ----

// phaseImages collects the user-supplied phase images for the allowlist check.
func phaseImages(app *bakerv1alpha1.FrontendApp) []domain.PhaseImage {
	var out []domain.PhaseImage
	if app.Spec.Pipeline.Phases.Setup.Image != "" {
		out = append(out, domain.PhaseImage{Phase: "setup", Image: app.Spec.Pipeline.Phases.Setup.Image})
	}
	if app.Spec.Pipeline.Phases.Fetch.Image != "" {
		out = append(out, domain.PhaseImage{Phase: "fetch", Image: app.Spec.Pipeline.Phases.Fetch.Image})
	}
	if app.Spec.Pipeline.Phases.Build.Image != "" {
		out = append(out, domain.PhaseImage{Phase: "build", Image: app.Spec.Pipeline.Phases.Build.Image})
	}
	return out
}

// validateNodeVersion checks that a set spec.pipeline.nodeVersion resolves in the
// operator's node-image map. nodeVersion 0 (unset, BYO image) always passes.
func (r *FrontendAppReconciler) validateNodeVersion(app *bakerv1alpha1.FrontendApp) error {
	if app.Spec.Pipeline.NodeVersion == 0 {
		return nil
	}
	if _, ok := domain.LookupNodeImage(r.Config.NodeImages, app.Spec.Pipeline.NodeVersion); ok {
		return nil
	}
	known := make([]string, 0, len(r.Config.NodeImages))
	for k := range r.Config.NodeImages {
		known = append(known, k)
	}
	sort.Strings(known)
	return fmt.Errorf("nodeVersion %d is not available; known versions: %v. Ask a cluster admin to add it to the operator's node-image map (Helm values operator.nodeImages)", app.Spec.Pipeline.NodeVersion, known)
}

func storageConfigFrom(app *bakerv1alpha1.FrontendApp) domain.StorageConfig {
	s := app.Spec.Storage
	return domain.StorageConfig{
		Cache:     domain.VolumeThresholds{CleanupBytes: s.Cache.CleanupBytes, AlertBytes: s.Cache.AlertBytes},
		DataCache: domain.VolumeThresholds{CleanupBytes: s.DataCache.CleanupBytes, AlertBytes: s.DataCache.AlertBytes},
		Output:    domain.VolumeThresholds{AlertBytes: s.Output.AlertBytes, CapBytes: s.Output.CapBytes},
	}
}

// alertThresholdsFrom resolves the per-volume alertBytes thresholds the metrics
// Recorder exports, flattened through storageConfigFrom + the domain's single
// volume enumeration so the metric set cannot drift from validation when a
// volume is added.
func alertThresholdsFrom(app *bakerv1alpha1.FrontendApp) map[string]int64 {
	vols := storageConfigFrom(app).Volumes()
	out := make(map[string]int64, len(vols))
	for _, v := range vols {
		out[v.Name] = v.AlertBytes
	}
	return out
}

// envMap collapses a phase's public []EnvVar into a Name→Value map for hashing.
// ValueFrom entries collapse to "" (only literal values are captured), matching
// the prior buildArgs hashing behavior.
func envMap(in []bakerv1alpha1.EnvVar) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for _, e := range in {
		out[e.Name] = e.Value
	}
	return out
}

func buildSpecFrom(app *bakerv1alpha1.FrontendApp) domain.BuildSpec {
	var secretRefs []string
	for _, s := range app.Spec.Pipeline.Phases.Fetch.Secrets {
		secretRefs = append(secretRefs, s.ValueFrom.SecretKeyRef.Name+"/"+s.ValueFrom.SecretKeyRef.Key)
	}
	return domain.BuildSpec{
		Repo:           app.Spec.Repo,
		Ref:            app.Spec.Ref,
		PackageManager: string(app.Spec.Pipeline.PackageManager),
		NodeVersion:    app.Spec.Pipeline.NodeVersion,
		Setup:          domain.PhaseSpec{Image: app.Spec.Pipeline.Phases.Setup.Image, Command: app.Spec.Pipeline.Phases.Setup.Command, RunAsUser: app.Spec.Pipeline.Phases.Setup.RunAsUser, Env: envMap(app.Spec.Pipeline.Phases.Setup.Env)},
		Fetch:          domain.PhaseSpec{Image: app.Spec.Pipeline.Phases.Fetch.Image, Command: app.Spec.Pipeline.Phases.Fetch.Command, RunAsUser: app.Spec.Pipeline.Phases.Fetch.RunAsUser, Env: envMap(app.Spec.Pipeline.Phases.Fetch.Env)},
		Build:          domain.PhaseSpec{Image: app.Spec.Pipeline.Phases.Build.Image, Command: app.Spec.Pipeline.Phases.Build.Command, RunAsUser: app.Spec.Pipeline.Phases.Build.RunAsUser, Env: envMap(app.Spec.Pipeline.Phases.Build.Env)},
		OutputDir:      app.Spec.Pipeline.Phases.Build.OutputDir,
		SecretRefs:     secretRefs,
	}
}

// phaseOf derives the current top-level phase from conditions/status WITHOUT
// mutating the object (used for the bootstrap gate).
func (r *FrontendAppReconciler) phaseOf(app *bakerv1alpha1.FrontendApp) bakerv1alpha1.Phase {
	if !r.hasSucceededOnce(app) && app.Status.Build.Phase != bakerv1alpha1.BuildPhaseComplete {
		if app.Status.Build.Phase == bakerv1alpha1.BuildPhaseRunning || app.Status.Build.Phase == bakerv1alpha1.BuildPhasePending {
			return bakerv1alpha1.PhaseBuilding
		}
		return bakerv1alpha1.PhaseAwaitingFirstBuild
	}
	if meta := findCondition(app, bakerv1alpha1.ConditionDegraded); meta != nil && meta.Status == metav1.ConditionTrue {
		return bakerv1alpha1.PhaseDegraded
	}
	if app.Status.Build.Phase == bakerv1alpha1.BuildPhaseRunning {
		return bakerv1alpha1.PhaseBuilding
	}
	return bakerv1alpha1.PhaseReady
}

func (r *FrontendAppReconciler) hasSucceededOnce(app *bakerv1alpha1.FrontendApp) bool {
	return app.Status.LastSuccessfulBuildTime != nil || app.Status.LastBuiltSpecHash != ""
}

func findCondition(app *bakerv1alpha1.FrontendApp, t string) *metav1.Condition {
	for i := range app.Status.Conditions {
		if app.Status.Conditions[i].Type == t {
			return &app.Status.Conditions[i]
		}
	}
	return nil
}

func (r *FrontendAppReconciler) setCondition(app *bakerv1alpha1.FrontendApp, t string, status metav1.ConditionStatus, reason, msg string) {
	cond := metav1.Condition{
		Type:               t,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		LastTransitionTime: metav1.NewTime(r.now()),
		ObservedGeneration: app.Generation,
	}
	if existing := findCondition(app, t); existing != nil {
		if existing.Status == status && existing.Reason == reason {
			cond.LastTransitionTime = existing.LastTransitionTime
		}
		*existing = cond
		return
	}
	app.Status.Conditions = append(app.Status.Conditions, cond)
}

// refreshPhase recomputes the derived phase and the Ready condition.
func (r *FrontendAppReconciler) refreshPhase(app *bakerv1alpha1.FrontendApp) {
	phase := r.phaseOf(app)
	app.Status.Phase = phase
	switch phase {
	case bakerv1alpha1.PhaseReady:
		r.setCondition(app, bakerv1alpha1.ConditionReady, metav1.ConditionTrue, bakerv1alpha1.ReasonReady, "serving current release")
	case bakerv1alpha1.PhaseBuilding:
		r.setCondition(app, bakerv1alpha1.ConditionReady, metav1.ConditionFalse, bakerv1alpha1.ReasonBuilding, "build in progress")
	case bakerv1alpha1.PhaseAwaitingFirstBuild:
		r.setCondition(app, bakerv1alpha1.ConditionReady, metav1.ConditionFalse, bakerv1alpha1.ReasonAwaitingFirstBuild, "awaiting first build")
	case bakerv1alpha1.PhaseDegraded:
		// Degraded condition already set by whoever degraded it.
	}
}

// fail sets Ready=False with the given reason and persists status. Used for the
// reconcile-time rejections (config, image, storage, storageclass).
func (r *FrontendAppReconciler) fail(ctx context.Context, app *bakerv1alpha1.FrontendApp, reason, msg string) (ctrl.Result, error) {
	r.setCondition(app, bakerv1alpha1.ConditionReady, metav1.ConditionFalse, reason, msg)
	r.setCondition(app, bakerv1alpha1.ConditionDegraded, metav1.ConditionTrue, reason, msg)
	app.Status.Phase = bakerv1alpha1.PhaseDegraded
	r.Metrics.RecordApp(app, r.buildDeadlineSeconds(app), alertThresholdsFrom(app))
	if isPlatformFault(reason, "") {
		r.Sentry.CaptureTerminalFailure(observability.TerminalFailure{
			App:       app.Name,
			Namespace: app.Namespace,
			Reason:    reason,
			Message:   msg,
		})
	}
	if err := r.Status().Update(ctx, app); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

// ---- storage class validation ----

func (r *FrontendAppReconciler) validateStorageClass(ctx context.Context) error {
	if r.StorageClassName == "" {
		return fmt.Errorf("operator StorageClassName is not configured")
	}
	sc := &storagev1.StorageClass{}
	if err := r.Get(ctx, types.NamespacedName{Name: r.StorageClassName}, sc); err != nil {
		return fmt.Errorf("storageclass %q: %w", r.StorageClassName, err)
	}
	if sc.VolumeBindingMode == nil || *sc.VolumeBindingMode != storagev1.VolumeBindingWaitForFirstConsumer {
		return fmt.Errorf("storageclass %q must have volumeBindingMode=WaitForFirstConsumer", r.StorageClassName)
	}
	return nil
}

// ---- build lifecycle ----

func (r *FrontendAppReconciler) seedRebuild(ctx context.Context, app *bakerv1alpha1.FrontendApp) error {
	token := strconv.FormatInt(r.now().Unix(), 10)
	patch := client.MergeFrom(app.DeepCopy())
	if app.Annotations == nil {
		app.Annotations = map[string]string{}
	}
	app.Annotations[bakerv1alpha1.RebuildAnnotation] = token
	return r.Patch(ctx, app, patch)
}

// buildActive reports whether a build Job for this app is still running. A Job
// is active if it is neither Complete nor Failed.
func (r *FrontendAppReconciler) buildActive(ctx context.Context, app *bakerv1alpha1.FrontendApp) (bool, string, error) {
	jobs := &batchv1.JobList{}
	if err := r.List(ctx, jobs, client.InNamespace(app.Namespace), client.MatchingLabels(buildLabelsFor(app))); err != nil {
		return false, "", err
	}
	for i := range jobs.Items {
		j := &jobs.Items[i]
		if jobFinished(j) == nil {
			return true, j.Name, nil
		}
	}
	return false, "", nil
}

// startBuild records the token into status.lastProcessedRebuild and creates ONE
// build Job (single source of truth pod). It also GCs a prior failed build pod.
func (r *FrontendAppReconciler) startBuild(ctx context.Context, app *bakerv1alpha1.FrontendApp, token string) error {
	job := r.BuildJob(app, token)
	if err := controllerutil.SetControllerReference(app, job, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	// Record the processed token + build status (single-active-build invariant).
	app.Status.LastProcessedRebuild = token
	by := app.Annotations[bakerv1alpha1.RebuildByAnnotation]
	trigger := classifyTrigger(app)
	app.Status.Build = bakerv1alpha1.BuildStatus{
		Phase:     bakerv1alpha1.BuildPhasePending,
		JobName:   job.Name,
		StartTime: ptr.To(metav1.NewTime(r.now())),
		Attempts:  app.Status.Build.Attempts + 1,
		// Record why this build ran and seed the ordered step timeline as all
		// Pending; observeBuild fills in PodName + per-step statuses as the pod runs.
		Trigger:     trigger,
		TriggeredBy: by,
		Steps:       deriveBuildSteps(applicableSteps(app), nil, false),
		// Record the exact image each container was created with (digest-pinned
		// for managed toolchains), read from the Job spec itself so it reflects
		// the build that actually ran — not a later operator-config change.
		ResolvedImages: containerImages(&job.Spec.Template.Spec),
	}
	// Stamp lastManualTrigger only on a MANUAL build so it survives intervening
	// scheduled builds ("last human who rebuilt").
	if trigger == bakerv1alpha1.BuildTriggerManual {
		app.Status.LastManualTrigger = bakerv1alpha1.ManualTrigger{
			TriggeredBy: by,
			Time:        ptr.To(metav1.NewTime(r.now())),
		}
	}
	app.Status.LastBuildTime = ptr.To(metav1.NewTime(r.now()))
	return nil
}

// containerImages flattens a pod spec's init + main containers into a
// name→image map for status.build.resolvedImages.
func containerImages(pod *corev1.PodSpec) map[string]string {
	images := make(map[string]string, len(pod.InitContainers)+len(pod.Containers))
	for _, c := range pod.InitContainers {
		images[c.Name] = c.Image
	}
	for _, c := range pod.Containers {
		images[c.Name] = c.Image
	}
	return images
}

// observeBuild reads the current build Job + copier termination message and
// updates status. On copier success it records lastBuiltSpecHash. Implements the
// asymmetric retention: success -> short TTL, failure -> retained.
func (r *FrontendAppReconciler) observeBuild(ctx context.Context, app *bakerv1alpha1.FrontendApp) error {
	if app.Status.Build.JobName == "" {
		return nil
	}
	job := &batchv1.Job{}
	if err := r.Get(ctx, types.NamespacedName{Name: app.Status.Build.JobName, Namespace: app.Namespace}, job); err != nil {
		return client.IgnoreNotFound(err)
	}
	// Idempotency short-circuit: if we already observed this Job to a terminal
	// result, don't reprocess it every reconcile (which would re-stamp times,
	// re-flip conditions, and re-run applyCopierTermination needlessly).
	if app.Status.Build.JobName == job.Name &&
		app.Status.Build.Phase == bakerv1alpha1.BuildPhaseComplete &&
		(app.Status.Build.Result == bakerv1alpha1.BuildResultSucceeded ||
			app.Status.Build.Result == bakerv1alpha1.BuildResultFailed) {
		return nil
	}
	cond := jobFinished(job)
	if cond == nil {
		// In-flight: surface the live per-step timeline from the build pod (the
		// console renders status.build.steps in realtime via the pod watch).
		app.Status.Build.Phase = bakerv1alpha1.BuildPhaseRunning
		if pod := r.findBuildPod(ctx, app, job); pod != nil {
			app.Status.Build.PodName = pod.Name
			app.Status.Build.Steps = deriveBuildSteps(applicableSteps(app), pod, false)
		}
		return nil
	}
	app.Status.Build.Phase = bakerv1alpha1.BuildPhaseComplete
	app.Status.Build.CompletionTime = ptr.To(metav1.NewTime(r.now()))

	if cond.Type == batchv1.JobComplete {
		app.Status.Build.Result = bakerv1alpha1.BuildResultSucceeded
		app.Status.LastSuccessfulBuildTime = ptr.To(metav1.NewTime(r.now()))
		// Record lastBuiltSpecHash from the hash STAMPED on the Job at creation
		// (the spec the build actually ran) — never the live spec, which may have
		// been edited while the build was in flight. Fall back to recomputing from
		// the build's spec only if the stamp is somehow absent (legacy Jobs).
		if stamped := job.Annotations[bakerv1alpha1.SpecHashAnnotation]; stamped != "" {
			app.Status.LastBuiltSpecHash = stamped
		} else {
			app.Status.LastBuiltSpecHash = buildSpecFrom(app).Hash()
		}
		// specStale is recomputed from the (current) live spec vs the just-recorded
		// hash in the main reconcile loop; do NOT force it false here, or an edit
		// mid-build would be masked.
		app.Status.SpecStale = domain.IsStale(buildSpecFrom(app), app.Status.LastBuiltSpecHash)
		// Capture the prior measuredAt BEFORE applyCopierTermination refreshes it,
		// so the post-build measurement debounce sees the real last-measured time.
		prevMeasuredAt := app.Status.Storage.MeasuredAt
		r.applyCopierTermination(ctx, app, job)
		// Spawn the cache/dataCache du Jobs (best-effort: a transient create error
		// must not wedge the just-recorded build success).
		if err := r.maybeStartMeasurement(ctx, app, prevMeasuredAt); err != nil {
			log.FromContext(ctx).Error(err, "failed to start storage measurement")
		}
		r.setCondition(app, bakerv1alpha1.ConditionBuildSucceeded, metav1.ConditionTrue, bakerv1alpha1.ReasonReady, "build succeeded")
		// On success, clear any prior Degraded condition (requirement 3).
		r.setCondition(app, bakerv1alpha1.ConditionDegraded, metav1.ConditionFalse, bakerv1alpha1.ReasonReady, "build succeeded")
		// Short TTL so a succeeded Job is reaped.
		if job.Spec.TTLSecondsAfterFinished == nil {
			patch := client.MergeFrom(job.DeepCopy())
			job.Spec.TTLSecondsAfterFinished = ptr.To(int32(600))
			_ = r.Patch(ctx, job, patch)
		}
	} else {
		app.Status.Build.Result = bakerv1alpha1.BuildResultFailed
		app.Status.Build.Message = cond.Message
		app.Status.Build.LogsRef = job.Name // FAILED job retained (no TTL) for logs
		// The failure conditions are set below, AFTER the build pod is read, so an
		// OOM kill can surface its own reason (ReasonOOMKilled) rather than the
		// generic BuildFailed.
	}

	// Finalize the per-step timeline from the build pod's terminal container
	// states. The copier IS the release publisher (it assembles the release dir
	// and flips the current symlink), so the synthetic release step is Succeeded
	// exactly when the build (copier) succeeded — independent of whether the
	// copier termination message populated status.release.current.
	releaseDone := app.Status.Build.Result == bakerv1alpha1.BuildResultSucceeded
	applicable := applicableSteps(app)
	pod := r.findBuildPod(ctx, app, job)
	if pod != nil {
		app.Status.Build.PodName = pod.Name
	}
	switch {
	case pod == nil && app.Status.Build.Result == bakerv1alpha1.BuildResultSucceeded:
		// Pod already gone at terminal observe; a success means every step passed.
		app.Status.Build.Steps = allSucceeded(applicable)
	default:
		app.Status.Build.Steps = deriveBuildSteps(applicable, pod, releaseDone)
	}
	app.Status.Build.FailedStep = failedStep(app.Status.Build.Steps)

	// On failure, capture how the build container terminated (OOMKilled etc.)
	// from the pod that just finished, so the fact is persisted on status.build
	// and survives the pod being reaped. An OOM kill promotes the failure
	// conditions' reason to ReasonOOMKilled and stamps the failed step's message.
	if app.Status.Build.Result == bakerv1alpha1.BuildResultFailed {
		reason, message := bakerv1alpha1.ReasonBuildFailed, cond.Message
		if term := detectTermination(pod, app.Status.Build.FailedStep); term != nil {
			app.Status.Build.Termination = term
			if msg := terminationStepMessage(term); msg != "" {
				stampStepMessage(app.Status.Build.Steps, term.Container, msg)
			}
			if term.Reason == bakerv1alpha1.TerminationReasonOOMKilled {
				reason, message = bakerv1alpha1.ReasonOOMKilled, oomConditionMessage(term)
			}
		}
		r.setCondition(app, bakerv1alpha1.ConditionBuildSucceeded, metav1.ConditionFalse, reason, message)
		r.setCondition(app, bakerv1alpha1.ConditionDegraded, metav1.ConditionTrue, reason, message)
		// Classified AFTER the OOM promotion above so the FINAL reason decides:
		// an OOMKilled user step is the user's memory limit, while an OOMKilled
		// copier (no user-settable limit) is still a platform fault.
		if isPlatformFault(reason, app.Status.Build.FailedStep) {
			r.Sentry.CaptureTerminalFailure(observability.TerminalFailure{
				App:       app.Name,
				Namespace: app.Namespace,
				Step:      app.Status.Build.FailedStep,
				Reason:    reason,
				Message:   message,
			})
		}
	}

	r.Metrics.RecordTerminalBuild(app)

	// Append a COPY of the finalized record to the newest-first history ring
	// (deduped by JobName as a safety net against a re-observe).
	app.Status.BuildHistory = appendBuildHistory(app.Status.BuildHistory, *app.Status.Build.DeepCopy(), 5)
	return nil
}

// applyCopierTermination reads the copier container's termination message (a
// JSON blob with release/sizes) and writes it to status.
func (r *FrontendAppReconciler) applyCopierTermination(ctx context.Context, app *bakerv1alpha1.FrontendApp, job *batchv1.Job) {
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(app.Namespace), client.MatchingLabels(buildLabelsFor(app))); err != nil {
		return
	}
	// Select ONLY the copier pod owned by THIS build Job (a prior failed/retained
	// Job leaves its pods behind under the same app-wide build label). Of those,
	// pick the most recently terminated copier so we read the current build's
	// outcome rather than an arbitrary first match.
	var (
		chosen     *corev1.Pod
		chosenTerm *corev1.ContainerStateTerminated
		chosenWhen metav1.Time
	)
	for i := range pods.Items {
		p := &pods.Items[i]
		if !ownedByJob(p, job) {
			continue
		}
		for j := range p.Status.ContainerStatuses {
			cs := &p.Status.ContainerStatuses[j]
			if cs.Name != "copier" || cs.State.Terminated == nil {
				continue
			}
			finishedAt := cs.State.Terminated.FinishedAt
			if chosen == nil || finishedAt.After(chosenWhen.Time) {
				chosen = p
				chosenTerm = cs.State.Terminated
				chosenWhen = finishedAt
			}
		}
	}
	if chosen == nil || chosenTerm == nil {
		return
	}
	app.Status.NodeName = chosen.Spec.NodeName
	blob, ok := parseCopierMessage(chosenTerm.Message)
	if !ok {
		return
	}
	if blob.Release.Current != "" {
		app.Status.Release.Previous = app.Status.Release.Current
		app.Status.Release.Current = blob.Release.Current
		app.Status.Release.ServingSince = ptr.To(metav1.NewTime(r.now()))
	}
	if blob.ReleaseCount > 0 {
		app.Status.Storage.ReleaseCount = blob.ReleaseCount
	}
	if len(blob.Sizes) > 0 {
		if app.Status.Storage.Sizes == nil {
			app.Status.Storage.Sizes = map[string]int64{}
		}
		for k, v := range blob.Sizes {
			app.Status.Storage.Sizes[k] = v
		}
		// "source" (transient work-volume scratch) is no longer a tracked
		// persistent volume: the copier stopped emitting it in the sizes map. The
		// additive merge above never removes keys, so drop any leftover here to
		// self-heal CRs whose status still carries it from an older copier.
		delete(app.Status.Storage.Sizes, "source")
		app.Status.Storage.MeasuredAt = ptr.To(metav1.NewTime(r.now()))
	}
}

// findBuildPod returns the build pod owned by the given Job (the single-pod
// build), or nil if none is found yet. It lists by the app-wide build label
// (the same pattern as applyCopierTermination) and filters by owner UID so a
// prior retained Job's leftover pods are never picked up.
func (r *FrontendAppReconciler) findBuildPod(ctx context.Context, app *bakerv1alpha1.FrontendApp, job *batchv1.Job) *corev1.Pod {
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(app.Namespace), client.MatchingLabels(buildLabelsFor(app))); err != nil {
		return nil
	}
	for i := range pods.Items {
		if ownedByJob(&pods.Items[i], job) {
			return &pods.Items[i]
		}
	}
	return nil
}

// ownedByJob reports whether pod p is a child of the given build Job, matched by
// the controller ownerReference UID (the Job controller stamps this on its pods).
func ownedByJob(p *corev1.Pod, job *batchv1.Job) bool {
	for _, ref := range p.OwnerReferences {
		if ref.Controller != nil && *ref.Controller && ref.UID == job.UID {
			return true
		}
	}
	return false
}

// ---- delete / finalizer ----

// reconcileDelete runs the bounded best-effort abort of an in-flight build Job
// (delete it + its pods). It does NOT spawn a node-pinned cleanup Job (that
// would wedge on a dead node); on-disk data is reclaimed by local-path Delete.
func (r *FrontendAppReconciler) reconcileDelete(ctx context.Context, app *bakerv1alpha1.FrontendApp) (ctrl.Result, error) {
	if controllerutil.ContainsFinalizer(app, bakerv1alpha1.FinalizerName) {
		jobs := &batchv1.JobList{}
		if err := r.List(ctx, jobs, client.InNamespace(app.Namespace), client.MatchingLabels(buildLabelsFor(app))); err == nil {
			policy := metav1.DeletePropagationBackground
			for i := range jobs.Items {
				if jobFinished(&jobs.Items[i]) == nil {
					_ = r.Delete(ctx, &jobs.Items[i], &client.DeleteOptions{PropagationPolicy: &policy})
				}
			}
		}
		controllerutil.RemoveFinalizer(app, bakerv1alpha1.FinalizerName)
		if err := r.Update(ctx, app); err != nil {
			return ctrl.Result{}, err
		}
		r.Metrics.ForgetApp(app.Namespace, app.Name)
	}
	return ctrl.Result{}, nil
}

// jobFinished returns the terminal JobCondition (Complete or Failed) if the Job
// is finished, else nil.
func jobFinished(j *batchv1.Job) *batchv1.JobCondition {
	for i := range j.Status.Conditions {
		c := &j.Status.Conditions[i]
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue {
			return c
		}
	}
	return nil
}

// mapBuildPodToApp maps a build pod to a reconcile request for its owning
// FrontendApp, so the operator re-reconciles (and re-derives status.build.steps)
// the instant a build pod's container states change — realtime step updates
// without polling. Build pods are Job-owned (not app-owned), so this can't use
// Owns(&Pod{}); it keys off the build-role + instance labels on the pod. Any
// non-build pod (or non-pod object) maps to nothing.
// isBuildPod is the watch predicate: only build-role pods are relevant to the
// reconciler, so all other pod events are dropped before they reach the queue.
func isBuildPod(obj client.Object) bool {
	return obj.GetLabels()["baker.toggle-corp.com/role"] == "build"
}

func mapBuildPodToApp(_ context.Context, obj client.Object) []ctrlreconcile.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}
	if pod.Labels["baker.toggle-corp.com/role"] != "build" {
		return nil
	}
	name := pod.Labels["app.kubernetes.io/instance"]
	if name == "" {
		return nil
	}
	return []ctrlreconcile.Request{{NamespacedName: types.NamespacedName{Name: name, Namespace: pod.Namespace}}}
}

// SetupWithManager wires the controller and its owned children.
func (r *FrontendAppReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Clock == nil {
		r.Clock = time.Now
	}
	r.Config.Defaults()
	return ctrl.NewControllerManagedBy(mgr).
		For(&bakerv1alpha1.FrontendApp{}).
		Owns(&batchv1.Job{}).
		Owns(&batchv1.CronJob{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&appsv1.Deployment{}).
		Owns(&networkingv1.Ingress{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		// Build pods are Job-owned, not app-owned, so we can't Owns(&Pod{}); watch
		// them and map back to the owning app via the build labels for realtime
		// per-step status updates. The predicate drops the bulk of cluster pod
		// events (only build-role pods enqueue) so unrelated pod churn never wakes
		// the reconciler.
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(mapBuildPodToApp),
			builder.WithPredicates(predicate.NewPredicateFuncs(isBuildPod))).
		Complete(r)
}
