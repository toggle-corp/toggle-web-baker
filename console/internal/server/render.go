package server

import (
	"embed"
	"fmt"
	"html/template"
	"log"
	"net/http"

	"github.com/toggle-corp/toggle-web-baker/console/internal/view"
)

//go:embed templates/*.gohtml
var templateFS embed.FS

// templates is parsed once at startup; html/template auto-escapes all data, so
// status fields coming from the cluster are safe to render directly.
var templates = template.Must(
	template.New("console").Funcs(funcMap).ParseFS(templateFS, "templates/*.gohtml"),
)

var funcMap = template.FuncMap{
	// dash renders an em-dash for empty strings so blank status fields read as
	// "absent" rather than looking like a layout bug.
	"dash": func(s string) string {
		if s == "" {
			return "—"
		}
		return s
	},
	// timetag / timetagFull emit a <time datetime="UTC"> element carrying the
	// raw RFC3339-UTC instant. The browser (bakerLocalizeTimes in layout.gohtml)
	// rewrites the text to the viewer's local timezone; with JS off the UTC
	// string shows as-is. timetagFull marks the element data-format="full" so the
	// client renders an inline absolute time alongside the relative delta (used
	// for "Next scheduled"). Empty input renders an em-dash.
	"timetag":     func(ts string) template.HTML { return timeTag(ts, false) },
	"timetagFull": func(ts string) template.HTML { return timeTag(ts, true) },
	// loghl wraps one log line in a severity-classed span (escaping its content),
	// so the log pane can colorize plain-text output. See loghl.go.
	"loghl": highlightLogLine,
	// shortSHA abbreviates a commit SHA to the conventional 7 characters.
	"shortSHA": view.ShortSHA,
	// linkctx bundles a URL + visible text for the commitlink sub-template.
	"linkctx": func(url, text string) linkView { return linkView{URL: url, Text: text} },
	// cleanupCtx bundles the App, the action kind, and the action status into one
	// value so the cleanupaction sub-template can render a prune row + button.
	"cleanupCtx": func(app view.App, kind string, action view.CleanupAction) cleanupView {
		return cleanupView{App: app, Kind: kind, Action: action}
	},
}

// cleanupView is the input to the cleanupaction sub-template.
type cleanupView struct {
	App    view.App
	Kind   string // "cache" | "releases"
	Action view.CleanupAction
}

// linkView is the input to the commitlink sub-template.
type linkView struct {
	URL  string
	Text string
}

// timeTag renders a <time> element carrying an RFC3339-UTC instant as both the
// datetime attribute and the fallback text; bakerLocalizeTimes() localizes it
// in the browser. full=true adds data-format="full" (inline absolute time).
func timeTag(ts string, full bool) template.HTML {
	if ts == "" {
		return template.HTML("—")
	}
	esc := template.HTMLEscapeString(ts)
	attr := ""
	if full {
		attr = ` data-format="full"`
	}
	return template.HTML(fmt.Sprintf(`<time datetime="%s"%s>%s</time>`, esc, attr, esc))
}

// head is the data the shared layout head/header needs.
type head struct {
	Title string
	User  string
	// Anon marks a public, logged-out page (e.g. /signed-out) so the header
	// suppresses the "no user header" badge that would otherwise scream a
	// misconfiguration on a page that is intentionally userless.
	Anon bool
	// Bare suppresses the standard header entirely; the detail page renders its
	// own sticky status bar (breadcrumb + actions + theme select) instead.
	Bare bool
}

type signedOutData struct {
	Head head
}

