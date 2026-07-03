// Package server is the read-only HTTP front end. It renders server-side HTML
// from the view model and exposes exactly one write route (rebuild), which
// patches an annotation using the GitHub username oauth2-proxy forwards.
package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/toggle-corp/toggle-web-baker/console/internal/k8s"
	"github.com/toggle-corp/toggle-web-baker/console/internal/loki"
	"github.com/toggle-corp/toggle-web-baker/console/internal/sentryhttp"
	"github.com/toggle-corp/toggle-web-baker/console/internal/view"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
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

// Server holds the App client plus the log-source and live-metrics
// capabilities. All are interfaces so tests can drive the handlers with fakes.
type Server struct {
	apps    k8s.AppPatcher
	pods    PodReader
	tailer  LokiTailer
	metrics k8s.PodMetricser
}

// New constructs the server around a App client, a pod reader, a Loki
// tailer, and a pod-metrics reader. metrics is best-effort: a nil value (or a
// failing fetch) degrades gracefully and never blocks the status fragment.
// Live metrics also need pods (the pod read resolves which kubelet to ask).
func New(apps k8s.AppPatcher, pods PodReader, tailer LokiTailer, metrics k8s.PodMetricser) *Server {
	return &Server{apps: apps, pods: pods, tailer: tailer, metrics: metrics}
}

// metricsTimeout bounds the live-metrics fetch (pod read + kubelet stats) so a
// slow/hanging kubelet never delays or fails the status fragment. On timeout
// the fragment degrades.
const metricsTimeout = 1500 * time.Millisecond

// logTailLines is how many newest lines the log pane fetches from either source
// (Loki query_range or the kubelet's pod-log tail). The pane is a scrollable
// <pre>, so this is sized to hold a WHOLE typical build log (the previous 100
// truncated Loki-mode history logs to their last few screens); 5000 matches
// Loki's default max_entries_limit_per_query — asking for more would 400.
const logTailLines = 5000

// Routes returns the configured mux. Go 1.22+ pattern routing carries the
// method and {namespace}/{name} path values.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /signed-out", s.handleSignedOut)
	mux.HandleFunc("GET /", s.handleList)
	mux.HandleFunc("GET /ns/{namespace}/app/{name}", s.handleDetail)
	mux.HandleFunc("GET /ns/{namespace}/app/{name}/manifest", s.handleManifest)
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
	// Cache not warm yet: render the "warming up" state instead of running the
	// facet/filter/pagination/storage pipeline over an empty set (which would
	// render as a healthy-looking empty cluster). See listData.Warming.
	if !s.apps.Synced() {
		render(w, "list", listData{
			Head:    head{Title: "Apps", User: sentryhttp.UserFrom(r)},
			Warming: true,
		})
		return
	}

	apps, err := s.apps.List(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusBadGateway, "Could not list Apps", err)
		return
	}

	// Server-side filters (plain query params — no client-side JS). Facet
	// counts and group chips are computed from the UNFILTERED set so the chips
	// never vanish while a filter is active; the two params compose.
	status := r.URL.Query().Get("status")
	if !validHealthClass(status) {
		status = ""
	}
	group := r.URL.Query().Get("group")
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	// sortOrder: "" = most-broken-first (default), "name" = plain namespace/name.
	// Any other value falls back to the default rather than erroring.
	sortOrder := r.URL.Query().Get("sort")
	if sortOrder != sortByName {
		sortOrder = ""
	}

	facets := statusFacets(apps, status, group, search, sortOrder)
	chips := groupChips(apps, status, group, search, sortOrder)
	filtered := searchApps(filterApps(apps, status, group), search)

	// Most-broken-first: degraded, degraded-serving, building, pending, ready;
	// stable namespace/name order within a rank. ?sort=name skips the rank.
	sort.SliceStable(filtered, func(i, j int) bool {
		if sortOrder == "" {
			if ri, rj := filtered[i].HealthRank(), filtered[j].HealthRank(); ri != rj {
				return ri < rj
			}
		}
		if filtered[i].Namespace != filtered[j].Namespace {
			return filtered[i].Namespace < filtered[j].Namespace
		}
		return filtered[i].Name < filtered[j].Name
	})

	// Paginate AFTER filter+search+sort: clamp the requested page to [1,lastPage]
	// (never 404) and slice the sorted set to that page's window.
	page, totalPages := clampPage(r.URL.Query().Get("page"), len(filtered))
	lo := (page - 1) * pageSize
	hi := lo + pageSize
	if hi > len(filtered) {
		hi = len(filtered)
	}
	pageApps := filtered[lo:hi]

	render(w, "list", listData{
		Head: head{Title: "Apps", User: sentryhttp.UserFrom(r)},
		// Stale: synced but the watch is currently erroring, so the list may be
		// out of date — the template shows a banner above the table.
		Stale: s.apps.Stale(),
		Apps:  pageApps,
		// Matched is the filtered (pre-pagination) count; the storage roll-up
		// spans the SAME set, so the heading count and storage describe one set.
		// Filtered flips the count to "Matched of Total" when a filter narrowed it.
		Total:    len(apps),
		Matched:  len(filtered),
		Filtered: len(filtered) != len(apps),
		Storage:  view.AggregateStorage(filtered),

		StatusFacets:   facets,
		GroupChips:     chips,
		Search:         search,
		ClearSearchURL: listURL(status, group, "", sortOrder),
		Status:         status,
		Group:          group,
		Sort:           sortOrder,
		SortToggleURL:  listURL(status, group, search, toggleSort(sortOrder)),
		Page:           page,
		TotalPages:     totalPages,
		ShowPager:      totalPages > 1,
		HasPrev:        page > 1,
		HasNext:        page < totalPages,
		PrevURL:        pageURL(status, group, search, sortOrder, page-1),
		NextURL:        pageURL(status, group, search, sortOrder, page+1),
	})
}

