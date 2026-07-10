package web

import (
	"embed"
	"html/template"
	"io"
	"net/http"
	"path/filepath"
)

// TemplatesFS is seeded from main.go with the embedded templates directory
var TemplatesFS embed.FS

// RenderTemplate parses and renders a template with the main layout
func RenderTemplate(w io.Writer, page string, data any) error {
	// In production, templates are read from embedded FS
	tmpl, err := template.New("layout.html").ParseFS(
		TemplatesFS,
		"web/templates/layout.html",
		"web/templates/partials/header.html",
		"web/templates/partials/footer.html",
		filepath.Join("web/templates/pages", page),
	)
	if err != nil {
		return err
	}

	return tmpl.Execute(w, data)
}

// InternalError renders a basic 500 message if template rendering itself fails
func InternalError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write([]byte("500 Internal Server Error - Template compilation failed."))
}
