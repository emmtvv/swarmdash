package agent

import (
	"net/http"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"

	"swarmdash/internal/httpx"
)

func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
	images, err := s.docker.ImageList(r.Context(), image.ListOptions{})
	if err != nil {
		http.Error(w, "list images: "+err.Error(), http.StatusBadGateway)
		return
	}
	httpx.WriteJSON(w, images)
}

// handleImagesPrune removes dangling (untagged) images by default; pass
// ?all=true to remove every image not used by a running container.
func (s *Server) handleImagesPrune(w http.ResponseWriter, r *http.Request) {
	dangling := "true"
	if r.URL.Query().Get("all") == "true" {
		dangling = "false"
	}
	report, err := s.docker.ImagesPrune(r.Context(), filters.NewArgs(filters.Arg("dangling", dangling)))
	if err != nil {
		http.Error(w, "prune images: "+err.Error(), http.StatusBadGateway)
		return
	}
	httpx.WriteJSON(w, report)
}
