package admin

import (
	"net/http"
)

func (s *Server) handleTokensPage(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.store.ListAPITokens()
	if err != nil {
		http.Error(w, "list tokens: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, "tokens.html", map[string]any{
		"User":   userFromContext(r),
		"Tokens": tokens,
	})
}

func (s *Server) handleTokenCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	if name == "" {
		name = "unnamed"
	}
	role := r.FormValue("role")
	if role == "" {
		role = "admin"
	}
	if !validRole(role) {
		http.Error(w, "role must be admin or viewer", http.StatusBadRequest)
		return
	}

	plaintext, rec := newAPIToken(name, role, userFromContext(r).Username)
	if err := s.store.PutAPIToken(rec); err != nil {
		http.Error(w, "create token: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "token.create", name, "role="+role, nil)

	tokens, err := s.store.ListAPITokens()
	if err != nil {
		http.Error(w, "list tokens: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, "tokens.html", map[string]any{
		"User":     userFromContext(r),
		"Tokens":   tokens,
		"NewToken": plaintext,
	})
}

func (s *Server) handleTokenDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteAPIToken(id); err != nil {
		http.Error(w, "delete token: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "token.delete", id, "", nil)
	redirect(w, r, "/settings/tokens")
}
