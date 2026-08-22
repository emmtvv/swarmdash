package admin

import (
	"net/http"
	"sort"

	"github.com/docker/docker/api/types/swarm"
)

func (s *Server) handleConfigsPage(w http.ResponseWriter, r *http.Request) {
	configs, err := s.docker.ConfigList(r.Context(), swarm.ConfigListOptions{})
	if err != nil {
		http.Error(w, "list configs: "+err.Error(), http.StatusBadGateway)
		return
	}
	sort.Slice(configs, func(i, j int) bool { return configs[i].Spec.Name < configs[j].Spec.Name })
	s.render(w, r, "configs.html", map[string]any{
		"User":    userFromContext(r),
		"Configs": configs,
	})
}

func (s *Server) handleConfigCreate(w http.ResponseWriter, r *http.Request) {
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

	_, err := s.docker.ConfigCreate(r.Context(), swarm.ConfigSpec{
		Annotations: swarm.Annotations{Name: name},
		Data:        []byte(data),
	})
	if err != nil {
		http.Error(w, "create config: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.audit(r, "config.create", name, "", nil)
	redirect(w, r, "/configs")
}

func (s *Server) handleConfigDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.docker.ConfigRemove(r.Context(), id); err != nil {
		http.Error(w, "remove config: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.audit(r, "config.delete", id, "", nil)
	redirect(w, r, "/configs")
}
