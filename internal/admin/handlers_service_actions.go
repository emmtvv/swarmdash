package admin

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/docker/docker/api/types/swarm"
)

func (s *Server) handleServiceScale(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	replicas, err := strconv.ParseUint(r.FormValue("replicas"), 10, 64)
	if err != nil {
		http.Error(w, "invalid replicas value", http.StatusBadRequest)
		return
	}

	svcName, err := s.doServiceScale(r.Context(), name, replicas)
	if err != nil {
		http.Error(w, "scale service: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.audit(r, "service.scale", svcName, fmt.Sprintf("replicas=%d", replicas), nil)
	redirect(w, r, "/services/"+name)
}

// doServiceScale is the shared implementation behind both the
// single-service scale form and the bulk-scale action.
func (s *Server) doServiceScale(ctx context.Context, nameOrID string, replicas uint64) (name string, err error) {
	svc, err := s.getService(ctx, nameOrID)
	if err != nil {
		return "", fmt.Errorf("get service: %w", err)
	}
	if svc.Spec.Mode.Replicated == nil {
		return svc.Spec.Name, fmt.Errorf("only replicated services can be scaled")
	}
	svc.Spec.Mode.Replicated.Replicas = &replicas
	if _, err := s.docker.ServiceUpdate(ctx, svc.ID, svc.Version, svc.Spec, swarm.ServiceUpdateOptions{}); err != nil {
		return svc.Spec.Name, err
	}
	return svc.Spec.Name, nil
}

func (s *Server) handleServiceUpdateImage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ctx := r.Context()

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	image := r.FormValue("image")
	if image == "" {
		http.Error(w, "image is required", http.StatusBadRequest)
		return
	}

	svc, err := s.getService(ctx, name)
	if err != nil {
		http.Error(w, "get service: "+err.Error(), http.StatusNotFound)
		return
	}
	svc.Spec.TaskTemplate.ContainerSpec.Image = image

	updateOpts := swarm.ServiceUpdateOptions{QueryRegistry: true}
	auth, err := s.encodedRegistryAuthFor(image)
	if err != nil {
		http.Error(w, "registry credential: "+err.Error(), http.StatusInternalServerError)
		return
	}
	updateOpts.EncodedRegistryAuth = auth

	if _, err := s.docker.ServiceUpdate(ctx, svc.ID, svc.Version, svc.Spec, updateOpts); err != nil {
		http.Error(w, "update service: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.log.Info("service image updated", "service", svc.Spec.Name, "image", image, "by", userFromContext(r).Username)
	s.audit(r, "service.update_image", svc.Spec.Name, "image="+image, nil)
	redirect(w, r, "/services/"+name)
}

func (s *Server) handleServiceRollback(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ctx := r.Context()

	svc, err := s.getService(ctx, name)
	if err != nil {
		http.Error(w, "get service: "+err.Error(), http.StatusNotFound)
		return
	}
	if svc.PreviousSpec == nil {
		http.Error(w, "service has no previous version to roll back to", http.StatusBadRequest)
		return
	}

	if _, err := s.docker.ServiceUpdate(ctx, svc.ID, svc.Version, svc.Spec, swarm.ServiceUpdateOptions{Rollback: "previous"}); err != nil {
		http.Error(w, "rollback service: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.log.Info("service rolled back", "service", svc.Spec.Name, "by", userFromContext(r).Username)
	s.audit(r, "service.rollback", svc.Spec.Name, "", nil)
	redirect(w, r, "/services/"+name)
}

// handleServiceRestart forces every task to be recreated without changing
// the service spec, mirroring `docker service update --force`.
func (s *Server) handleServiceRestart(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	svcName, err := s.doServiceRestart(r.Context(), name)
	if err != nil {
		http.Error(w, "restart service: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.audit(r, "service.restart", svcName, "", nil)
	redirect(w, r, "/services/"+name)
}

func (s *Server) doServiceRestart(ctx context.Context, nameOrID string) (name string, err error) {
	svc, err := s.getService(ctx, nameOrID)
	if err != nil {
		return "", fmt.Errorf("get service: %w", err)
	}
	svc.Spec.TaskTemplate.ForceUpdate++
	if _, err := s.docker.ServiceUpdate(ctx, svc.ID, svc.Version, svc.Spec, swarm.ServiceUpdateOptions{}); err != nil {
		return svc.Spec.Name, err
	}
	return svc.Spec.Name, nil
}

// handleServiceUpdateLatest re-resolves the service's current image
// reference against the registry and force-recreates its tasks, the
// equivalent of `docker service update --with-registry-auth --force`.
// This is how a service pinned to a mutable tag (e.g. `:latest`) picks up
// a newer image without the tag itself changing.
func (s *Server) handleServiceUpdateLatest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	svcName, err := s.doServiceUpdateLatest(r.Context(), name)
	if err != nil {
		http.Error(w, "update service: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.log.Info("service updated to latest image", "service", svcName, "by", userFromContext(r).Username)
	s.audit(r, "service.update_latest", svcName, "", nil)
	redirect(w, r, "/services/"+name)
}

func (s *Server) doServiceUpdateLatest(ctx context.Context, nameOrID string) (name string, err error) {
	svc, err := s.getService(ctx, nameOrID)
	if err != nil {
		return "", fmt.Errorf("get service: %w", err)
	}
	svc.Spec.TaskTemplate.ForceUpdate++

	// The running service's image is digest-pinned by the daemon (see
	// imageTag's doc comment in compose_diff.go). Passing that straight back
	// into ServiceUpdate leaves no mutable tag for QueryRegistry to
	// re-resolve, so the daemon just reuses the pinned digest and this
	// silently degrades into a restart. Strip it so a newer image is
	// actually looked up.
	svc.Spec.TaskTemplate.ContainerSpec.Image = imageTag(svc.Spec.TaskTemplate.ContainerSpec.Image)

	auth, err := s.encodedRegistryAuthFor(svc.Spec.TaskTemplate.ContainerSpec.Image)
	if err != nil {
		return svc.Spec.Name, fmt.Errorf("registry credential: %w", err)
	}
	if _, err := s.docker.ServiceUpdate(ctx, svc.ID, svc.Version, svc.Spec, swarm.ServiceUpdateOptions{
		QueryRegistry:       true,
		EncodedRegistryAuth: auth,
	}); err != nil {
		return svc.Spec.Name, err
	}
	return svc.Spec.Name, nil
}

func (s *Server) handleServiceDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	svcName, stack, err := s.doServiceDelete(r.Context(), name)
	if err != nil {
		http.Error(w, "remove service: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.audit(r, "service.delete", svcName, "", nil)

	if stack != "" {
		redirect(w, r, "/stacks/"+stack)
		return
	}
	redirect(w, r, "/services")
}

func (s *Server) doServiceDelete(ctx context.Context, nameOrID string) (name, stack string, err error) {
	svc, err := s.getService(ctx, nameOrID)
	if err != nil {
		return "", "", fmt.Errorf("get service: %w", err)
	}
	if err := s.docker.ServiceRemove(ctx, nameOrID); err != nil {
		return svc.Spec.Name, "", err
	}
	return svc.Spec.Name, stackName(svc), nil
}

// handleServicesBulk applies restart/delete/scale to every service ID
// checked on the services list page. Best-effort: it keeps going on a
// per-service error and reports how many succeeded/failed rather than
// aborting the whole batch.
func (s *Server) handleServicesBulk(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	action := r.FormValue("action")
	ids := r.Form["ids"]
	ctx := r.Context()

	var replicas uint64
	if action == "scale" {
		var err error
		replicas, err = strconv.ParseUint(r.FormValue("replicas"), 10, 64)
		if err != nil {
			http.Error(w, "invalid replicas value", http.StatusBadRequest)
			return
		}
	}

	ok, failed := 0, 0
	for _, id := range ids {
		var name string
		var err error
		switch action {
		case "restart":
			name, err = s.doServiceRestart(ctx, id)
		case "latest":
			name, err = s.doServiceUpdateLatest(ctx, id)
		case "delete":
			name, _, err = s.doServiceDelete(ctx, id)
		case "scale":
			name, err = s.doServiceScale(ctx, id, replicas)
		default:
			http.Error(w, "unknown bulk action", http.StatusBadRequest)
			return
		}
		if err != nil {
			failed++
			s.audit(r, "service.bulk_"+action, name, "", err)
			continue
		}
		ok++
		s.audit(r, "service.bulk_"+action, name, "", nil)
	}

	s.log.Info("bulk service action", "action", action, "ok", ok, "failed", failed, "by", userFromContext(r).Username)
	redirect(w, r, "/services")
}
