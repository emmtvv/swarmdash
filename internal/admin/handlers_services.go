package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
)

// errServiceNotFound distinguishes "no such service" from other lookup
// failures in loadServiceDetailData, so callers can map it to a 404 instead
// of a generic 502.
var errServiceNotFound = errors.New("service not found")

func (s *Server) handleServicesPage(w http.ResponseWriter, r *http.Request) {
	services, err := s.listServices(r.Context())
	if err != nil {
		http.Error(w, "list services: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.render(w, r, "services.html", map[string]any{
		"User":     userFromContext(r),
		"Services": services,
	})
}

func (s *Server) handleServiceDetail(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	data, err := s.loadServiceDetailData(r.Context(), name)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, errServiceNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	data["User"] = userFromContext(r)
	s.render(w, r, "service_detail.html", data)
}

// loadServiceDetailData gathers everything service_detail.html needs.
// Factored out of handleServiceDetail so other handlers that need to
// re-render the page (e.g. creating a deploy webhook) don't duplicate the
// tasks/hostnames/replicas lookups.
func (s *Server) loadServiceDetailData(ctx context.Context, name string) (map[string]any, error) {
	svc, err := s.getService(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("get service: %w: %w", errServiceNotFound, err)
	}
	tasks, err := s.listTasksForService(ctx, svc.ID)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}

	// Resolve node hostnames for display so tasks read "worker-2" instead
	// of a bare node ID.
	nodes, err := s.listNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	hostnames := make(map[string]string, len(nodes))
	for _, n := range nodes {
		hostnames[n.ID] = n.Description.Hostname
	}

	replicas := uint64(0)
	if svc.Spec.Mode.Replicated != nil && svc.Spec.Mode.Replicated.Replicas != nil {
		replicas = *svc.Spec.Mode.Replicated.Replicas
	}

	deployHooks, err := s.store.ListDeployHooksForService(svc.Spec.Name)
	if err != nil {
		return nil, fmt.Errorf("list deploy hooks: %w", err)
	}

	// Attached networks are stored on the spec by ID, not the name the
	// user typed when attaching one - resolve them back so the editor
	// textarea round-trips names instead of raw IDs.
	nets, err := s.docker.NetworkList(ctx, network.ListOptions{Filters: filters.NewArgs(filters.Arg("scope", "swarm"))})
	if err != nil {
		return nil, fmt.Errorf("list networks: %w", err)
	}
	netNames := make(map[string]string, len(nets))
	for _, n := range nets {
		netNames[n.ID] = n.Name
	}

	return map[string]any{
		"Service":     svc,
		"Tasks":       tasks,
		"Hostnames":   hostnames,
		"Replicas":    replicas,
		"IsGlobal":    svc.Spec.Mode.Global != nil,
		"Editor":      serviceEditorForm(svc, netNames),
		"DeployHooks": deployHooks,
	}, nil
}
