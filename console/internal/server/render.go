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
}

// head is the data the shared layout head/header needs.
type head struct {
	Title string
	User  string
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
