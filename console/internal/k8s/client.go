// Package k8s wraps the dynamic client with exactly the two operations the
// console needs: list Apps across namespaces, and get/patch one. It
// never imports the operator's Go types — the App is addressed purely
// by its GroupVersionResource and read as unstructured data.
package k8s

import (
	"context"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/toggle-corp/toggle-web-baker/console/internal/view"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

// testCacheSyncTimeout bounds NewWithDynamic's warm-up. It is short because that
// constructor is the test/inline path (fake dynamic client, objects seeded
// synchronously): a completed sync is near-instant, so a miswired fake (missing
// listKind, wrong GVR) should surface fast — a warning after 5s, not a silent
// stall then an empty cache. The production New() never blocks on sync at all.
const testCacheSyncTimeout = 5 * time.Second

// staleWindow is how recently a watch error must have fired for Stale() to be
// true. It is a heuristic: while the informer's watch keeps erroring the cache
// stops receiving updates and its snapshot drifts from the cluster, so a recent
// watch error is treated as "the list may be out of date". A quiet window past
// staleWindow means the watch recovered (or the errors stopped), so we clear it.
const staleWindow = 60 * time.Second

// GVR addresses the App custom resource. The resource (plural) name is
// the lowercase-plural of the kind, matching the CRD's spec.names.plural.
var GVR = schema.GroupVersionResource{
	Group:    "baker.toggle-corp.com",
	Version:  "v1alpha1",
	Resource: "apps",
}

// Client is the dynamic-client-backed reader/patcher used by the HTTP server.
// clientset is the typed kubernetes client used for the pod-log capability; it
// is nil-safe (tests built via NewWithDynamic have no clientset and the pod
// methods then return an error rather than panicking).
//
// List reads are served from lister — a shared dynamic informer's local cache
// warmed and kept current by a background watch — so the console's frequent list
// polling never fans out to the API server. Writes (RequestRebuild/Cleanup) and
// single Get/pod reads still go direct for strong consistency.
type Client struct {
	dyn       dynamic.Interface
	clientset kubernetes.Interface

	factory  dynamicinformer.DynamicSharedInformerFactory
	informer cache.SharedIndexInformer // used for HasSynced + the watch-error handler
	lister   cache.GenericLister
	stop     chan struct{} // closed by Close to stop the informer goroutines
	// closeOnce makes Close idempotent: concurrent callers race to close(stop),
	// and a double close panics. sync.Once collapses them to a single close.
	closeOnce sync.Once

	// lastWatchErr is the time of the most recent informer watch error, guarded
	// by watchMu. Zero means no error seen. Set from the WatchErrorHandler using
	// view.Now (injectable clock) so Stale()'s window is testable. Stale() reads
	// it; both take watchMu.
	watchMu      sync.Mutex
	lastWatchErr time.Time
}

// AppPatcher is the narrow capability the server depends on; tests
// substitute a fake dynamic client behind it.
type AppPatcher interface {
	List(ctx context.Context) ([]view.App, error)
	Get(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error)
	RequestRebuild(ctx context.Context, namespace, name, user string) error
	RequestCleanupCache(ctx context.Context, namespace, name, user string) error
	RequestCleanupReleases(ctx context.Context, namespace, name, user string) error
	// Synced reports whether the informer's initial list has populated the cache
	// (List before this returns an empty, not authoritative, set). Stale reports
	// whether the watch is currently erroring, so the cached list may be out of
	// date. Both let the server render honest "warming"/"stale" states.
	Synced() bool
	Stale() bool
}

var _ AppPatcher = (*Client)(nil)

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
	c := newWithInformer(dyn)
	c.clientset = cs

	// Do NOT block on cache sync. The console must start serving immediately so
	// /healthz and the detail/pod-log routes answer even while the CRD/apiserver
	// is briefly unavailable — a blocking wait would crash-loop startup. The
	// informer warms in the background; until it does, the list page renders a
	// "warming up" state (Synced()==false) instead of a misleading empty list.
	return c, nil
}

// NewWithDynamic wraps an existing dynamic client (used by tests with the fake).
// The typed clientset is left nil; pod-log methods then return an error.
//
// It starts an informer against the passed dynamic client and blocks on cache
// sync so the first List sees a warm cache — the fake dynamic client supports
// List+Watch, so this works in tests. The signature stays (*Client) with no
// error so the many server-package callers keep compiling; a sync failure is
// therefore not surfaced here, but tests seed objects synchronously so sync
// always succeeds. The caller should Close the client when done (tests use
// t.Cleanup); a long-lived server may let the informer run until process exit.
func NewWithDynamic(dyn dynamic.Interface) *Client {
	c := newWithInformer(dyn)
	// Best-effort warm-up under a SHORT bound; tests seed synchronously so the
	// fake's initial List completes well within it. A miswired fake never syncs,
	// so log a clear warning (rather than stalling 30s then silently serving an
	// empty cache) and still return the client — its List then serves whatever
	// the cache holds, and the warning points at the cause.
	ctx, cancel := context.WithTimeout(context.Background(), testCacheSyncTimeout)
	defer cancel()
	if !c.waitForSync(ctx.Done()) {
		log.Printf("k8s: NewWithDynamic informer cache did not sync within %s "+
			"(miswired fake? check listKind/GVR); serving from an empty cache", testCacheSyncTimeout)
	}
	return c
}

