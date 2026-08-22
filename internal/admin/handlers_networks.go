package admin

import (
	"net/http"
	"sort"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
)

func (s *Server) handleNetworksPage(w http.ResponseWriter, r *http.Request) {
	nets, err := s.docker.NetworkList(r.Context(), network.ListOptions{
		Filters: filters.NewArgs(filters.Arg("scope", "swarm")),
	})
	if err != nil {
		http.Error(w, "list networks: "+err.Error(), http.StatusBadGateway)
		return
	}
	sort.Slice(nets, func(i, j int) bool { return nets[i].Name < nets[j].Name })
	s.render(w, r, "networks.html", map[string]any{
		"User":     userFromContext(r),
		"Networks": nets,
	})
}

func (s *Server) handleNetworkCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	driver := r.FormValue("driver")
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if driver == "" {
		driver = "overlay"
	}

	_, err := s.docker.NetworkCreate(r.Context(), name, network.CreateOptions{
		Driver:     driver,
		Attachable: r.FormValue("attachable") == "on",
		Internal:   r.FormValue("internal") == "on",
	})
	if err != nil {
		http.Error(w, "create network: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.audit(r, "network.create", name, "driver="+driver, nil)
	redirect(w, r, "/networks")
}

func (s *Server) handleNetworkDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.docker.NetworkRemove(r.Context(), id); err != nil {
		http.Error(w, "remove network: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.audit(r, "network.delete", id, "", nil)
	redirect(w, r, "/networks")
}
