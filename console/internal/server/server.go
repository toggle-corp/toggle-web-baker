// Package server is the read-only HTTP front end. It renders server-side HTML
// from the view model and exposes exactly one write route (rebuild), which
// patches an annotation using the GitHub username oauth2-proxy forwards.
package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/toggle-corp/toggle-web-baker/console/internal/k8s"
	"github.com/toggle-corp/toggle-web-baker/console/internal/loki"
	"github.com/toggle-corp/toggle-web-baker/console/internal/view"

	corev1 "k8s.io/api/core/v1"
)

// PodReader is the pod-log capability the log handler depends on. The real
// k8s.Client satisfies it; tests fake it without a clientset.
type PodReader interface {
	GetPod(ctx context.Context, namespace, name string) (*corev1.Pod, error)
	PodLogTail(ctx context.Context, namespace, pod, container string, tail int64) ([]string, error)
}

// LokiTailer is the durable-log capability the log handler depends on. The real
// *loki.Client satisfies it; an unconfigured client reports Configured()==false.
type LokiTailer interface {
	Configured() bool
	Tail(ctx context.Context, namespace, pod, container string, start, end time.Time, limit int) ([]string, error)
}

// Server holds the FrontendApp client plus the log-source and live-metrics
// capabilities. All are interfaces so tests can drive the handlers with fakes.
type Server struct {
	apps    k8s.FrontendAppPatcher
	pods    PodReader
	tailer  LokiTailer
	metrics k8s.PodMetricser
}

// New constructs the server around a FrontendApp client, a pod reader, a Loki
// tailer, and a pod-metrics reader. metrics is best-effort: a nil value (or a
// failing fetch) degrades gracefully and never blocks the status fragment.
func New(apps k8s.FrontendAppPatcher, pods PodReader, tailer LokiTailer, metrics k8s.PodMetricser) *Server {
	return &Server{apps: apps, pods: pods, tailer: tailer, metrics: metrics}
}

// metricsTimeout bounds the live-metrics fetch so a slow/hanging metrics-server
// never delays or fails the status fragment. On timeout the fragment degrades.
const metricsTimeout = 1500 * time.Millisecond

// Routes returns the configured mux. Go 1.22+ pattern routing carries the
// method and {namespace}/{name} path values.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /signed-out", s.handleSignedOut)
	mux.HandleFunc("GET /", s.handleList)
	mux.HandleFunc("GET /ns/{namespace}/app/{name}", s.handleDetail)
	mux.HandleFunc("GET /ns/{namespace}/app/{name}/partial", s.handlePartial)
	mux.HandleFunc("GET /ns/{namespace}/app/{name}/logs", s.handleLogs)
	mux.HandleFunc("POST /ns/{namespace}/app/{name}/rebuild", s.handleRebuild)
	mux.HandleFunc("POST /ns/{namespace}/app/{name}/cleanup-cache", s.handleCleanupCache)
	mux.HandleFunc("POST /ns/{namespace}/app/{name}/cleanup-releases", s.handleCleanupReleases)
	return mux
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

// handleSignedOut renders the public post-logout page. The chart exempts
// ^/signed-out$ from oauth2-proxy, so this route requires no user header. It
// must not show a "Log out" link or the "no user header" badge.
func (s *Server) handleSignedOut(w http.ResponseWriter, _ *http.Request) {
	render(w, "signed-out", signedOutData{Head: head{Title: "Signed out", Anon: true}})
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	// The std mux routes every unmatched path to "GET /"; 404 anything that is
	// not the actual root so a typo'd app URL is not silently the list page.
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	apps, err := s.apps.List(r.Context())
	if err != nil {
		s.renderError(w, http.StatusBadGateway, "Could not list FrontendApps", err)
		return
	}
	sort.SliceStable(apps, func(i, j int) bool {
		if apps[i].Namespace != apps[j].Namespace {
			return apps[i].Namespace < apps[j].Namespace
		}
		return apps[i].Name < apps[j].Name
	})
	render(w, "list", listData{Head: head{Title: "Apps", User: userFrom(r)}, Apps: apps})
}

