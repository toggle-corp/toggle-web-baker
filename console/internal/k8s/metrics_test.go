package k8s

import "testing"

// summaryJSON is a trimmed kubelet /stats/summary payload: two pods on the
// node, the build pod currently in its INIT phase (the `build` step running) —
// the exact shape metrics.k8s.io can never report.
const summaryJSON = `{
  "node": {"nodeName": "node-1"},
  "pods": [
    {
      "podRef": {"name": "other-app-abc", "namespace": "other", "uid": "u1"},
      "containers": [
        {"name": "web", "cpu": {"usageNanoCores": 999000000}, "memory": {"workingSetBytes": 999}}
      ]
    },
    {
      "podRef": {"name": "mapswipe-build-xyz", "namespace": "apps", "uid": "u2"},
      "containers": [
        {"name": "build", "cpu": {"usageNanoCores": 431648513}, "memory": {"workingSetBytes": 634040320}},
        {"name": "clone", "cpu": {"usageNanoCores": 0}, "memory": {"workingSetBytes": 1048576}}
      ]
    }
  ]
}`

func TestProjectPodUsage(t *testing.T) {
	usage, err := projectPodUsage([]byte(summaryJSON), "apps", "mapswipe-build-xyz")
	if err != nil {
		t.Fatalf("projectPodUsage: %v", err)
	}
	if len(usage) != 2 {
		t.Fatalf("want 2 containers, got %d: %v", len(usage), usage)
	}
	b := usage["build"]
	if b.CPUMillicores != 431 { // 431648513 nanocores -> 431 millicores
		t.Errorf("build CPU = %dm, want 431m", b.CPUMillicores)
	}
	if b.MemoryBytes != 634040320 {
		t.Errorf("build mem = %d, want 634040320", b.MemoryBytes)
	}
	if _, ok := usage["web"]; ok {
		t.Error("must not leak another pod's containers")
	}
}

func TestProjectPodUsage_PodAbsent(t *testing.T) {
	if _, err := projectPodUsage([]byte(summaryJSON), "apps", "gone-pod"); err == nil {
		t.Error("want error when the pod is not in the summary")
	}
	// Same name, wrong namespace must not match.
	if _, err := projectPodUsage([]byte(summaryJSON), "wrong-ns", "mapswipe-build-xyz"); err == nil {
		t.Error("want error when the namespace does not match")
	}
}

func TestProjectPodUsage_MissingSamplesReadZero(t *testing.T) {
	// A container that just started can have a podRef entry with no cpu/memory
	// sample yet — null/absent fields must read as 0, not fail.
	raw := `{"pods":[{"podRef":{"name":"p","namespace":"n"},"containers":[{"name":"setup","cpu":{},"memory":{}}]}]}`
	usage, err := projectPodUsage([]byte(raw), "n", "p")
	if err != nil {
		t.Fatalf("projectPodUsage: %v", err)
	}
	if u := usage["setup"]; u.CPUMillicores != 0 || u.MemoryBytes != 0 {
		t.Errorf("missing samples should read 0/0, got %+v", u)
	}
}

func TestProjectPodUsage_GarbageJSON(t *testing.T) {
	if _, err := projectPodUsage([]byte("not json"), "n", "p"); err == nil {
		t.Error("want decode error on garbage payload")
	}
}
