// Package server is the read-only HTTP front end. It renders server-side HTML
// from the view model and exposes exactly one write route (rebuild), which
// patches an annotation using the GitHub username oauth2-proxy forwards.
package server

import (
	"errors"
	"log"
	"net/http"
	"sort"

	"github.com/toggle-corp/toggle-web-baker/console/internal/k8s"
	"github.com/toggle-corp/toggle-web-baker/console/internal/view"
)

// Server holds the FrontendApp client. The client is an interface so tests can
// drive the handlers with a fake dynamic client.
type Server struct {
	apps k8s.FrontendAppPatcher
}

// New constructs the server around a FrontendApp client.
func New(apps k8s.FrontendAppPatcher) *Server {
	return &Server{apps: apps}
}

// Routes returns the configured mux. Go 1.22+ pattern routing carries the
// method and {namespace}/{name} path values.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /", s.handleList)
	mux.HandleFunc("GET /ns/{namespace}/app/{name}", s.handleDetail)
	mux.HandleFunc("POST /ns/{namespace}/app/{name}/rebuild", s.handleRebuild)
	return mux
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
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

// userFrom reads the GitHub username oauth2-proxy injects. It checks both the
// header oauth2-proxy uses by default (X-Auth-Request-User) and the legacy /
// alternate X-Forwarded-User, per the brief.
func userFrom(r *http.Request) string {
	if u := r.Header.Get("X-Auth-Request-User"); u != "" {
		return u
	}
	return r.Header.Get("X-Forwarded-User")
}

func (s *Server) renderError(w http.ResponseWriter, code int, msg string, err error) {
	log.Printf("console: %s: %v", msg, err)
	w.WriteHeader(code)
	render(w, "error", errorData{Message: msg, Detail: err.Error(), Code: code})
}