func (s *Server) handleDetail(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("namespace")
	name := r.PathValue("name")
	obj, err := s.apps.Get(r.Context(), ns, name)
	if err != nil {
		s.renderError(w, http.StatusNotFound, "FrontendApp not found", err)
		return
	}
	app := view.FromUnstructured(obj)
	s.attachBuildMetrics(r.Context(), ns, &app)
	render(w, "detail", detailData{
		Head:             head{Title: name, User: userFrom(r)},
		App:              app,
		Requested:        r.URL.Query().Get("rebuild") == "requested",
		CleanupRequested: r.URL.Query().Get("cleanup"),
	})
}

// handlePartial returns just the dynamic detail region (flow strip + recent
// builds + storage) for the poller. It is an HTML fragment, not a full page.
func (s *Server) handlePartial(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("namespace")
	name := r.PathValue("name")
	obj, err := s.apps.Get(r.Context(), ns, name)
	if err != nil {
		s.renderError(w, http.StatusNotFound, "FrontendApp not found", err)
		return
	}
	app := view.FromUnstructured(obj)
	s.attachBuildMetrics(r.Context(), ns, &app)
	render(w, "partial", partialData{App: app})
}

// attachBuildMetrics populates app.BuildMetrics with the build pod's active
// container live usage, best-effort. It only fetches when a build is live
// (BuildActive && PodName != "") and bounds the fetch with metricsTimeout so a
// slow/absent metrics-server never delays the status fragment. On any
// timeout/error/no-data while a build IS running it leaves BuildMetrics nil and
// sets BuildMetricsNote so the misconfiguration is visible; idle apps stay clean.
func (s *Server) attachBuildMetrics(ctx context.Context, ns string, app *view.App) {
	if s.metrics == nil || !app.BuildActive() || app.Build.PodName == "" {
		return
	}
	container := defaultContainer(containerSteps(app.Build))

	mctx, cancel := context.WithTimeout(ctx, metricsTimeout)
	defer cancel()
	usageByContainer, err := s.metrics.PodMetrics(mctx, ns, app.Build.PodName)
	if err != nil {
		app.BuildMetricsNote = "metrics unavailable"
		return
	}
	usage, ok := usageByContainer[container]
	if !ok {
		app.BuildMetricsNote = "metrics unavailable"
		return
	}

	bm := &view.BuildMetrics{
		Container:     container,
		CPUMillicores: usage.CPUMillicores,
		MemoryBytes:   usage.MemoryBytes,
		CPUHuman:      view.HumanizeCPU(usage.CPUMillicores),
		MemoryHuman:   view.HumanizeBytes(usage.MemoryBytes),
	}

	// Resolve the active container's resource limits from the pod so the % bars
	// have something to draw against. A missing pod/limit simply yields no bar.
	if s.pods != nil {
		if pod, perr := s.pods.GetPod(ctx, ns, app.Build.PodName); perr == nil {
			cpuLim, memLim := containerLimits(pod, container)
			bm.CPULimitMilli = cpuLim
			bm.MemLimitBytes = memLim
			if pct, over := view.StorageBar(usage.CPUMillicores, cpuLim); pct != view.StorageBarNoBar {
				bm.HasCPUBar, bm.CPUBarPct, bm.CPUOver = true, pct, over
			}
			if pct, over := view.StorageBar(usage.MemoryBytes, memLim); pct != view.StorageBarNoBar {
				bm.HasMemBar, bm.MemBarPct, bm.MemOver = true, pct, over
			}
		}
	}

	app.BuildMetrics = bm
}

// containerLimits returns the named container's cpu (millicores) and memory
// (bytes) limits from a pod, or (0,0) when the container or limits are absent.
func containerLimits(pod *corev1.Pod, container string) (cpuMilli, memBytes int64) {
	if pod == nil {
		return 0, 0
	}
	for i := range pod.Spec.Containers {
		c := pod.Spec.Containers[i]
		if c.Name != container {
			continue
		}
		if cpu, ok := c.Resources.Limits[corev1.ResourceCPU]; ok {
			cpuMilli = cpu.MilliValue()
		}
		if mem, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
			memBytes = mem.Value()
		}
		return cpuMilli, memBytes
	}
	return 0, 0
}