// newWithInformer builds a Client with a started shared dynamic informer for GVR
// across all namespaces (resync 0 — we rely on the watch, not periodic relist).
// It does NOT wait for sync; callers decide how (and whether) to block on
// readiness.
func newWithInformer(dyn dynamic.Interface) *Client {
	factory := dynamicinformer.NewDynamicSharedInformerFactory(dyn, 0)
	gi := factory.ForResource(GVR)
	informer := gi.Informer()
	c := &Client{
		dyn:      dyn,
		factory:  factory,
		informer: informer,
		lister:   gi.Lister(),
		stop:     make(chan struct{}),
	}
	// SetWatchErrorHandler must be called BEFORE factory.Start; the handler
	// records the time of the latest watch error (used by Stale) and logs it for
	// visibility (replacing, not chaining, the default handler).
	_ = informer.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
		c.recordWatchErr()
		log.Printf("k8s: app informer watch error: %v", err)
	})
	factory.Start(c.stop)
	return c
}

// recordWatchErr stamps the time of the latest informer watch error using the
// injectable view.Now clock (so Stale()'s window is testable).
func (c *Client) recordWatchErr() {
	c.watchMu.Lock()
	c.lastWatchErr = view.Now()
	c.watchMu.Unlock()
}

// Synced reports whether the informer's initial list has populated the cache.
// The server renders a "warming up" state until this is true rather than an
// empty list (which would look like a healthy empty cluster).
func (c *Client) Synced() bool {
	return c.informer != nil && c.informer.HasSynced()
}

// Stale reports whether a watch error fired within staleWindow of now — i.e. the
// watch is currently failing so the cached list has likely stopped tracking the
// cluster. This is a HEURISTIC (watch-erroring ⇒ likely stale), not proof of
// divergence: a brief error the informer immediately recovered from can still
// read stale until the window elapses.
func (c *Client) Stale() bool {
	c.watchMu.Lock()
	last := c.lastWatchErr
	c.watchMu.Unlock()
	return recentWatchErr(last, view.Now())
}

// recentWatchErr is Stale()'s time-window predicate, factored out so it can be
// unit-tested without wiring a live informer's error path. A zero last means no
// error was ever recorded.
func recentWatchErr(last, now time.Time) bool {
	if last.IsZero() {
		return false
	}
	return now.Sub(last) < staleWindow
}

// waitForSync blocks until the informer cache is synced, or stopCh (a bounded
// timeout) or Close fires. Returns true only on a completed sync.
func (c *Client) waitForSync(stopCh <-chan struct{}) bool {
	// WaitForCacheSync takes a single stop channel; fan stopCh and c.stop (a
	// Close during startup) into it, and tear the fan-in goroutine down when the
	// wait returns so it can't leak past this call.
	done := make(chan struct{})
	fanDone := make(chan struct{})
	go func() {
		select {
		case <-stopCh:
		case <-c.stop:
		case <-fanDone:
		}
		close(done)
	}()
	synced := c.factory.WaitForCacheSync(done)
	close(fanDone)
	return synced[GVR]
}

// Close stops the informer's background goroutines. Idempotent and safe under
// concurrent callers (sync.Once): further List calls then serve whatever the
// cache last held. Tests register this via t.Cleanup to avoid leaking a
// goroutine per client.
func (c *Client) Close() {
	c.closeOnce.Do(func() {
		close(c.stop)
		if c.factory != nil {
			c.factory.Shutdown()
		}
	})
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

// List returns every App in every namespace as view models, mapped
// defensively. It reads from the informer's local cache (warmed at construction,
// kept current by the background watch) rather than the API server, so the
// console's frequent list polling costs nothing on the apiserver. Order is
// unspecified — the server re-sorts. Items that fail to project still appear with
// whatever rendered.
func (c *Client) List(_ context.Context) ([]view.App, error) {
	objs, err := c.lister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("list apps from cache: %w", err)
	}
	apps := make([]view.App, 0, len(objs))
	for _, o := range objs {
		u, ok := o.(*unstructured.Unstructured)
		if !ok {
			// Should never happen for a dynamic informer, but skip rather than
			// panic if the store ever holds an unexpected type.
			continue
		}
		apps = append(apps, view.FromUnstructured(u))
	}
	return apps, nil
}

// Get fetches a single App unstructured object.
func (c *Client) Get(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	obj, err := c.dyn.Resource(GVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get app %s/%s: %w", namespace, name, err)
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