// sortByName is the ?sort= value for plain namespace/name order; the empty
// string is the most-broken-first default. toggleSort flips between the two
// for the list heading's sort link.
const sortByName = "name"

func toggleSort(cur string) string {
	if cur == sortByName {
		return ""
	}
	return sortByName
}

// pageSize is the fixed in-memory page window for the app list.
const pageSize = 50

// clampPage parses the ?page= param and clamps it to [1,totalPages]. Non-numeric
// or <1 → 1; > last → last. totalPages is ceil(match/pageSize), min 1 (an empty
// result set is page 1 of 1). It never errors — a bad page is a clamp, not a 404.
func clampPage(raw string, match int) (page, totalPages int) {
	totalPages = (match + pageSize - 1) / pageSize
	if totalPages < 1 {
		totalPages = 1
	}
	page, err := strconv.Atoi(raw)
	if err != nil || page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}
	return page, totalPages
}

// pageURL builds a Prev/Next href: listURL's filter params plus ?page=N. Unlike
// listURL (used for chips/search, which reset to page 1 by omitting page), this
// sets page so paging preserves the active status/group/search/sort.
func pageURL(status, group, search, sortOrder string, page int) string {
	q := url.Values{}
	if status != "" {
		q.Set("status", status)
	}
	if group != "" {
		q.Set("group", group)
	}
	if search != "" {
		q.Set("search", search)
	}
	if sortOrder != "" {
		q.Set("sort", sortOrder)
	}
	q.Set("page", strconv.Itoa(page))
	return "/?" + q.Encode()
}

// healthClasses is the fixed facet order (after "all"). Values are the
// view.App.HealthClass() strings.
var healthClasses = []string{"ready", "building", "degraded", "degraded-serving", "pending"}

func validHealthClass(s string) bool {
	for _, hc := range healthClasses {
		if s == hc {
			return true
		}
	}
	return false
}

// facetLabel is the chip text for a health class; degraded-serving reads as
// its human name.
func facetLabel(hc string) string {
	if hc == "degraded-serving" {
		return "serving last-good"
	}
	return hc
}

// ungroupedParam is the ?group= sentinel selecting apps WITHOUT a spec.group.
// A real group literally named "ungrouped" would be shadowed by it — accepted;
// the chip row simply filters the group-less apps.
const ungroupedParam = "ungrouped"

// listURL builds a list link composing the filter params. Empty values are
// omitted so the "all" chips link to a clean URL; search and sort are carried
// through so clicking a chip keeps the active search term and sort order.
func listURL(status, group, search, sortOrder string) string {
	q := url.Values{}
	if status != "" {
		q.Set("status", status)
	}
	if group != "" {
		q.Set("group", group)
	}
	if search != "" {
		q.Set("search", search)
	}
	if sortOrder != "" {
		q.Set("sort", sortOrder)
	}
	if len(q) == 0 {
		return "/"
	}
	return "/?" + q.Encode()
}