// handleLogs returns the log pane fragment for one build. It resolves the log
// source (live pod / Loki / retained pod / unavailable) and renders the
// container <select>, a one-line source note, and the lines (or a note).
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("namespace")
	name := r.PathValue("name")
	obj, err := s.apps.Get(r.Context(), ns, name)
	if err != nil {
		s.renderError(w, http.StatusNotFound, "FrontendApp not found", err)
		return
	}
	app := view.FromUnstructured(obj)

	// Follow mode chases the live build: it ignores the build and container
	// params, always resolving the current build and its active container (so a
	// newly started build is picked up, and the container tracks Failed→Running→last).
	follow := r.URL.Query().Get("follow") == "1"

	build := r.URL.Query().Get("build")
	if follow {
		build = "current"
	}
	rec, isCurrent, found := pickBuild(app, build)
	if !found {
		s.renderError(w, http.StatusNotFound, "build not found", errors.New("no build matches "+build))
		return
	}

	// Only the build's real pod containers are valid log targets; reject any
	// other ?container= value (a stray value would otherwise be interpolated
	// into the Loki selector and break the query) and fall back to the default.
	steps := containerSteps(rec)
	container := r.URL.Query().Get("container")
	if follow || !validContainer(steps, container) {
		container = defaultContainer(steps)
	}

	data := s.resolveLogs(r.Context(), ns, rec, isCurrent, container, steps)
	data.Follow = follow
	render(w, "logpane", data)
}

// releaseStepName is the synthetic final step (the operator's release-pointer
// flip). It has no pod container and therefore no logs.
const releaseStepName = "release"

// containerSteps returns the build steps that map to a real pod container —
// every step except the synthetic "release". These are the only valid values
// for the container picker and the ?container= param.
func containerSteps(rec view.Build) []view.Step {
	out := make([]view.Step, 0, len(rec.Steps))
	for _, st := range rec.Steps {
		if st.Name == releaseStepName {
			continue
		}
		out = append(out, st)
	}
	return out
}

// validContainer reports whether name is one of the build's real containers.
func validContainer(steps []view.Step, name string) bool {
	for _, st := range steps {
		if st.Name == name {
			return true
		}
	}
	return false
}

// pickBuild selects the BuildStatus to show. An empty or "current" build param
// selects App.Build; otherwise it matches a history entry by JobName. isCurrent
// reports whether the selected record is the current build (vs history).
func pickBuild(app view.App, build string) (rec view.Build, isCurrent, found bool) {
	if build == "" || build == "current" {
		return app.Build, true, true
	}
	if app.Build.JobName == build {
		return app.Build, true, true
	}
	for _, h := range app.BuildHistory {
		if h.JobName == build {
			return h, false, true
		}
	}
	return view.Build{}, false, false
}

// defaultContainer chooses which real container's logs to show by default: the
// failed/aborted step, else the running step, else the last container (copier),
// else "build". steps must already exclude the synthetic release step.
func defaultContainer(steps []view.Step) string {
	for _, st := range steps {
		if st.Status == "Failed" || st.Status == "Aborted" {
			return st.Name
		}
	}
	for _, st := range steps {
		if st.Status == "Running" {
			return st.Name
		}
	}
	if len(steps) > 0 {
		return steps[len(steps)-1].Name
	}
	return "build"
}

// resolveLogs determines the source and fetches the lines, degrading
// gracefully. steps is the container-picker option list (release excluded).
func (s *Server) resolveLogs(ctx context.Context, ns string, rec view.Build, isCurrent bool, container string, steps []view.Step) logpaneData {
	data := logpaneData{
		Namespace: ns,
		Build:     rec,
		Container: container,
		Steps:     steps,
	}

	// inProgress only when this is the CURRENT build and its phase is not done.
	inProgress := isCurrent && (rec.Phase == "Running" || rec.Phase == "Pending") && rec.CompletionTime == ""

	lokiConfigured := s.tailer != nil && s.tailer.Configured()

	podRetained := false
	if rec.PodName != "" && s.pods != nil {
		if _, err := s.pods.GetPod(ctx, ns, rec.PodName); err == nil {
			podRetained = true
		}
	}

	src := loki.ResolveLogSource(inProgress, lokiConfigured, podRetained)
	switch src {
	case loki.SourceLivePod:
		data.SourceNote = "live pod"
		data.Lines, data.FetchErr = s.podLines(ctx, ns, rec.PodName, container)
	case loki.SourcePodFallback:
		data.SourceNote = "pod (Loki unavailable)"
		data.Lines, data.FetchErr = s.podLines(ctx, ns, rec.PodName, container)
	case loki.SourceLoki:
		data.SourceNote = "Loki"
		start, _ := time.Parse(time.RFC3339, rec.StartTime)
		end := view.Now()
		if rec.CompletionTime != "" {
			if t, err := time.Parse(time.RFC3339, rec.CompletionTime); err == nil {
				end = t
			}
		}
		data.Lines, data.FetchErr = s.tailer.Tail(ctx, ns, rec.PodName, container, start, end, 100)
	default: // SourceUnavailable
		data.Unavailable = "logs unavailable — job " + dashOr(rec.JobName) + " / pod " + dashOr(rec.PodName)
	}
	if data.FetchErr != nil {
		data.Unavailable = "logs unavailable — " + data.FetchErr.Error()
	}
	return data
}

