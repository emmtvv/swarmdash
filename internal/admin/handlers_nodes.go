package admin

import (
	"net/http"

	"github.com/docker/docker/api/types/swarm"

	"swarmdash/internal/httpx"
)

// nodeRow pairs a swarm node with its live resource usage for display -
// embedding swarm.Node lets templates keep using the same field paths
// (.Description.Hostname, .Spec.Role, .ID, ...) they already did.
type nodeRow struct {
	swarm.Node
	CPUUsedCores   float64
	CPUUsedPct     float64
	MemUsedBytes   int64
	MemUsedPct     float64
	DiskUsedBytes  int64
	DiskTotalBytes int64
	DiskUsedPct    float64
	StatsOK        bool
}

func newNodeRow(n swarm.Node, st nodeResourceStats) nodeRow {
	row := nodeRow{
		Node:           n,
		CPUUsedCores:   st.CPUUsedCores,
		MemUsedBytes:   st.MemUsedBytes,
		DiskUsedBytes:  st.DiskUsedBytes,
		DiskTotalBytes: st.DiskTotalBytes,
		StatsOK:        st.Available,
	}
	if nano := n.Description.Resources.NanoCPUs; nano > 0 {
		row.CPUUsedPct = st.CPUUsedCores * 1e9 / float64(nano) * 100
	}
	if mem := n.Description.Resources.MemoryBytes; mem > 0 {
		row.MemUsedPct = float64(st.MemUsedBytes) / float64(mem) * 100
	}
	if st.DiskTotalBytes > 0 {
		row.DiskUsedPct = float64(st.DiskUsedBytes) / float64(st.DiskTotalBytes) * 100
	}
	return row
}

// handleNodesPage renders immediately from cheap Docker API data alone.
// Live CPU/memory usage requires querying every node's agent (a couple of
// seconds fanned out, since each container needs two stats samples a
// second apart - see containerResourceUsage in agent/stats.go), so it's
// left for the page's own JS to fetch asynchronously from /nodes/stats
// rather than blocking the initial render on it.
func (s *Server) handleNodesPage(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.listNodes(r.Context())
	if err != nil {
		http.Error(w, "list nodes: "+err.Error(), http.StatusBadGateway)
		return
	}
	rows := make([]nodeRow, len(nodes))
	for i, n := range nodes {
		rows[i] = newNodeRow(n, nodeResourceStats{})
	}
	s.render(w, r, "nodes.html", map[string]any{
		"User":  userFromContext(r),
		"Nodes": rows,
	})
}

// nodeStatsJSON is the wire shape for the async /nodes/stats and
// /nodes/{id}/stats endpoints the nodes list and detail pages poll after
// their initial (stats-free) render.
type nodeStatsJSON struct {
	OK             bool    `json:"ok"`
	CPUUsedCores   float64 `json:"cpuUsedCores"`
	CPUUsedPct     float64 `json:"cpuUsedPct"`
	MemUsedBytes   int64   `json:"memUsedBytes"`
	MemUsedPct     float64 `json:"memUsedPct"`
	DiskUsedBytes  int64   `json:"diskUsedBytes"`
	DiskTotalBytes int64   `json:"diskTotalBytes"`
	DiskUsedPct    float64 `json:"diskUsedPct"`
}

func toNodeStatsJSON(row nodeRow) nodeStatsJSON {
	return nodeStatsJSON{
		OK:             row.StatsOK,
		CPUUsedCores:   row.CPUUsedCores,
		CPUUsedPct:     row.CPUUsedPct,
		MemUsedBytes:   row.MemUsedBytes,
		MemUsedPct:     row.MemUsedPct,
		DiskUsedBytes:  row.DiskUsedBytes,
		DiskTotalBytes: row.DiskTotalBytes,
		DiskUsedPct:    row.DiskUsedPct,
	}
}

// handleNodesStatsJSON is the async data source for the nodes list page:
// live usage for every ready node, fanned out and keyed by node ID.
func (s *Server) handleNodesStatsJSON(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.listNodes(r.Context())
	if err != nil {
		http.Error(w, "list nodes: "+err.Error(), http.StatusBadGateway)
		return
	}
	stats := s.nodesStats(r.Context(), nodes)
	out := make(map[string]nodeStatsJSON, len(nodes))
	for _, n := range nodes {
		out[n.ID] = toNodeStatsJSON(newNodeRow(n, stats[n.ID]))
	}
	httpx.WriteJSON(w, out)
}

