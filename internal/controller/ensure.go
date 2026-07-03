package controller

import (
	"context"
	"encoding/json"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
)

// CopierMessage is the JSON blob the copier (and du Jobs) emit via the container
// termination message. The operator parses it into build-derived status.
type CopierMessage struct {
	Release struct {
		Current string `json:"current,omitempty"`
	} `json:"release,omitempty"`
	Sizes map[string]int64 `json:"sizes,omitempty"`
	// ReleaseCount is the number of release dirs retained on the output PVC,
	// counted by the copier post retention-sweep (0 = not reported / old copier).
	ReleaseCount int64 `json:"releaseCount,omitempty"`
}

func parseCopierMessage(s string) (CopierMessage, bool) {
	var m CopierMessage
	if s == "" {
		return m, false
	}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return m, false
	}
	return m, true
}

// fieldOwner is the fixed Server-Side Apply field manager for every child the
// operator converges via upsert.
const fieldOwner = "toggle-web-baker-operator"

// upsert converges obj toward the desired state via Server-Side Apply (which
// also creates it if absent). SSA — not a full-object Update — is load-bearing:
// the desired object lacks server-populated defaults, so a blind PUT always
// differed from the stored object, bumping generation/resourceVersion on every
// reconcile; the owned-child watch then re-enqueued the reconcile that issued
// the next PUT — a self-feeding hot loop. Applying an unchanged desired state
// is a server-side no-op (no write, no bump, no watch event), so steady state
// converges. It always stamps an owner reference for cascade GC.
func (r *FrontendAppReconciler) upsert(ctx context.Context, app *bakerv1alpha1.FrontendApp, obj client.Object, mutate func()) error {
	mutate()
	if err := controllerutil.SetControllerReference(app, obj, r.Scheme); err != nil {
		return err
	}
	// SSA requires apiVersion/kind on the wire; typed objects carry an empty
	// TypeMeta, so resolve the GVK from the scheme (the unstructured Traefik
	// Middleware already carries its own, which GVKForObject returns as-is).
	gvk, err := apiutil.GVKForObject(obj, r.Scheme)
	if err != nil {
		return err
	}
	obj.GetObjectKind().SetGroupVersionKind(gvk)
	// An apply is not compare-and-swap: never send a resourceVersion.
	obj.SetResourceVersion("")
	return r.Patch(ctx, obj, client.Apply, client.FieldOwner(fieldOwner), client.ForceOwnership)
}

// ensureExists creates obj if absent and otherwise leaves its (effectively
// immutable) spec untouched. Use for resources whose spec cannot change after
// creation (PVCs, Services) — blindly Updating them would wipe server-populated
// immutable fields (PVC VolumeName, Service ClusterIP). It still reconciles the
// owner reference on an already-existing object so cascade GC always reclaims
// it; ownerReferences are metadata, so stamping one never touches the spec.
func (r *FrontendAppReconciler) ensureExists(ctx context.Context, app *bakerv1alpha1.FrontendApp, obj client.Object) error {
	existing := obj.DeepCopyObject().(client.Object)
	err := r.Get(ctx, client.ObjectKeyFromObject(obj), existing)
	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			return err
		}
		if err := controllerutil.SetControllerReference(app, obj, r.Scheme); err != nil {
			return err
		}
		return r.Create(ctx, obj)
	}
	// Already exists: keep our controller owner reference current (e.g. for an
	// object created out-of-band or by a pre-owner-ref operator version) but
	// never re-Update the immutable spec.
	if ref := metav1.GetControllerOf(existing); ref != nil && ref.UID == app.UID {
		return nil
	}
	if err := controllerutil.SetControllerReference(app, existing, r.Scheme); err != nil {
		return err
	}
	return r.Update(ctx, existing)
}

// ensureInfra reconciles the always-present children: PVCs, the clock
// SA/Role/RoleBinding/CronJob, and the build NetworkPolicy.
func (r *FrontendAppReconciler) ensureInfra(ctx context.Context, app *bakerv1alpha1.FrontendApp) error {
	// Three PVCs (cache, dataCache, output) — WaitForFirstConsumer SC; the build
	// pod is the first consumer of all three (deterministic co-binding).
	for _, name := range []string{cacheePVCName(app), dataCachePVCName(app), outputPVCName(app)} {
		pvc := r.pvc(app, name, r.StorageClassName)
		if err := r.ensureExists(ctx, app, pvc); err != nil {
			return err
		}
	}

	if err := r.ensureTriggers(ctx, app); err != nil {
		return err
	}

	// Build pod NetworkPolicy (default-deny ingress; egress = DNS + public
	// minus cluster CIDRs + metadata).
	bnp := r.buildNetworkPolicy(app)
	if err := r.upsert(ctx, app, bnp, func() {}); err != nil {
		return err
	}
	return nil
}