func (s *Server) podLines(ctx context.Context, ns, pod, container string) ([]string, error) {
	if s.pods == nil {
		return nil, errors.New("pod reader unavailable")
	}
	return s.pods.PodLogTail(ctx, ns, pod, container, 100)
}

func dashOr(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// ErrNoUser is returned when neither oauth2-proxy user header is present. In
// production the console is unreachable without oauth2-proxy, so a missing
// header means a misconfiguration and we fail closed.
var ErrNoUser = errors.New("no authenticated user header")

func (s *Server) handleRebuild(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("namespace")
	name := r.PathValue("name")

	user := userFrom(r)
	if user == "" {
		s.renderError(w, http.StatusUnauthorized,
			"No authenticated user", ErrNoUser)
		return
	}

	if err := s.apps.RequestRebuild(r.Context(), ns, name, user); err != nil {
		s.renderError(w, http.StatusBadGateway, "Rebuild request failed", err)
		return
	}
	// Redirect back to the detail page so the new lastManualTrigger shows up on
	// the next reconcile; 303 turns the POST into a GET.
	http.Redirect(w, r, "/ns/"+ns+"/app/"+name+"?rebuild=requested", http.StatusSeeOther)
}

// handleCleanupCache mirrors handleRebuild: it requires a user, patches the
// cache-cleanup annotations, and 303-redirects to the detail page with the flag.
func (s *Server) handleCleanupCache(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("namespace")
	name := r.PathValue("name")

	user := userFrom(r)
	if user == "" {
		s.renderError(w, http.StatusUnauthorized, "No authenticated user", ErrNoUser)
		return
	}
	if err := s.apps.RequestCleanupCache(r.Context(), ns, name, user); err != nil {
		s.renderError(w, http.StatusBadGateway, "Cache cleanup request failed", err)
		return
	}
	http.Redirect(w, r, "/ns/"+ns+"/app/"+name+"?cleanup=cache", http.StatusSeeOther)
}

// handleCleanupReleases mirrors handleRebuild for the release-prune action.
func (s *Server) handleCleanupReleases(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("namespace")
	name := r.PathValue("name")

	user := userFrom(r)
	if user == "" {
		s.renderError(w, http.StatusUnauthorized, "No authenticated user", ErrNoUser)
		return
	}
	if err := s.apps.RequestCleanupReleases(r.Context(), ns, name, user); err != nil {
		s.renderError(w, http.StatusBadGateway, "Release cleanup request failed", err)
		return
	}
	http.Redirect(w, r, "/ns/"+ns+"/app/"+name+"?cleanup=releases", http.StatusSeeOther)
}

// userFrom reads the GitHub username oauth2-proxy injects into the upstream
// request. In reverse-proxy mode oauth2-proxy passes X-Forwarded-User (via
// --pass-user-headers) — that is the live source. X-Auth-Request-User is only
// emitted in nginx auth_request mode, kept here as a harmless fallback.
func userFrom(r *http.Request) string {
	if u := r.Header.Get("X-Forwarded-User"); u != "" {
		return u
	}
	return r.Header.Get("X-Auth-Request-User")
}

func (s *Server) renderError(w http.ResponseWriter, code int, msg string, err error) {
	log.Printf("console: %s: %v", msg, err)
	w.WriteHeader(code)
	render(w, "error", errorData{Message: msg, Detail: err.Error(), Code: code})
}
