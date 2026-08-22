package admin

import (
	"encoding/json"
	"html/template"
	"net/http"
	"sort"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/swarm"
)

// topologyNode is either a service or an overlay network in the cluster
// topology graph rendered by topology.js.
type topologyNode struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Type    string `json:"type"` // "service" | "network"
	Stack   string `json:"stack,omitempty"`
	Mode    string `json:"mode,omitempty"`    // service only: "replicated" | "global"
	Running uint64 `json:"running"`           // service only
	Desired uint64 `json:"desired"`           // service only
	Ingress bool   `json:"ingress,omitempty"` // network only
}

type topologyEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

type topologyGraph struct {
	Nodes []topologyNode `json:"nodes"`
	Edges []topologyEdge `json:"edges"`
}

func (s *Server) handleTopologyPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	nets, err := s.docker.NetworkList(ctx, network.ListOptions{
		Filters: filters.NewArgs(filters.Arg("scope", "swarm")),
	})
	if err != nil {
		http.Error(w, "list networks: "+err.Error(), http.StatusBadGateway)
		return
	}
	services, err := s.listServices(ctx)
	if err != nil {
		http.Error(w, "list services: "+err.Error(), http.StatusBadGateway)
		return
	}

	graph, isolated := buildTopologyGraph(nets, services)
	graphJSON, err := json.Marshal(graph)
	if err != nil {
		http.Error(w, "encode graph: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.render(w, r, "topology.html", map[string]any{
		"User":      userFromContext(r),
		"GraphJSON": template.JS(graphJSON),
		"Isolated":  isolated,
		"HasGraph":  len(graph.Nodes) > 0,
	})
}

// buildTopologyGraph turns the raw network/service lists into the
// service<->network bipartite graph the topology map renders: an edge means
// "this service is attached to this overlay network", so two services end
// up visually connected whenever they share one. Services with no overlay
// attachment (host-network-only, or attached to a local bridge network
// only) are returned separately since they'd otherwise render as
// disconnected dots with no edges to explain.
func buildTopologyGraph(nets []network.Summary, services []swarm.Service) (topologyGraph, []swarm.Service) {
	overlay := make(map[string]network.Summary, len(nets))
	for _, n := range nets {
		if n.Driver == "overlay" {
			overlay[n.ID] = n
		}
	}

	graph := topologyGraph{}
	usedNetworks := make(map[string]bool)
	var isolated []swarm.Service

	for _, svc := range services {
		mode := "global"
		if svc.Spec.Mode.Replicated != nil {
			mode = "replicated"
		}
		var running, desired uint64
		if svc.ServiceStatus != nil {
			running = svc.ServiceStatus.RunningTasks
			desired = svc.ServiceStatus.DesiredTasks
		}

		svcNodeID := "svc:" + svc.ID
		graph.Nodes = append(graph.Nodes, topologyNode{
			ID:      svcNodeID,
			Label:   svc.Spec.Name,
			Type:    "service",
			Stack:   stackName(svc),
			Mode:    mode,
			Running: running,
			Desired: desired,
		})

		attached := false
		for _, netID := range serviceOverlayNetworkIDs(svc) {
			n, ok := overlay[netID]
			if !ok {
				continue
			}
			attached = true
			usedNetworks[n.ID] = true
			graph.Edges = append(graph.Edges, topologyEdge{Source: svcNodeID, Target: "net:" + n.ID})
		}
		if !attached {
			isolated = append(isolated, svc)
		}
	}

	for _, n := range nets {
		if !usedNetworks[n.ID] {
			continue
		}
		graph.Nodes = append(graph.Nodes, topologyNode{
			ID:      "net:" + n.ID,
			Label:   n.Name,
			Type:    "network",
			Ingress: n.Ingress,
		})
	}

	sort.Slice(isolated, func(i, j int) bool { return isolated[i].Spec.Name < isolated[j].Spec.Name })
	return graph, isolated
}

// serviceOverlayNetworkIDs collects the network IDs a service is attached
// to, preferring TaskSpec.Networks (the current field) and falling back to
// the deprecated ServiceSpec-level Networks so specs written against older
// compose/API versions still show up.
func serviceOverlayNetworkIDs(svc swarm.Service) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, n := range svc.Spec.TaskTemplate.Networks {
		add(n.Target)
	}
	for _, n := range svc.Spec.Networks { //nolint:staticcheck // SA1019: deprecated fallback is the point, see doc comment above
		add(n.Target)
	}
	return out
}
