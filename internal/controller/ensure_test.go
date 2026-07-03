package controller

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
)

// upsert used a blind full-object Update toward the freshly-built desired
// object; the PUT always differed from the stored (server-defaulted) object,
// so every reconcile bumped the child's generation/resourceVersion and the
// owned-child watch re-enqueued the reconcile that issued the next PUT — an
// indefinite hot loop (observed live: the nginx Deployment's generation
// climbing ~3-7/s). With Server-Side Apply an unchanged desired state is a
// server-side no-op, so a steady-state reconcile must leave EVERY upserted
// child (typed and the unstructured Traefik Middleware alike) untouched.
func TestReconcile_SteadyStateDoesNotRewriteChildren(t *testing.T) {
	app := baseApp()
	app.Spec.Auth = &bakerv1alpha1.AuthConfig{PasswordHash: ptr.To("hash")}
	app.Spec.ScheduledBuilds = &bakerv1alpha1.ScheduledBuildsSpec{Enabled: true}
	app.Spec.WatchCommits = &bakerv1alpha1.WatchCommitsSpec{Enabled: true}
	app.Annotations = map[string]string{bakerv1alpha1.RebuildAnnotation: "tok-1"}
	app.Status.LastProcessedRebuild = "tok-1"
	app.Status.LastSuccessfulBuildTime = ptr.To(metav1.NewTime(time.Unix(900, 0)))
	app.Status.LastBuiltSpecHash = buildSpecFrom(app).Hash()
	r, cl := newReconciler(t, app, wffc())
	reconcile(t, r, app) // finalizer + children created
	reconcile(t, r, app) // settle

	middleware := &unstructured.Unstructured{}
	middleware.SetGroupVersionKind(r.traefikMiddlewareGVK())
	children := map[string]client.Object{
		clockSAName(app):      &corev1.ServiceAccount{},
		clockRoleName(app):    &rbacv1.Role{},
		clockBindingName(app): &rbacv1.RoleBinding{},
		clockCronJobName(app): &batchv1.CronJob{},
		watchCronJobName(app): &batchv1.CronJob{},
		buildNetPolName(app):  &networkingv1.NetworkPolicy{},
		nginxConfigName(app):  &corev1.ConfigMap{},
		nginxDeployName(app):  &appsv1.Deployment{},
		ingressName(app):      &networkingv1.Ingress{},
		nginxNetPolName(app):  &networkingv1.NetworkPolicy{},
		middlewareName(app):   middleware,
	}

	type revision struct {
		rv  string
		gen int64
	}
	snapshot := func() map[string]revision {
		t.Helper()
		out := map[string]revision{}
		for name, proto := range children {
			obj := proto.DeepCopyObject().(client.Object)
			if err := cl.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "apps"}, obj); err != nil {
				t.Fatalf("get child %s: %v", name, err)
			}
			out[name] = revision{rv: obj.GetResourceVersion(), gen: obj.GetGeneration()}
		}
		return out
	}

	before := snapshot()
	reconcile(t, r, app) // steady state: desired unchanged
	after := snapshot()

	for name := range children {
		if before[name] != after[name] {
			t.Errorf("child %s rewritten by a steady-state reconcile: resourceVersion/generation %+v -> %+v", name, before[name], after[name])
		}
	}
}
