package controller

import (
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
)

func mustQuantity(s string) resource.Quantity {
	return resource.MustParse(s)
}

// traefikMiddlewareGVK returns the configured Traefik Middleware GVK (group is
// operator-configurable, version v1alpha1, kind Middleware).
func (r *AppReconciler) traefikMiddlewareGVK() schema.GroupVersionKind {
	group := r.Config.TraefikGroup
	if group == "" {
		group = "traefik.io"
	}
	return schema.GroupVersionKind{Group: group, Version: "v1alpha1", Kind: "Middleware"}
}

// authMiddleware builds the Traefik basicAuth Middleware as an unstructured
// object (the CRD's group is configurable, so we avoid a compile-time import).
// It references a Secret holding an htpasswd users list. The operator
// materializes that Secret from spec.auth before creating the middleware.
func (r *AppReconciler) authMiddleware(app *bakerv1alpha1.App, secretName string) *unstructured.Unstructured {
	gvk := r.traefikMiddlewareGVK()
	m := &unstructured.Unstructured{}
	m.SetGroupVersionKind(gvk)
	m.SetName(middlewareName(app))
	m.SetNamespace(app.Namespace)
	m.SetLabels(labelsFor(app))
	_ = unstructured.SetNestedMap(m.Object, map[string]any{
		"basicAuth": map[string]any{
			"secret": secretName,
		},
	}, "spec")
	return m
}
