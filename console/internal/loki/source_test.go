package loki

import "testing"

func TestResolveLogSource(t *testing.T) {
	cases := []struct {
		name       string
		inProgress bool
		configured bool
		retained   bool
		want       Source
	}{
		{"in-progress -> live pod", true, false, false, SourceLivePod},
		{"in-progress beats loki", true, true, true, SourceLivePod},
		{"completed + loki -> loki", false, true, false, SourceLoki},
		{"completed + loki beats pod", false, true, true, SourceLoki},
		{"completed + no loki + retained -> pod fallback", false, false, true, SourcePodFallback},
		{"completed + nothing -> unavailable", false, false, false, SourceUnavailable},
	}
	for _, c := range cases {
		if got := ResolveLogSource(c.inProgress, c.configured, c.retained); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
