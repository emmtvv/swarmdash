package admin

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/docker/docker/api/types/swarm"
)

func (s *Server) handleConfigsPage(w http.ResponseWriter, r *http.Request) {
	configs, err := s.docker.ConfigList(r.Context(), swarm.ConfigListOptions{})
	if err != nil {
		http.Error(w, "list configs: "+err.Error(), http.StatusBadGateway)
		return
	}
	usage, err := s.fileUsage(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	sort.Slice(configs, func(i, j int) bool { return configs[i].Spec.Name < configs[j].Spec.Name })
	s.render(w, r, "configs.html", map[string]any{
		"User":    userFromContext(r),
		"Configs": configs,
		"Usage":   usage.Configs,
	})
}

func (s *Server) handleConfigCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	data := r.FormValue("data")
	if name == "" || data == "" {
		http.Error(w, "name and data are required", http.StatusBadRequest)
		return
	}

	_, err := s.docker.ConfigCreate(r.Context(), swarm.ConfigSpec{
		Annotations: swarm.Annotations{Name: name},
		Data:        []byte(data),
	})
	if err != nil {
		http.Error(w, "create config: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.audit(r, "config.create", name, "", nil)
	redirect(w, r, "/configs")
}

// handleConfigDetail shows a config's content (configs, unlike secrets,
// can be read back through the API), the services using it, and the
// edit form - see handleConfigRotate.
func (s *Server) handleConfigDetail(w http.ResponseWriter, r *http.Request) {
	s.renderConfigDetail(w, r, r.PathValue("id"), nil)
}

func (s *Server) renderConfigDetail(w http.ResponseWriter, r *http.Request, id string, result *rotationResult) {
	ctx := r.Context()
	cfg, _, err := s.docker.ConfigInspectWithRaw(ctx, id)
	if err != nil {
		http.Error(w, "config not found", http.StatusNotFound)
		return
	}
	usage, err := s.fileUsage(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	uses := usage.Configs[cfg.ID]
	s.render(w, r, "config_detail.html", map[string]any{
		"User":          userFromContext(r),
		"Config":        cfg,
		"Content":       string(cfg.Spec.Data),
		"Uses":          uses,
		"ComposeStacks": s.composeStacksAmong(uses),
		"SuggestedName": nextVersionName(cfg.Spec.Name, func(n string) bool { _, err := s.findConfigByName(ctx, n); return err == nil }),
		"Result":        result,
	})
}

// handleConfigRotate "edits" a config. Swarm configs are immutable, so -
// exactly like handleSecretRotate - this creates a new config with the
// edited content, repoints every service using the old one at it (keeping
// each attachment's target path), and optionally deletes the old one.
func (s *Server) handleConfigRotate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	old, _, err := s.docker.ConfigInspectWithRaw(ctx, r.PathValue("id"))
	if err != nil {
		http.Error(w, "config not found", http.StatusNotFound)
		return
	}
	data := r.FormValue("data")
	if data == "" {
		http.Error(w, "content is required", http.StatusBadRequest)
		return
	}
	if data == string(old.Spec.Data) {
		http.Error(w, "content is unchanged", http.StatusBadRequest)
		return
	}
	newName := strings.TrimSpace(r.FormValue("new_name"))
	if newName == "" {
		newName = nextVersionName(old.Spec.Name, func(n string) bool { _, err := s.findConfigByName(ctx, n); return err == nil })
	}

	created, err := s.docker.ConfigCreate(ctx, swarm.ConfigSpec{
		Annotations: swarm.Annotations{Name: newName, Labels: old.Spec.Labels},
		Data:        []byte(data),
		Templating:  old.Spec.Templating,
	})
	if err != nil {
		s.audit(r, "config.rotate", old.Spec.Name, "", err)
		http.Error(w, "create config: "+err.Error(), http.StatusBadGateway)
		return
	}

	result := &rotationResult{OldName: old.Spec.Name}
	usage, err := s.fileUsage(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	for _, use := range usage.Configs[old.ID] {
		if err := s.repointService(ctx, use.Service, func(cs *swarm.ContainerSpec) {
			for _, ref := range cs.Configs {
				if ref.ConfigID == old.ID {
					ref.ConfigID, ref.ConfigName = created.ID, newName
				}
			}
		}); err != nil {
			err = fmt.Errorf("created config %q, but switching service %q to it failed (services before it were switched): %w", newName, use.Service, err)
			s.audit(r, "config.rotate", old.Spec.Name, "new="+newName, err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		result.Updated = append(result.Updated, use.Service)
	}

	if r.FormValue("delete_old") == "on" {
		if err := s.docker.ConfigRemove(ctx, old.ID); err != nil {
			result.DeleteErr = err.Error()
		} else {
			result.Deleted = true
		}
	}
	s.audit(r, "config.rotate", old.Spec.Name, fmt.Sprintf("new=%s services=%d deleted_old=%v", newName, len(result.Updated), result.Deleted), nil)
	s.renderConfigDetail(w, r, created.ID, result)
}

func (s *Server) handleConfigDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.docker.ConfigRemove(r.Context(), id); err != nil {
		http.Error(w, "remove config: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.audit(r, "config.delete", id, "", nil)
	redirect(w, r, "/configs")
}
