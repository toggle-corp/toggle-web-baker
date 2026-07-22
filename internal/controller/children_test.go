package controller

import (
	"fmt"
	"testing"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
)

const routerMiddlewaresKey = "traefik.ingress.kubernetes.io/router.middlewares"

// User-supplied spec.ingress.annotations land on the generated Ingress.
func TestIngress_UserAnnotationsPresent(t *testing.T) {
	r := reconcilerForPod()
	app := baseApp()
	app.Spec.Ingress.Annotations = map[string]string{
		"cert-manager.io/cluster-issuer": "letsencrypt",
		"custom.example.com/foo":         "bar",
	}
	ing := r.ingress(app)
	if got := ing.Annotations["cert-manager.io/cluster-issuer"]; got != "letsencrypt" {
		t.Fatalf("cert-manager annotation = %q, want letsencrypt", got)
	}
	if got := ing.Annotations["custom.example.com/foo"]; got != "bar" {
		t.Fatalf("custom annotation = %q, want bar", got)
	}
}

// With auth on, the operator-managed router.middlewares annotation is added and
// user annotations are still merged.
func TestIngress_AuthMergesUserAnnotations(t *testing.T) {
	r := reconcilerForPod()
	app := baseApp()
	app.Spec.Auth = &bakerv1alpha1.AuthConfig{PasswordHash: ptrStr("$2y$hash")}
	app.Spec.Ingress.Annotations = map[string]string{"custom.example.com/foo": "bar"}
	ing := r.ingress(app)
	wantMW := fmt.Sprintf("%s-%s@kubernetescrd", app.Namespace, middlewareName(app))
	if got := ing.Annotations[routerMiddlewaresKey]; got != wantMW {
		t.Fatalf("router.middlewares = %q, want %q", got, wantMW)
	}
	if got := ing.Annotations["custom.example.com/foo"]; got != "bar" {
		t.Fatalf("user annotation lost when auth on: got %q", got)
	}
}

// The operator-managed middleware annotation ALWAYS wins: even if a user
// somehow sets router.middlewares (validation should reject it, but defense in
// depth), the operator's value overlays it last.
func TestIngress_OperatorMiddlewareWins(t *testing.T) {
	r := reconcilerForPod()
	app := baseApp()
	app.Spec.Auth = &bakerv1alpha1.AuthConfig{PasswordHash: ptrStr("$2y$hash")}
	app.Spec.Ingress.Annotations = map[string]string{routerMiddlewaresKey: "evil@kubernetescrd"}
	ing := r.ingress(app)
	wantMW := fmt.Sprintf("%s-%s@kubernetescrd", app.Namespace, middlewareName(app))
	if got := ing.Annotations[routerMiddlewaresKey]; got != wantMW {
		t.Fatalf("router.middlewares = %q, want operator value %q (operator must win)", got, wantMW)
	}
}

// Auth off: user annotations still merge, and no router.middlewares is added.
func TestIngress_AuthOffMergesUserAnnotations(t *testing.T) {
	r := reconcilerForPod()
	app := baseApp()
	app.Spec.Ingress.Annotations = map[string]string{"custom.example.com/foo": "bar"}
	ing := r.ingress(app)
	if got := ing.Annotations["custom.example.com/foo"]; got != "bar" {
		t.Fatalf("user annotation = %q, want bar", got)
	}
	if _, ok := ing.Annotations[routerMiddlewaresKey]; ok {
		t.Fatalf("router.middlewares must be absent when auth off")
	}
}

func ptrStr(s string) *string { return &s }
