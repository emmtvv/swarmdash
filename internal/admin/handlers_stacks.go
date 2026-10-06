package admin

import (
	"fmt"
	"net/http"

	"github.com/docker/docker/api/types/swarm"
)

func (s *Server) handleStacksPage(w http.ResponseWriter, r *http.Request) {
	services, err := s.listServices(r.Context())
	if err != nil {
		http.Error(w, "list services: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.render(w, r, "stacks.html", map[string]any{
		"User":       userFromContext(r),
		"Stacks":     groupByStack(services),
		"Standalone": standaloneServices(services),
	})
}

func (s *Server) handleStackDetail(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	services, err := s.listServices(r.Context())
	if err != nil {
		http.Error(w, "list services: "+err.Error(), http.StatusBadGateway)
		return
	}
	var stackServices []any
	for _, svc := range services {
		if stackName(svc) == name {
			stackServices = append(stackServices, svc)
		}
	}
	if len(stackServices) == 0 {
		http.NotFound(w, r)
		return
	}
	versions, err := s.store.ListStackVersions(name)
	if err != nil {
		http.Error(w, "list stack versions: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data := map[string]any{
		"User":     userFromContext(r),
		"Name":     name,
		"Services": stackServices,
		"Versions": versions,
	}
	if gs, ok := s.gitStackFor(name); ok {
		data["GitOps"] = gs
	}
	s.render(w, r, "stack_detail.html", data)
}

// handleStackDelete removes every service belonging to the stack. Docker
// has no atomic "delete stack" API - this mirrors what `docker stack rm`
// does under the hood.
func (s *Server) handleStackDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ctx := r.Context()
	services, err := s.listServices(ctx)
	if err != nil {
		http.Error(w, "list services: "+err.Error(), http.StatusBadGateway)
		return
	}
	for _, svc := range services {
		if stackName(svc) == name {
			if err := s.docker.ServiceRemove(ctx, svc.ID); err != nil {
				http.Error(w, "remove service "+svc.Spec.Name+": "+err.Error(), http.StatusBadGateway)
				return
			}
		}
	}
	s.audit(r, "stack.delete", name, "", nil)
	redirect(w, r, "/stacks")
}

// handleStackRestart force-updates every service in the stack, restarting
// all of their tasks without changing any spec - a bulk version of the
// per-service restart button.
func (s *Server) handleStackRestart(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ctx := r.Context()
	services, err := s.listServices(ctx)
	if err != nil {
		http.Error(w, "list services: "+err.Error(), http.StatusBadGateway)
		return
	}
	restarted := 0
	for _, svc := range services {
		if stackName(svc) != name {
			continue
		}
		full, err := s.getService(ctx, svc.ID)
		if err != nil {
			http.Error(w, "get service "+svc.Spec.Name+": "+err.Error(), http.StatusBadGateway)
			return
		}
		full.Spec.TaskTemplate.ForceUpdate++
		if _, err := s.docker.ServiceUpdate(ctx, full.ID, full.Version, full.Spec, swarm.ServiceUpdateOptions{}); err != nil {
			http.Error(w, "restart service "+full.Spec.Name+": "+err.Error(), http.StatusBadGateway)
			return
		}
		restarted++
	}
	s.audit(r, "stack.restart", name, fmt.Sprintf("services=%d", restarted), nil)
	redirect(w, r, "/stacks/"+name)
}
