package admin

import (
	"net/http"
	"time"

	"swarmdash/internal/store"
)

func (s *Server) handleWebhooksPage(w http.ResponseWriter, r *http.Request) {
	hooks, err := s.store.ListWebhooks()
	if err != nil {
		http.Error(w, "list webhooks: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, "webhooks.html", map[string]any{
		"User":     userFromContext(r),
		"Webhooks": hooks,
	})
}

func (s *Server) handleWebhookCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	url := r.FormValue("url")
	if name == "" || url == "" {
		http.Error(w, "name and url are required", http.StatusBadRequest)
		return
	}
	hook := store.Webhook{ID: randomToken(8), Name: name, URL: url, CreatedAt: time.Now()}
	if err := s.store.PutWebhook(hook); err != nil {
		http.Error(w, "create webhook: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "webhook.create", name, url, nil)
	redirect(w, r, "/settings/webhooks")
}

func (s *Server) handleWebhookDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteWebhook(id); err != nil {
		http.Error(w, "delete webhook: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "webhook.delete", id, "", nil)
	redirect(w, r, "/settings/webhooks")
}
