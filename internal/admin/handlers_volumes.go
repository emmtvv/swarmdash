package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sort"

	"github.com/docker/docker/api/types/volume"
)

// handleVolumesPage shows volumes on a single node at a time - like
// images, volumes are local to each node's daemon, no swarm-wide list.
func (s *Server) handleVolumesPage(w http.ResponseWriter, r *http.Request) {
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

	var volumes []*volume.Volume
	resp, err := s.agentGet(ctx, nodeID, "/v1/volumes")
	if err != nil {
		http.Error(w, "agent request: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		_ = json.NewDecoder(resp.Body).Decode(&volumes)
	}
	sort.Slice(volumes, func(i, j int) bool { return volumes[i].Name < volumes[j].Name })
	page, pagination := paginateSlice(r, volumes)

	s.render(w, r, "volumes.html", map[string]any{
		"User":       userFromContext(r),
		"Nodes":      nodes,
		"SelectedID": nodeID,
		"Volumes":    page,
		"Pagination": pagination,
	})
}

func (s *Server) handleVolumeCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	nodeID := r.FormValue("node")
	name := r.FormValue("name")
	driver := r.FormValue("driver")
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	body, _ := json.Marshal(map[string]string{"name": name, "driver": driver})
	resp, err := s.agentPost(ctx, nodeID, "/v1/volumes", bytes.NewReader(body))
	if err != nil {
		http.Error(w, "agent request: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.Error(w, "create volume failed", http.StatusBadGateway)
		return
	}

	s.audit(r, "volume.create", name, "node="+nodeID, nil)
	redirect(w, r, "/volumes?node="+nodeID)
}

func (s *Server) handleVolumeDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	nodeID := r.URL.Query().Get("node")
	name := r.PathValue("name")

	resp, err := s.agentPost(ctx, nodeID, "/v1/volumes/"+name+"/delete", nil)
	if err != nil {
		http.Error(w, "agent request: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	s.audit(r, "volume.delete", name, "node="+nodeID, nil)
	redirect(w, r, "/volumes?node="+nodeID)
}
