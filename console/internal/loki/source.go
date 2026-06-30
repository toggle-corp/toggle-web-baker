package loki

// Source identifies where the console should read a build's logs from. The
// console can `get` (but not `list`) the build pod, so a retained pod is a
// usable fallback when Loki is not configured.
type Source int

const (
	// SourceLivePod streams from the still-running build pod.
	SourceLivePod Source = iota
	// SourceLoki queries Loki for a completed build's logs.
	SourceLoki
	// SourcePodFallback reads a completed build's logs from the retained pod
	// (Loki not configured but the pod still exists).
	SourcePodFallback
	// SourceUnavailable means no log source is reachable.
	SourceUnavailable
)

// ResolveLogSource picks the log source for a build. While a build is in
// progress the live pod always wins. Once completed, prefer Loki (durable),
// then a retained pod, otherwise nothing is available.
func ResolveLogSource(inProgress, lokiConfigured, podRetained bool) Source {
	switch {
	case inProgress:
		return SourceLivePod
	case lokiConfigured:
		return SourceLoki
	case podRetained:
		return SourcePodFallback
	default:
		return SourceUnavailable
	}
}