// statusFacet is one status filter chip: "ready (4)" linking to /?status=ready
// (composed with the active group).
type statusFacet struct {
	Value  string // health class; "" = all
	Label  string
	Count  int
	Active bool
	URL    string
}

// statusFacets computes the fixed-order status chips with counts from the
// UNFILTERED app set. The emitted URLs carry the active group, search, and sort
// so the params keep composing across chip navigation.
func statusFacets(apps []view.App, activeStatus, group, search, sortOrder string) []statusFacet {
	counts := map[string]int{}
	for _, a := range apps {
		counts[a.HealthClass()]++
	}
	facets := []statusFacet{{
		Label:  "all",
		Count:  len(apps),
		Active: activeStatus == "",
		URL:    listURL("", group, search, sortOrder),
	}}
	for _, hc := range healthClasses {
		facets = append(facets, statusFacet{
			Value:  hc,
			Label:  facetLabel(hc),
			Count:  counts[hc],
			Active: activeStatus == hc,
			URL:    listURL(hc, group, search, sortOrder),
		})
	}
	return facets
}

// groupChip is one group filter chip linking to /?group=<g> (composed with the
// active status).
type groupChip struct {
	Value  string // group name, ungroupedParam, or "" = all
	Label  string
	Active bool
	URL    string
}

// groupChips builds the group chip row: "all", one chip per distinct
// spec.group, and "ungrouped" when group-less apps exist. When NO app carries
// a group the row is omitted entirely (nil) — a lone "all·ungrouped" pair
// would be noise.
func groupChips(apps []view.App, status, activeGroup, search, sortOrder string) []groupChip {
	set := map[string]bool{}
	hasUngrouped := false
	for _, a := range apps {
		if a.Group == "" {
			hasUngrouped = true
		} else {
			set[a.Group] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	groups := make([]string, 0, len(set))
	for g := range set {
		groups = append(groups, g)
	}
	sort.Strings(groups)

	chips := []groupChip{{Label: "all", Active: activeGroup == "", URL: listURL(status, "", search, sortOrder)}}
	for _, g := range groups {
		chips = append(chips, groupChip{Value: g, Label: g, Active: activeGroup == g, URL: listURL(status, g, search, sortOrder)})
	}
	if hasUngrouped {
		chips = append(chips, groupChip{
			Value:  ungroupedParam,
			Label:  "ungrouped",
			Active: activeGroup == ungroupedParam,
			URL:    listURL(status, ungroupedParam, search, sortOrder),
		})
	}
	return chips
}

// filterApps applies the composed status + group filters.
func filterApps(apps []view.App, status, group string) []view.App {
	out := make([]view.App, 0, len(apps))
	for _, a := range apps {
		if status != "" && a.HealthClass() != status {
			continue
		}
		switch group {
		case "":
			// no group filter
		case ungroupedParam:
			if a.Group != "" {
				continue
			}
		default:
			if a.Group != group {
				continue
			}
		}
		out = append(out, a)
	}
	return out
}

// searchApps keeps apps where term is a case-insensitive substring of ANY single
// field (name, namespace, group, or URL host). Matching per-field — not against a
// joined haystack — is deliberate: a term with a space (e.g. "web prod") must
// not match ACROSS a field boundary (name "…-web" + namespace "prod-…"). An
// empty term is a no-op.
func searchApps(apps []view.App, term string) []view.App {
	if term == "" {
		return apps
	}
	term = strings.ToLower(term)
	out := make([]view.App, 0, len(apps))
	for _, a := range apps {
		for _, field := range []string{a.Name, a.Namespace, a.Group, a.URLHost()} {
			if strings.Contains(strings.ToLower(field), term) {
				out = append(out, a)
				break
			}
		}
	}
	return out
}

// getApp resolves the {namespace}/{name} path values and fetches the App,
// rendering the shared not-found page (and reporting ok=false) on any miss.
func (s *Server) getApp(w http.ResponseWriter, r *http.Request) (obj *unstructured.Unstructured, ns, name string, ok bool) {
	ns = r.PathValue("namespace")
	name = r.PathValue("name")
	obj, err := s.apps.Get(r.Context(), ns, name)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "App not found", err)
		return nil, ns, name, false
	}
	return obj, ns, name, true
}