// ensureTriggers converges the opt-in trigger children. The clock CronJob
// (scheduled builds) and the watcher CronJob (commit watch) are each rendered
// only when their spec struct is enabled, and DELETED when disabled — flipping
// a trigger off must reclaim its CronJob, not orphan it. The shared RBAC trio
// (SA/Role/RoleBinding, scoped to patch only this FrontendApp) exists while
// EITHER trigger is enabled and goes with the last one.
func (r *FrontendAppReconciler) ensureTriggers(ctx context.Context, app *bakerv1alpha1.FrontendApp) error {
	scheduledOn := app.Spec.ScheduledBuilds != nil && app.Spec.ScheduledBuilds.Enabled
	watchOn := app.Spec.WatchCommits != nil && app.Spec.WatchCommits.Enabled

	rbac := []client.Object{r.clockServiceAccount(app), r.clockRole(app), r.clockRoleBinding(app)}
	if scheduledOn || watchOn {
		for _, obj := range rbac {
			if err := r.upsert(ctx, app, obj, func() {}); err != nil {
				return err
			}
		}
	} else {
		for _, obj := range rbac {
			if err := client.IgnoreNotFound(r.Delete(ctx, obj)); err != nil {
				return err
			}
		}
	}

	if scheduledOn {
		if err := r.upsert(ctx, app, r.clockCronJob(app), func() {}); err != nil {
			return err
		}
	} else {
		gone := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: clockCronJobName(app), Namespace: app.Namespace}}
		if err := client.IgnoreNotFound(r.Delete(ctx, gone)); err != nil {
			return err
		}
	}

	if watchOn {
		watcher, err := r.watchCronJob(app)
		if err != nil {
			// An unexpressible interval (CRD-pattern-valid but not cron-mappable,
			// e.g. "90m") fails the reconcile loudly instead of silently polling
			// at a surprising rate.
			return fmt.Errorf("watchCommits: %w", err)
		}
		if err := r.upsert(ctx, app, watcher, func() {}); err != nil {
			return err
		}
	} else {
		gone := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: watchCronJobName(app), Namespace: app.Namespace}}
		if err := client.IgnoreNotFound(r.Delete(ctx, gone)); err != nil {
			return err
		}
	}
	return nil
}

// ensureServing reconciles the serving stack: nginx ConfigMap + Deployment +
// Service + Ingress + nginx NetworkPolicy + optional Traefik basic-auth
// Middleware. Created ONLY after the first successful deploy.
func (r *FrontendAppReconciler) ensureServing(ctx context.Context, app *bakerv1alpha1.FrontendApp) error {
	conf := r.nginxConfigMap(app)
	if err := r.upsert(ctx, app, conf, func() {}); err != nil {
		return err
	}
	deploy := r.nginxDeployment(app)
	if err := r.upsert(ctx, app, deploy, func() {}); err != nil {
		return err
	}
	svc := r.service(app)
	if err := r.ensureExists(ctx, app, svc); err != nil {
		return err
	}

	// Auth Middleware (optional) MUST be upserted BEFORE the Ingress that
	// references it via the router-middlewares annotation, so the annotation
	// never points at a not-yet-created CRD. Note: in production the operator
	// would also materialize a Secret from spec.auth; here we reference an
	// existing one.
	if app.Spec.Auth != nil {
		mw := r.authMiddleware(app, middlewareName(app)+"-secret")
		if err := r.upsert(ctx, app, mw, func() {}); err != nil {
			return err
		}
	}

	ing := r.ingress(app)
	if err := r.upsert(ctx, app, ing, func() {}); err != nil {
		return err
	}
	nnp := r.nginxNetworkPolicy(app, r.TraefikNamespace)
	if err := r.upsert(ctx, app, nnp, func() {}); err != nil {
		return err
	}

	// IngressReady: validate the TLS secret exists (if TLS configured).
	r.validateIngress(ctx, app)

	app.Status.URL = ingressURL(app)
	return nil
}

func ingressURL(app *bakerv1alpha1.FrontendApp) string {
	scheme := "http"
	if app.Spec.Ingress.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + app.Spec.Ingress.Host
}

// validateIngress checks the TLS secret exists and sets IngressReady. Plaintext
// + no-auth produces a status warning (Degraded=false, but a note).
func (r *FrontendAppReconciler) validateIngress(ctx context.Context, app *bakerv1alpha1.FrontendApp) {
	if app.Spec.Ingress.TLS != nil {
		secret := &corev1.Secret{}
		err := r.Get(ctx, client.ObjectKey{Namespace: app.Namespace, Name: app.Spec.Ingress.TLS.SecretName}, secret)
		if err != nil {
			r.setCondition(app, bakerv1alpha1.ConditionIngressReady, metav1.ConditionFalse, bakerv1alpha1.ReasonMissingTLSSecret,
				"TLS secret "+app.Spec.Ingress.TLS.SecretName+" not found")
			return
		}
	}
	msg := "ingress ready"
	if app.Spec.Ingress.TLS == nil && app.Spec.Auth == nil {
		msg = "WARNING: serving plaintext with no auth"
	}
	r.setCondition(app, bakerv1alpha1.ConditionIngressReady, metav1.ConditionTrue, bakerv1alpha1.ReasonReady, msg)
}
