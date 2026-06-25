// Package k8s wraps the dynamic client with exactly the two operations the
// console needs: list FrontendApps across namespaces, and get/patch one. It
// never imports the operator's Go types — the FrontendApp is addressed purely
// by its GroupVersionResource and read as unstructured data.
package k8s

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/toggle-corp/toggle-web-baker/console/internal/view"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

// GVR addresses the FrontendApp custom resource. The resource (plural) name is
// the lowercase-plural of the kind, matching the CRD's spec.names.plural.
var GVR = schema.GroupVersionResource{
	Group:    "baker.toggle-corp.com",
	Version:  "v1alpha1",
	Resource: "frontendapps",
}

// Client is the dynamic-client-backed reader/patcher used by the HTTP server.
type Client struct {
	dyn dynamic.Interface
}

// FrontendAppPatcher is the narrow capability the server depends on; tests
// substitute a fake dynamic client behind it.
type FrontendAppPatcher interface {
	List(ctx context.Context) ([]view.App, error)
	Get(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error)
	RequestRebuild(ctx context.Context, namespace, name, user string) error
}

var _ FrontendAppPatcher = (*Client)(nil)

// New builds a Client. It prefers in-cluster config (the production path,
// running as the console ServiceAccount) and falls back to a kubeconfig for
// local development.
func New() (*Client, error) {
	cfg, err := restConfig()
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build dynamic client: %w", err)
	}
	return NewWithDynamic(dyn), nil
}

// NewWithDynamic wraps an existing dynamic client (used by tests with the fake).
func NewWithDynamic(dyn dynamic.Interface) *Client {
	return &Client{dyn: dyn}
}

func restConfig() (*rest.Config, error) {
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	kubeconfig := filepath.Join(homedir.HomeDir(), ".kube", "config")
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("no in-cluster config and kubeconfig %q failed: %w", kubeconfig, err)
	}
	return cfg, nil
}

// List returns every FrontendApp in every namespace as view models, mapped
// defensively. Items that fail to project still appear with whatever rendered.
func (c *Client) List(ctx context.Context) ([]view.App, error) {
	ul, err := c.dyn.Resource(GVR).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list frontendapps: %w", err)
	}
	apps := make([]view.App, 0, len(ul.Items))
	for i := range ul.Items {
		apps = append(apps, view.FromUnstructured(&ul.Items[i]))
	}
	return apps, nil
}

// Get fetches a single FrontendApp unstructured object.
func (c *Client) Get(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	obj, err := c.dyn.Resource(GVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get frontendapp %s/%s: %w", namespace, name, err)
	}
	return obj, nil
}

// RequestRebuild is the console's ONLY write. It merge-patches the two rebuild
// annotations onto metadata; the operator observes requested-at and starts a
// build. No Job or Pod is ever created by the console.
func (c *Client) RequestRebuild(ctx context.Context, namespace, name, user string) error {
	patch := rebuildPatch(user, view.Now())
	_, err := c.dyn.Resource(GVR).Namespace(namespace).Patch(
		ctx, name, types.MergePatchType, patch, metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("patch rebuild annotation on %s/%s: %w", namespace, name, err)
	}
	return nil
}

// rebuildPatch builds the merge-patch body. Exposed package-internal so the
// handler test can assert the exact annotations land on the object.
func rebuildPatch(user string, now time.Time) []byte {
	body := fmt.Sprintf(
		`{"metadata":{"annotations":{%q:%q,%q:%q}}}`,
		view.AnnotationRebuildRequestedAt, now.Format(time.RFC3339),
		view.AnnotationRebuildBy, user,
	)
	return []byte(body)
}
