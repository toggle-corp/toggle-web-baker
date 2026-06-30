package server

import (
	"embed"
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
	// reltime renders a timestamp as a relative phrase plus an absolute tooltip,
	// so templates can do <span title="{{.Abs}}">{{.Rel}}</span>.
	"reltime": func(ts string) relTime {
		rel, abs := view.RelativeTime(ts)
		return relTime{Rel: rel, Abs: abs}
	},
}

// relTime pairs a relative phrase with the absolute timestamp for a tooltip.
type relTime struct {
	Rel string
	Abs string
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
