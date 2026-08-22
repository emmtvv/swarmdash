package admin

import (
	"net/http"
	"sort"

	"github.com/docker/docker/api/types/swarm"
)

func (s *Server) handleSecretsPage(w http.ResponseWriter, r *http.Request) {
	secrets, err := s.docker.SecretList(r.Context(), swarm.SecretListOptions{})
	if err != nil {
		http.Error(w, "list secrets: "+err.Error(), http.StatusBadGateway)
		return
	}
	sort.Slice(secrets, func(i, j int) bool { return secrets[i].Spec.Name < secrets[j].Spec.Name })
	s.render(w, r, "secrets.html", map[string]any{
		"User":    userFromContext(r),
		"Secrets": secrets,
	})
}

func (s *Server) handleSecretCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	data := r.FormValue("data")
	if name == "" || data == "" {
		http.Error(w, "name and data are required", http.StatusBadRequest)
		return
	}

	_, err := s.docker.SecretCreate(r.Context(), swarm.SecretSpec{
		Annotations: swarm.Annotations{Name: name},
		Data:        []byte(data),
	})
	if err != nil {
		http.Error(w, "create secret: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.audit(r, "secret.create", name, "", nil)
	redirect(w, r, "/secrets")
}

func (s *Server) handleSecretDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.docker.SecretRemove(r.Context(), id); err != nil {
		http.Error(w, "remove secret: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.audit(r, "secret.delete", id, "", nil)
	redirect(w, r, "/secrets")
}