func (s *Server) handleDetail(w http.ResponseWriter, r *http.Request) {
	obj, ns, name, ok := s.getApp(w, r)
	if !ok {
		return
	}
	app := view.FromUnstructured(obj)
	s.attachBuildMetrics(r.Context(), ns, &app)
	render(w, "detail", detailData{
		// Bare: the detail page has no standard header — its sticky status bar
		// carries the breadcrumb, actions, and theme select instead.
		Head:             head{Title: name, User: sentryhttp.UserFrom(r), Bare: true},
		App:              app,
		Requested:        r.URL.Query().Get("rebuild") == "requested",
		CleanupRequested: r.URL.Query().Get("cleanup"),
	})
}

// handleManifest renders the read-only "what you applied" view of an App: the
// object pruned to apiVersion/kind/name/namespace/labels/spec (annotations kept
// but values hidden), marshaled to YAML and syntax-highlighted server-side.
func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	obj, ns, name, ok := s.getApp(w, r)
	if !ok {
		return
	}
	pruned, masked := pruneManifest(obj)
	raw, err := yaml.Marshal(pruned)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Could not render manifest", err)
		return
	}
	render(w, "manifest", manifestData{
		Head:        head{Title: name + " · manifest", User: sentryhttp.UserFrom(r), Bare: true},
		Namespace:   ns,
		Name:        name,
		Highlighted: highlightYAML(string(raw)),
		HasHidden:   masked,
		SetupHint:   setupHint(obj),
	})
}

// handlePartial returns just the dynamic detail region (flow strip + recent
// builds + storage) for the poller. It is an HTML fragment, not a full page.
func (s *Server) handlePartial(w http.ResponseWriter, r *http.Request) {
	obj, ns, _, ok := s.getApp(w, r)
	if !ok {
		return
	}
	app := view.FromUnstructured(obj)
	s.attachBuildMetrics(r.Context(), ns, &app)
	render(w, "partial", partialData{App: app})
}

// attachBuildMetrics populates app.BuildMetrics with the build pod's active
// container live usage, best-effort. It only fetches when a build is live
// (BuildActive && PodName != "") and bounds the whole fetch (pod read + kubelet
// stats) with metricsTimeout so a slow node never delays the status fragment.
// The pod read comes FIRST: it resolves both spec.nodeName (which kubelet to
// ask — see k8s.Client.PodMetrics for why metrics.k8s.io cannot serve build
// pods) and the active container's limits for the % bars. On any
// timeout/error/no-data while a build IS running it leaves BuildMetrics nil and
// sets BuildMetricsNote so the misconfiguration is visible; idle apps stay clean.
func (s *Server) attachBuildMetrics(ctx context.Context, ns string, app *view.App) {
	if s.metrics == nil || s.pods == nil || !app.BuildActive() || app.Build.PodName == "" {
		return
	}
	container := defaultContainer(containerSteps(app.Build))

	mctx, cancel := context.WithTimeout(ctx, metricsTimeout)
	defer cancel()
	pod, err := s.pods.GetPod(mctx, ns, app.Build.PodName)
	if err != nil {
		app.BuildMetricsNote = "metrics unavailable"
		return
	}
	usageByContainer, err := s.metrics.PodMetrics(mctx, pod.Spec.NodeName, ns, app.Build.PodName)
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
		Container:   container,
		MemoryBytes: usage.MemoryBytes,
		MemoryHuman: view.HumanizeBytes(usage.MemoryBytes),
	}

	// Resolve the active container's memory limit from the pod so the % bar has
	// something to draw against, and the "used / available" text its right-hand
	// side. A missing limit simply yields no bar and no "/ available".
	memLim := containerMemLimit(pod, container)
	bm.MemLimitBytes = memLim
	if memLim > 0 {
		bm.MemLimitHuman = view.HumanizeBytes(memLim)
	}
	if pct, over := view.StorageBar(usage.MemoryBytes, memLim); pct != view.StorageBarNoBar {
		bm.HasMemBar, bm.MemBarPct, bm.MemOver = true, pct, over
	}

	app.BuildMetrics = bm
}