type listData struct {
	Head head
	// Warming is true when the informer cache has not synced yet: the template
	// then shows a "cache warming up" notice INSTEAD of the table (an empty table
	// would look like a healthy empty cluster). The filter/pagination fields are
	// left zero in this mode — there is nothing to show yet.
	Warming bool
	// Stale is true when synced but the watch is currently erroring, so the cached
	// list may be out of date: the template renders a banner above the table.
	Stale bool
	// Apps is the filtered, health-ranked row set; Total is the unfiltered
	// count. Matched is len(filtered) — the count AFTER status/group/search but
	// BEFORE pagination, which is also the population the storage roll-up spans.
	// Filtered is true when a filter/search narrowed the set (Matched != Total);
	// the heading then reads "Matched of Total" instead of just Total.
	Apps     []view.App
	Total    int
	Matched  int
	Filtered bool
	// StatusFacets / GroupChips are the server-rendered filter chip rows; both
	// are computed from the unfiltered set (see handleList).
	StatusFacets []statusFacet
	GroupChips   []groupChip
	// Storage is the storage roll-up over the FILTERED, PRE-pagination set (see
	// handleList), rendered in the heading after the count. Its *Human methods
	// keep the template logic-free.
	Storage view.StorageTotals
	// Search is the active search term (echoed into the input and empty-state
	// copy); ClearSearchURL drops search while keeping status/group. Status /
	// Group are the active filters, carried as hidden inputs so submitting a
	// search preserves them.
	Search         string
	ClearSearchURL string
	Status         string
	Group          string
	// Sort is the active order ("" = most-broken-first, "name" = namespace/name);
	// SortToggleURL flips it while keeping the active filters/search. The heading
	// renders the current order as a link to the other one.
	Sort          string
	SortToggleURL string
	// Pagination controls (window applied after filter+search+sort). Page is the
	// clamped 1-based current page; TotalPages is ceil(match/pageSize), min 1.
	// ShowPager gates the whole controls block (TotalPages>1). HasPrev/HasNext
	// grey the ends; PrevURL/NextURL carry the active filters plus the target page.
	Page       int
	TotalPages int
	ShowPager  bool
	HasPrev    bool
	HasNext    bool
	PrevURL    string
	NextURL    string
}

type detailData struct {
	Head      head
	App       view.App
	Requested bool // true when redirected here right after a rebuild POST
	// CleanupRequested is "cache" / "releases" when redirected here right after a
	// cleanup POST, driving the matching banner; empty otherwise.
	CleanupRequested string
}

// partialData drives the pollable detail region fragment (flow + recent builds
// + storage).
type partialData struct {
	App view.App
}

// logpaneData drives the log pane fragment: the container badge buttons, a
// source note, and either the lines or an unavailable message.
type logpaneData struct {
	Namespace   string
	Build       view.Build
	Container   string
	Steps       []view.Step // the container badge-button row
	SourceNote  string      // "live pod" / "Loki" / "pod (Loki unavailable)"
	Lines       []string
	Unavailable string // non-empty → render this instead of lines
	FetchErr    error  // internal; folded into Unavailable
	Follow      bool   // follow mode: container tracks the active step, always current build
	// AppActive reports whether a build is currently in flight, gating the
	// "follow active step" toggle — following is meaningless when nothing runs.
	AppActive bool
	// IsCurrent reports whether the displayed build is the app's current build
	// (vs a finished history entry); false drives the "viewing <job>" indicator.
	IsCurrent bool
}

// manifestData drives the read-only manifest page. Highlighted is the only
// copy of the YAML shipped to the browser (its textContent is byte-identical
// to the marshaled text, so the Copy button reads it back rather than the page
// carrying the document twice). HasHidden gates the security note (masked
// annotation values and/or the inline auth credential).
type manifestData struct {
	Head        head
	Namespace   string
	Name        string
	Highlighted template.HTML
	HasHidden   bool
}

// errorData carries its own Head (with the request's user) so an error page
// never renders the "no user header" misconfiguration badge to a signed-in
// user who merely hit a 404/502.
type errorData struct {
	Head    head
	Message string
	Detail  string
	Code    int
}

func render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, name+".gohtml", data); err != nil {
		log.Printf("console: render %s: %v", name, err)
	}
}
