// Package k8s wraps the dynamic client with exactly the two operations the
// console needs: list FrontendApps across namespaces, and get/patch one. It
// never imports the operator's Go types — the FrontendApp is addressed purely
// by its GroupVersionResource and read as unstructured data.
package k8s

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/toggle-corp/toggle-web-baker/console/internal/view"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
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
// clientset is the typed kubernetes client used for the pod-log capability; it
// is nil-safe (tests built via NewWithDynamic have no clientset and the pod
// methods then return an error rather than panicking).
type Client struct {
	dyn       dynamic.Interface
	clientset kubernetes.Interface
}

// FrontendAppPatcher is the narrow capability the server depends on; tests
// substitute a fake dynamic client behind it.
type FrontendAppPatcher interface {
	List(ctx context.Context) ([]view.App, error)
	Get(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error)
	RequestRebuild(ctx context.Context, namespace, name, user string) error
	RequestCleanupCache(ctx context.Context, namespace, name, user string) error
	RequestCleanupReleases(ctx context.Context, namespace, name, user string) error
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
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build clientset: %w", err)
	}
	c := NewWithDynamic(dyn)
	c.clientset = cs
	return c, nil
}

// NewWithDynamic wraps an existing dynamic client (used by tests with the fake).
// The typed clientset is left nil; pod-log methods then return an error.
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

// RequestCleanupCache merge-patches the cache-cleanup annotations; the operator
// observes requested-at and prunes the build cache. Annotations only — no Job or
// Pod is created by the console.
func (c *Client) RequestCleanupCache(ctx context.Context, namespace, name, user string) error {
	patch := cleanupPatch(view.AnnotationCleanupCacheRequestedAt, view.AnnotationCleanupCacheBy, user, view.Now())
	_, err := c.dyn.Resource(GVR).Namespace(namespace).Patch(
		ctx, name, types.MergePatchType, patch, metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("patch cleanup-cache annotation on %s/%s: %w", namespace, name, err)
	}
	return nil
}

// RequestCleanupReleases merge-patches the releases-cleanup annotations; the
// operator observes requested-at and prunes old releases. Annotations only.
func (c *Client) RequestCleanupReleases(ctx context.Context, namespace, name, user string) error {
	patch := cleanupPatch(view.AnnotationCleanupReleasesRequestedAt, view.AnnotationCleanupReleasesBy, user, view.Now())
	_, err := c.dyn.Resource(GVR).Namespace(namespace).Patch(
		ctx, name, types.MergePatchType, patch, metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("patch cleanup-releases annotation on %s/%s: %w", namespace, name, err)
	}
	return nil
}

// errNoClientset is returned by the pod methods when the Client was built
// without a typed clientset (e.g. NewWithDynamic in tests). Callers treat it
// like any other pod-read failure and fall back.
var errNoClientset = fmt.Errorf("k8s: no typed clientset configured")

// GetPod fetches a single build pod so the console can confirm a retained pod
// exists (the read-only console can get, but not list, pods).
func (c *Client) GetPod(ctx context.Context, namespace, name string) (*corev1.Pod, error) {
	if c.clientset == nil {
		return nil, errNoClientset
	}
	pod, err := c.clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get pod %s/%s: %w", namespace, name, err)
	}
	return pod, nil
}

// PodLogTail streams the last `tail` lines of one container's logs from a
// retained/live build pod. It reads the stream fully, splits into lines, and
// drops a trailing empty line. Any failure is returned so callers can fall back
// to Loki or render an "unavailable" note.
func (c *Client) PodLogTail(ctx context.Context, namespace, pod, container string, tail int64) ([]string, error) {
	if c.clientset == nil {
		return nil, errNoClientset
	}
	opts := &corev1.PodLogOptions{Container: container}
	if tail > 0 {
		opts.TailLines = &tail
	}
	req := c.clientset.CoreV1().Pods(namespace).GetLogs(pod, opts)
	stream, err := req.Stream(ctx)
	if err != nil {
		return nil, fmt.Errorf("stream logs %s/%s[%s]: %w", namespace, pod, container, err)
	}
	defer func() { _ = stream.Close() }()

	raw, err := io.ReadAll(stream)
	if err != nil {
		return nil, fmt.Errorf("read logs %s/%s[%s]: %w", namespace, pod, container, err)
	}
	lines := strings.Split(string(raw), "\n")
	// Drop a single trailing empty line (logs typically end with a newline).
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines, nil
}

// rebuildPatch builds the merge-patch body. Exposed package-internal so the
// handler test can assert the exact annotations land on the object. It also
// NULLS the commit annotation: trigger sources each clear the others' keys in
// the same patch, so a stale watcher SHA can't relabel this manual build.
func rebuildPatch(user string, now time.Time) []byte {
	body := fmt.Sprintf(
		`{"metadata":{"annotations":{%q:%q,%q:%q,%q:null}}}`,
		view.AnnotationRebuildRequestedAt, now.Format(time.RFC3339),
		view.AnnotationRebuildBy, user,
		view.AnnotationRebuildCommit,
	)
	return []byte(body)
}

// cleanupPatch builds the merge-patch body for one cleanup action, setting its
// requested-at + by annotations. Generalized over the key pair so both cache and
// releases reuse it; the console writes only annotations.
func cleanupPatch(requestedAtKey, byKey, user string, now time.Time) []byte {
	body := fmt.Sprintf(
		`{"metadata":{"annotations":{%q:%q,%q:%q}}}`,
		requestedAtKey, now.Format(time.RFC3339),
		byKey, user,
	)
	return []byte(body)
}
