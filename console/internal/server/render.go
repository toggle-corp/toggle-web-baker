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
	// mkhead builds a head value for templates (like the error page) that do
	// not carry their own head struct.
	"mkhead": func(title string) head { return head{Title: title} },
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
	// Apps is the filtered, health-ranked row set; Total is the unfiltered
	// count shown in the heading.
	Apps  []view.App
	Total int
	// StatusFacets / GroupChips are the server-rendered filter chip rows; both
	// are computed from the unfiltered set (see handleList).
	StatusFacets []statusFacet
	GroupChips   []groupChip
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

type errorData struct {
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
