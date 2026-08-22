package web

import (
	"bytes"
	"html/template"
	"log/slog"
	"net/http"
)

type Renderer struct {
	tmpl *template.Template
}

func NewRenderer() *Renderer {
	return &Renderer{tmpl: Templates()}
}

type layoutData struct {
	Data      any
	Content   template.HTML
	CSRFToken string
}

// Render renders the named page template (e.g. "dashboard.html", matching
// its {{define}} name) and wraps it in the shared layout. The layout shows
// the sidebar nav only when data carries a "User" key (map[string]any),
// which is how the login page ends up chrome-free without a separate
// template path.
//
// csrfToken is embedded as a <meta> tag the client-side JS reads to attach
// a hidden field to every plain <form method="post">, so individual page
// handlers don't each need to thread it through their own data. nonce is
// bound to the "cspNonce" template func (via a cheap Clone, since
// html/template funcs can't otherwise vary per call) so inline <script>
// blocks - scattered across page templates, not just the layout - can carry
// the nonce that matches this response's CSP header.
func (r *Renderer) Render(w http.ResponseWriter, page string, data any, csrfToken, nonce string) {
	tmpl, err := r.tmpl.Clone()
	if err != nil {
		slog.Error("template clone failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	tmpl = tmpl.Funcs(template.FuncMap{"cspNonce": func() string { return nonce }})

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, page, data); err != nil {
		slog.Error("template render failed", "template", page, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "layout", layoutData{Data: data, Content: template.HTML(buf.String()), CSRFToken: csrfToken}); err != nil {
		slog.Error("layout render failed", "err", err)
	}
}

// RenderFragment renders a template with no layout wrapper - used for
// htmx partial responses (e.g. the live task table refreshed in place).
func (r *Renderer) RenderFragment(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := r.tmpl.ExecuteTemplate(w, name, data); err != nil {
		slog.Error("fragment render failed", "template", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
