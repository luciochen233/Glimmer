package server

import (
	"embed"
	"html/template"
	"io"
)

//go:embed all:templates
var templateFS embed.FS

//go:embed all:static
var staticFS embed.FS

var templates *template.Template

func initTemplates() {
	templates = template.Must(template.ParseFS(templateFS, "templates/*.html"))
}

func renderTemplate(w io.Writer, name string, data any) error {
	return templates.ExecuteTemplate(w, name, data)
}