// containerMemLimit returns the named container's memory limit in bytes from a
// pod, or 0 when the container or limit is absent. Both container lists are
// searched: every build step runs as an initContainer (only the copier is an
// app container), and the active step is usually init.
func containerMemLimit(pod *corev1.Pod, container string) int64 {
	if pod == nil {
		return 0
	}
	for _, list := range [][]corev1.Container{pod.Spec.InitContainers, pod.Spec.Containers} {
		for i := range list {
			c := list[i]
			if c.Name != container {
				continue
			}
			if mem, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
				return mem.Value()
			}
			return 0
		}
	}
	return 0
}

// handleLogs returns the log pane fragment for one build. It resolves the log
// source (live pod / Loki / retained pod / unavailable) and renders the
// container badge buttons, a one-line source note, and the lines (or a note).
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	obj, ns, _, ok := s.getApp(w, r)
	if !ok {
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
		s.renderError(w, r, http.StatusNotFound, "build not found", errors.New("no build matches "+build))
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
	data.AppActive = app.BuildActive()
	data.IsCurrent = isCurrent
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
// failed/aborted step, else the running step, else — when some step has already
// finished — the FIRST Pending step (the kubelet gap between one init container
// terminating and the next starting; without this, follow mode briefly resolved
// to the last container (copier) on every phase change), else the last container
// (copier, the all-done case), else "build". steps must already exclude the
// synthetic release step.
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
	// Between-phases gap: the next step up is the first Pending one AFTER a
	// Succeeded step. An all-Pending timeline (pod not started) keeps the "build"
	// fallback below — clone's logs would be empty anyway.
	started := false
	for _, st := range steps {
		if st.Status == "Succeeded" {
			started = true
			continue
		}
		if started && st.Status == "Pending" {
			return st.Name
		}
	}
	if len(steps) > 0 {
		if steps[0].Status == "Pending" {
			// Nothing has run yet (pod still scheduling): follow the first step,
			// not the copier.
			return steps[0].Name
		}
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
		data.Lines, data.FetchErr = s.tailer.Tail(ctx, ns, rec.PodName, container, start, end, logTailLines)
	default: // SourceUnavailable
		if rec.JobName == "" && rec.PodName == "" {
			// Never-built app: say so instead of "job — / pod —".
			data.Unavailable = "no builds yet — logs will appear when the first build starts"
		} else {
			data.Unavailable = "logs unavailable — job " + dashOr(rec.JobName) + " / pod " + dashOr(rec.PodName)
		}
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
	return s.pods.PodLogTail(ctx, ns, pod, container, logTailLines)
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

	user := sentryhttp.UserFrom(r)
	if user == "" {
		s.renderError(w, r, http.StatusUnauthorized,
			"No authenticated user", ErrNoUser)
		return
	}

	if err := s.apps.RequestRebuild(r.Context(), ns, name, user); err != nil {
		s.renderError(w, r, http.StatusBadGateway, "Rebuild request failed", err)
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

	user := sentryhttp.UserFrom(r)
	if user == "" {
		s.renderError(w, r, http.StatusUnauthorized, "No authenticated user", ErrNoUser)
		return
	}
	if err := s.apps.RequestCleanupCache(r.Context(), ns, name, user); err != nil {
		s.renderError(w, r, http.StatusBadGateway, "Cache cleanup request failed", err)
		return
	}
	http.Redirect(w, r, "/ns/"+ns+"/app/"+name+"?cleanup=cache", http.StatusSeeOther)
}

// handleCleanupReleases mirrors handleRebuild for the release-prune action.
func (s *Server) handleCleanupReleases(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("namespace")
	name := r.PathValue("name")

	user := sentryhttp.UserFrom(r)
	if user == "" {
		s.renderError(w, r, http.StatusUnauthorized, "No authenticated user", ErrNoUser)
		return
	}
	if err := s.apps.RequestCleanupReleases(r.Context(), ns, name, user); err != nil {
		s.renderError(w, r, http.StatusBadGateway, "Release cleanup request failed", err)
		return
	}
	http.Redirect(w, r, "/ns/"+ns+"/app/"+name+"?cleanup=releases", http.StatusSeeOther)
}

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, code int, msg string, err error) {
	log.Printf("console: %s: %v", msg, err)
	sentryhttp.AttachError(r.Context(), msg, err)
	w.WriteHeader(code)
	// The head carries the request's user: without it the header shows the red
	// "no user header" badge — a false oauth2-proxy alarm — on every error page.
	render(w, "error", errorData{
		Head:    head{Title: "Error", User: sentryhttp.UserFrom(r)},
		Message: msg, Detail: err.Error(), Code: code,
	})
}
