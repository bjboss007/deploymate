package httpserver

import (
	"net/http"

	"github.com/a-h/templ"

	"github.com/habibmuhammad/deploymate/web/templates"
)

// render writes a templ component with the given status code.
func render(w http.ResponseWriter, r *http.Request, status int, comp templ.Component) {
	buf := templ.GetBuffer()
	defer templ.ReleaseBuffer(buf)
	if err := comp.Render(r.Context(), buf); err != nil {
		http.Error(w, "render failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// notFoundPage answers 404 with the standalone not-found screen instead of
// the bare "404 page not found" text.
func notFoundPage(w http.ResponseWriter, r *http.Request) {
	render(w, r, http.StatusNotFound, templates.NotFoundPage())
}

// redirectHome is a tiny helper so handlers read consistently.
func redirectHome(w http.ResponseWriter, r *http.Request, msg string) {
	if msg != "" {
		http.Redirect(w, r, "/projects?flash="+msg, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/projects", http.StatusSeeOther)
}