// handleNodeStatsJSON is the async data source for the node detail page's
// usage rings.
func (s *Server) handleNodeStatsJSON(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	node, _, err := s.docker.NodeInspectWithRaw(r.Context(), id)
	if err != nil {
		http.Error(w, "get node: "+err.Error(), http.StatusNotFound)
		return
	}
	var stats nodeResourceStats
	if node.Status.State == swarm.NodeStateReady {
		stats = s.nodeStats(r.Context(), id, node.Description.Hostname)
	}
	httpx.WriteJSON(w, toNodeStatsJSON(newNodeRow(node, stats)))
}

func (s *Server) handleNodeAvailability(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	availability := swarm.NodeAvailability(r.FormValue("availability"))
	switch availability {
	case swarm.NodeAvailabilityActive, swarm.NodeAvailabilityPause, swarm.NodeAvailabilityDrain:
	default:
		http.Error(w, "invalid availability value", http.StatusBadRequest)
		return
	}

	node, _, err := s.docker.NodeInspectWithRaw(ctx, id)
	if err != nil {
		http.Error(w, "get node: "+err.Error(), http.StatusNotFound)
		return
	}
	node.Spec.Availability = availability
	if err := s.docker.NodeUpdate(ctx, id, node.Version, node.Spec); err != nil {
		http.Error(w, "update node: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.audit(r, "node.availability", node.Description.Hostname, string(availability), nil)
	redirect(w, r, "/nodes")
}

func (s *Server) handleNodePromote(w http.ResponseWriter, r *http.Request) {
	s.setNodeRole(w, r, swarm.NodeRoleManager)
}

func (s *Server) handleNodeDemote(w http.ResponseWriter, r *http.Request) {
	s.setNodeRole(w, r, swarm.NodeRoleWorker)
}

func (s *Server) setNodeRole(w http.ResponseWriter, r *http.Request, role swarm.NodeRole) {
	id := r.PathValue("id")
	ctx := r.Context()

	node, _, err := s.docker.NodeInspectWithRaw(ctx, id)
	if err != nil {
		http.Error(w, "get node: "+err.Error(), http.StatusNotFound)
		return
	}
	node.Spec.Role = role
	if err := s.docker.NodeUpdate(ctx, id, node.Version, node.Spec); err != nil {
		http.Error(w, "update node: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.audit(r, "node.role", node.Description.Hostname, string(role), nil)
	redirect(w, r, "/nodes")
}

func (s *Server) handleNodeDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	node, _, err := s.docker.NodeInspectWithRaw(r.Context(), id)
	if err != nil {
		http.Error(w, "get node: "+err.Error(), http.StatusNotFound)
		return
	}
	s.render(w, r, "node_detail.html", map[string]any{
		"User": userFromContext(r),
		"Node": newNodeRow(node, nodeResourceStats{}),
	})
}

func (s *Server) handleNodeLabelAdd(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	key := r.FormValue("key")
	value := r.FormValue("value")
	if key == "" {
		http.Error(w, "label key is required", http.StatusBadRequest)
		return
	}

	node, _, err := s.docker.NodeInspectWithRaw(ctx, id)
	if err != nil {
		http.Error(w, "get node: "+err.Error(), http.StatusNotFound)
		return
	}
	if node.Spec.Labels == nil {
		node.Spec.Labels = map[string]string{}
	}
	node.Spec.Labels[key] = value
	if err := s.docker.NodeUpdate(ctx, id, node.Version, node.Spec); err != nil {
		http.Error(w, "update node: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.audit(r, "node.label.add", node.Description.Hostname, key+"="+value, nil)
	redirect(w, r, "/nodes/"+id)
}

func (s *Server) handleNodeLabelDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	key := r.PathValue("key")
	ctx := r.Context()

	node, _, err := s.docker.NodeInspectWithRaw(ctx, id)
	if err != nil {
		http.Error(w, "get node: "+err.Error(), http.StatusNotFound)
		return
	}
	delete(node.Spec.Labels, key)
	if err := s.docker.NodeUpdate(ctx, id, node.Version, node.Spec); err != nil {
		http.Error(w, "update node: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.audit(r, "node.label.delete", node.Description.Hostname, key, nil)
	redirect(w, r, "/nodes/"+id)
}
