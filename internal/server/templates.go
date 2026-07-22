package server

import (
	"embed"
	"fmt"
	"html/template"
	"io"
)

//go:embed all:templates
var templateFS embed.FS

//go:embed all:static
var staticFS embed.FS

var templateMap map[string]*template.Template

func initTemplates() {
	templateMap = make(map[string]*template.Template)

	// Pages that use the base layout (sidebar if logged in, public header otherwise).
	// Each gets its own parsed template set so their {{define "content"}} blocks don't collide.
	layoutPages := []string{
		"index.html",
		"admin.html",
		"admin_edit.html",
		"admin_bin.html",
		"admin_bin_edit.html",
		"admin_uploads.html",
		"admin_log.html",
		"bin_view.html",
		"bin_token.html",
	}

	for _, page := range layoutPages {
		t := template.Must(
			template.New("").ParseFS(templateFS, "templates/base.html", "templates/"+page),
		)
		templateMap[page] = t
	}

	// Standalone pages (no base layout wrapper) — render themselves directly.
	standalonePages := []string{
		"admin_login.html",
		"404.html",
		"bin_view_embed.html", // for ?embed=1 iframe preview
	}
	for _, page := range standalonePages {
		t := template.Must(
			template.New("").ParseFS(templateFS, "templates/"+page),
		)
		templateMap[page] = t
	}
}

func renderTemplate(w io.Writer, name string, data any) error {
	t, ok := templateMap[name]
	if !ok {
		return fmt.Errorf("template %q not found", name)
	}
	// Layout pages render via the base template (which calls {{template "content" .}}).
	// Standalone pages render by their own filename.
	standalone := map[string]bool{
		"admin_login.html":     true,
		"404.html":             true,
		"bin_view_embed.html":  true,
	}
	if standalone[name] {
		return t.ExecuteTemplate(w, name, data)
	}
	return t.ExecuteTemplate(w, "base", data)
}
