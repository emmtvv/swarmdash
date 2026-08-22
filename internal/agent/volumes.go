package agent

import (
	"encoding/json"
	"net/http"

	"github.com/docker/docker/api/types/volume"

	"swarmdash/internal/httpx"
)

// Volumes, like images, are local to each node's daemon - there's no
// swarm-wide volume object - so this mirrors the images.go pattern of
// letting admin ask a specific node's agent.

func (s *Server) handleVolumes(w http.ResponseWriter, r *http.Request) {
	resp, err := s.docker.VolumeList(r.Context(), volume.ListOptions{})
	if err != nil {
		http.Error(w, "list volumes: "+err.Error(), http.StatusBadGateway)
		return
	}
	httpx.WriteJSON(w, resp.Volumes)
}

func (s *Server) handleVolumeCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Driver string `json:"driver"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	vol, err := s.docker.VolumeCreate(r.Context(), volume.CreateOptions{Name: req.Name, Driver: req.Driver})
	if err != nil {
		http.Error(w, "create volume: "+err.Error(), http.StatusBadGateway)
		return
	}
	httpx.WriteJSON(w, vol)
}

func (s *Server) handleVolumeDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.docker.VolumeRemove(r.Context(), name, false); err != nil {
		http.Error(w, "remove volume: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusOK)
}
