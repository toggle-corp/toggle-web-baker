package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
)

// ContainerUsage is one container's live resource usage, in normalized units.
// Only memory is carried: the console's usage block dropped CPU (memory is
// what OOM-kills builds; CPU is throttled, never fatal).
type ContainerUsage struct {
	MemoryBytes int64
}

// PodMetricser returns per-container live usage for one pod, keyed by container
// name. node is the pod's spec.nodeName — the kubelet to ask. Returns an error
// when the node is unreachable or the pod has no stats yet. The real *k8s.Client
// satisfies it; tests fake it.
type PodMetricser interface {
	PodMetrics(ctx context.Context, node, namespace, pod string) (map[string]ContainerUsage, error)
}

var _ PodMetricser = (*Client)(nil)

// PodMetrics reads the pod's live usage from the KUBELET summary API
// (nodes/<node>/proxy/stats/summary), deliberately NOT from metrics.k8s.io.
// The build pod runs its steps as initContainers, and metrics-server publishes
// no PodMetrics object at all while a pod is in its init phase — then lags one
// scrape window behind once the app container starts. Verified on kind
// (2026-07): metrics.k8s.io returned 404 for the ENTIRE init phase and listed
// only the already-exited step afterwards, so the console's live-usage lookup
// could never hit. The kubelet tracks every running container — init or app —
// with seconds-fresh samples, and needs no metrics-server installed. Requires
// `get` on nodes/proxy (see the console ClusterRole).
func (c *Client) PodMetrics(ctx context.Context, node, namespace, pod string) (map[string]ContainerUsage, error) {
	if c.clientset == nil {
		return nil, errNoClientset
	}
	if node == "" {
		return nil, fmt.Errorf("pod %s/%s has no node yet (unscheduled)", namespace, pod)
	}
	raw, err := c.clientset.CoreV1().RESTClient().Get().
		Resource("nodes").Name(node).SubResource("proxy").
		Suffix("stats/summary").
		Param("only_cpu_and_memory", "true").
		DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("kubelet stats summary for node %s: %w", node, err)
	}
	return projectPodUsage(raw, namespace, pod)
}

// statsSummary mirrors the kubelet summary API response — only the fields the
// console projects. Decoded from JSON rather than importing the k8s.io/kubelet
// stats types, keeping this module free of another k8s dependency. CPU samples
// are deliberately not decoded: the console's usage block is memory-only.
type statsSummary struct {
	Pods []struct {
		PodRef struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"podRef"`
		Containers []struct {
			Name   string `json:"name"`
			Memory struct {
				WorkingSetBytes *uint64 `json:"workingSetBytes"`
			} `json:"memory"`
		} `json:"containers"`
	} `json:"pods"`
}

// projectPodUsage picks one pod out of a kubelet stats summary and normalizes
// its containers' memory usage to bytes. Defensive: absent/null sample fields
// read as 0. Split from the fetch so tests cover it with canned JSON.
func projectPodUsage(raw []byte, namespace, pod string) (map[string]ContainerUsage, error) {
	var sum statsSummary
	if err := json.Unmarshal(raw, &sum); err != nil {
		return nil, fmt.Errorf("decode kubelet stats summary: %w", err)
	}
	for _, p := range sum.Pods {
		if p.PodRef.Name != pod || p.PodRef.Namespace != namespace {
			continue
		}
		out := make(map[string]ContainerUsage, len(p.Containers))
		for _, ctr := range p.Containers {
			if ctr.Name == "" {
				continue
			}
			u := ContainerUsage{}
			if ctr.Memory.WorkingSetBytes != nil {
				u.MemoryBytes = clampInt64(*ctr.Memory.WorkingSetBytes)
			}
			out[ctr.Name] = u
		}
		return out, nil
	}
	return nil, fmt.Errorf("pod %s/%s not in kubelet stats summary", namespace, pod)
}

// clampInt64 converts a kubelet uint64 sample without overflow.
func clampInt64(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}
