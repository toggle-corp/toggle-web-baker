package k8s

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// MetricsGVR addresses the metrics.k8s.io PodMetrics resource. The console reads
// it as unstructured via the SAME dynamic client used for the FrontendApp CRD,
// so no k8s.io/metrics module dependency is added.
var MetricsGVR = schema.GroupVersionResource{
	Group:    "metrics.k8s.io",
	Version:  "v1beta1",
	Resource: "pods",
}

// ContainerUsage is one container's live resource usage, in normalized units.
type ContainerUsage struct {
	CPUMillicores int64
	MemoryBytes   int64
}

// PodMetricser returns per-container live usage for one pod, keyed by container
// name. Returns an error if metrics.k8s.io is unavailable (metrics-server not
// installed) or the pod has no metrics yet. The real *k8s.Client satisfies it;
// tests fake it.
type PodMetricser interface {
	PodMetrics(ctx context.Context, namespace, pod string) (map[string]ContainerUsage, error)
}

var _ PodMetricser = (*Client)(nil)

// PodMetrics fetches metrics.k8s.io/v1beta1 PodMetrics for one pod as
// unstructured data and projects containers[].usage.{cpu,memory} into
// normalized millicores/bytes. Defensive: missing/mistyped fields are skipped.
func (c *Client) PodMetrics(ctx context.Context, namespace, pod string) (map[string]ContainerUsage, error) {
	obj, err := c.dyn.Resource(MetricsGVR).Namespace(namespace).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get podmetrics %s/%s: %w", namespace, pod, err)
	}
	containers, ok := obj.Object["containers"].([]any)
	if !ok {
		return nil, fmt.Errorf("podmetrics %s/%s: no containers", namespace, pod)
	}
	out := make(map[string]ContainerUsage, len(containers))
	for _, item := range containers {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		if name == "" {
			continue
		}
		usage, _ := m["usage"].(map[string]any)
		cpu, _ := usage["cpu"].(string)
		mem, _ := usage["memory"].(string)
		milli, bytes := parseUsage(cpu, mem)
		out[name] = ContainerUsage{CPUMillicores: milli, MemoryBytes: bytes}
	}
	return out, nil
}

// parseUsage turns Kubernetes quantity strings (e.g. "1500m", "2Gi",
// "2143289344") into millicores and bytes. Unparseable/empty values yield 0.
func parseUsage(cpu, mem string) (millicores, bytes int64) {
	if q, err := resource.ParseQuantity(cpu); err == nil {
		millicores = q.MilliValue()
	}
	if q, err := resource.ParseQuantity(mem); err == nil {
		bytes = q.Value()
	}
	return millicores, bytes
}
