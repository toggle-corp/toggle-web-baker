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

// Server holds the FrontendApp client plus the log-source capabilities. All
// three are interfaces so tests can drive the handlers with fakes.
type Server struct {
	apps   k8s.FrontendAppPatcher
	pods   PodReader
	tailer LokiTailer
}

// New constructs the server around a FrontendApp client, a pod reader, and a
// Loki tailer.
func New(apps k8s.FrontendAppPatcher, pods PodReader, tailer LokiTailer) *Server {
	return &Server{apps: apps, pods: pods, tailer: tailer}
}

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
	render(w, "detail", detailData{
		Head:      head{Title: name, User: userFrom(r)},
		App:       view.FromUnstructured(obj),
		Requested: r.URL.Query().Get("rebuild") == "requested",
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
	render(w, "partial", partialData{App: view.FromUnstructured(obj)})
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

	build := r.URL.Query().Get("build")
	rec, isCurrent, found := pickBuild(app, build)
	if !found {
		s.renderError(w, http.StatusNotFound, "build not found", errors.New("no build matches "+build))
		return
	}

	container := r.URL.Query().Get("container")
	if container == "" {
		container = defaultContainer(rec)
	}

	data := s.resolveLogs(r.Context(), ns, rec, isCurrent, container)
	render(w, "logpane", data)
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

// defaultContainer chooses which step's logs to show by default: the failed
// step, else the running step, else "build".
func defaultContainer(rec view.Build) string {
	if rec.FailedStep != "" {
		return rec.FailedStep
	}
	for _, st := range rec.Steps {
		if st.Status == "Running" {
			return st.Name
		}
	}
	return "build"
}

// resolveLogs determines the source and fetches the lines, degrading gracefully.
func (s *Server) resolveLogs(ctx context.Context, ns string, rec view.Build, isCurrent bool, container string) logpaneData {
	data := logpaneData{
		Namespace: ns,
		Build:     rec,
		Container: container,
		Steps:     rec.Steps,
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
