package admin

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/docker/docker/api/types/image"
)

// handleImagesPage shows images on a single node at a time (images are
// local to each node's daemon - there's no swarm-wide image list). The
// node picker defaults to whichever node sorts first alphabetically.
func (s *Server) handleImagesPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	nodes, err := s.listNodes(ctx)
	if err != nil {
		http.Error(w, "list nodes: "+err.Error(), http.StatusBadGateway)
		return
	}
	if len(nodes) == 0 {
		http.Error(w, "no nodes found", http.StatusBadGateway)
		return
	}

	nodeID := r.URL.Query().Get("node")
	if nodeID == "" {
		nodeID = nodes[0].ID
	}

	var images []image.Summary
	resp, err := s.agentGet(ctx, nodeID, "/v1/images")
	if err != nil {
		http.Error(w, "agent request: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		_ = json.NewDecoder(resp.Body).Decode(&images)
	}
	sort.Slice(images, func(i, j int) bool { return images[i].Size > images[j].Size })
	page, pagination := paginateSlice(r, images)

	s.render(w, r, "images.html", map[string]any{
		"User":       userFromContext(r),
		"Nodes":      nodes,
		"SelectedID": nodeID,
		"Images":     page,
		"Pagination": pagination,
	})
}

func (s *Server) handleImagesPrune(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	nodeID := r.FormValue("node")
	all := r.FormValue("all")

	path := "/v1/images/prune"
	if all == "on" {
		path += "?all=true"
	}
	resp, err := s.agentPost(ctx, nodeID, path, nil)
	if err != nil {
		http.Error(w, "prune request: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	s.audit(r, "images.prune", nodeID, "all="+all, nil)
	redirect(w, r, "/images?node="+nodeID)
}
