package controller

import (
	"context"
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
)

// CopierMessage is the JSON blob the copier (and du Jobs) emit via the container
// termination message. The operator parses it into build-derived status.
type CopierMessage struct {
	DataFreshness string `json:"dataFreshness,omitempty"`
	Release       struct {
		Current string `json:"current,omitempty"`
	} `json:"release,omitempty"`
	Sizes map[string]int64 `json:"sizes,omitempty"`
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

// upsert creates obj if absent, else patches it toward the desired state. It
// always stamps an owner reference for cascade GC.
func (r *FrontendAppReconciler) upsert(ctx context.Context, app *bakerv1alpha1.FrontendApp, obj client.Object, mutate func()) error {
	mutate()
	if err := controllerutil.SetControllerReference(app, obj, r.Scheme); err != nil {
		return err
	}
	existing := obj.DeepCopyObject().(client.Object)
	err := r.Get(ctx, client.ObjectKeyFromObject(obj), existing)
	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			return err
		}
		return r.Create(ctx, obj)
	}
	obj.SetResourceVersion(existing.GetResourceVersion())
	return r.Update(ctx, obj)
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

// ensureInfra reconciles the always-present children: PVCs, the build-args
// ConfigMap, the clock SA/Role/RoleBinding/CronJob, and the build NetworkPolicy.
func (r *FrontendAppReconciler) ensureInfra(ctx context.Context, app *bakerv1alpha1.FrontendApp) error {
	// Three PVCs (cache, dataCache, output) — WaitForFirstConsumer SC; the build
	// pod is the first consumer of all three (deterministic co-binding).
	for _, name := range []string{cacheePVCName(app), dataCachePVCName(app), outputPVCName(app)} {
		pvc := r.pvc(app, name, r.StorageClassName)
		if err := r.ensureExists(ctx, app, pvc); err != nil {
			return err
		}
	}

	// Build-env ConfigMap (public literal values materialized for the build
	// phase). buildArgs is gone; the public build-env channel is now
	// spec.build.env. ValueFrom (ConfigMap-sourced) entries are skipped — only
	// literal values are materialized here.
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: buildArgsConfigName(app), Namespace: app.Namespace, Labels: labelsFor(app)}}
	if err := r.upsert(ctx, app, cm, func() {
		data := map[string]string{}
		for _, e := range app.Spec.Build.Env {
			if e.ValueFrom == nil {
				data[e.Name] = e.Value
			}
		}
		cm.Data = data
	}); err != nil {
		return err
	}

	// Clock RBAC + CronJob (scoped to patch only this FrontendApp).
	sa := r.clockServiceAccount(app)
	if err := r.upsert(ctx, app, sa, func() {}); err != nil {
		return err
	}
	role := r.clockRole(app)
	if err := r.upsert(ctx, app, role, func() {}); err != nil {
		return err
	}
	rb := r.clockRoleBinding(app)
	if err := r.upsert(ctx, app, rb, func() {}); err != nil {
		return err
	}
	cron := r.clockCronJob(app)
	if err := r.upsert(ctx, app, cron, func() {}); err != nil {
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
