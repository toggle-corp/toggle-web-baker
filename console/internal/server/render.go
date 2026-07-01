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
}

type signedOutData struct {
	Head head
}

type listData struct {
	Head head
	Apps []view.App
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

// logpaneData drives the log pane fragment: the container select, a source
// note, and either the lines or an unavailable message.
type logpaneData struct {
	Namespace   string
	Build       view.Build
	Container   string
	Steps       []view.Step // option list for the container <select>
	SourceNote  string      // "live pod" / "Loki" / "pod (Loki unavailable)"
	Lines       []string
	Unavailable string // non-empty → render this instead of lines
	FetchErr    error  // internal; folded into Unavailable
	Follow      bool   // follow mode: container tracks the active step, always current build
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
