package admin

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/swarm"
)

// fileUse is one service's attachment of a secret or config.
type fileUse struct {
	Service string
	Stack   string
	Target  string
}

// fileUsage maps secret and config IDs to the services attaching them,
// read off every service's spec - Docker has no reverse lookup.
type fileUsage struct {
	Secrets map[string][]fileUse
	Configs map[string][]fileUse
}

func (s *Server) fileUsage(ctx context.Context) (fileUsage, error) {
	u := fileUsage{Secrets: map[string][]fileUse{}, Configs: map[string][]fileUse{}}
	services, err := s.listServices(ctx)
	if err != nil {
		return u, fmt.Errorf("list services: %w", err)
	}
	for _, svc := range services {
		cs := svc.Spec.TaskTemplate.ContainerSpec
		if cs == nil {
			continue
		}
		for _, ref := range cs.Secrets {
			use := fileUse{Service: svc.Spec.Name, Stack: stackName(svc)}
			if ref.File != nil {
				use.Target = "/run/secrets/" + ref.File.Name
				if strings.HasPrefix(ref.File.Name, "/") {
					use.Target = ref.File.Name
				}
			}
			u.Secrets[ref.SecretID] = append(u.Secrets[ref.SecretID], use)
		}
		for _, ref := range cs.Configs {
			use := fileUse{Service: svc.Spec.Name, Stack: stackName(svc)}
			if ref.File != nil {
				use.Target = ref.File.Name
				if !strings.HasPrefix(use.Target, "/") {
					use.Target = "/" + use.Target
				}
			}
			u.Configs[ref.ConfigID] = append(u.Configs[ref.ConfigID], use)
		}
	}
	return u, nil
}

// composeStacksAmong returns the stacks (from uses) that have a stored
// compose file - those reference secrets/configs by name, so a rotation
// has to be mirrored in their file or their next deploy switches back.
func (s *Server) composeStacksAmong(uses []fileUse) []string {
	seen := map[string]bool{}
	var out []string
	for _, u := range uses {
		if u.Stack == "" || seen[u.Stack] {
			continue
		}
		seen[u.Stack] = true
		if versions, err := s.store.ListStackVersions(u.Stack); err == nil && len(versions) > 0 {
			out = append(out, u.Stack)
		}
	}
	sort.Strings(out)
	return out
}

var versionSuffix = regexp.MustCompile(`^(.*?)_v(\d+)$`)

// nextVersionName suggests the name a rotated secret/config gets:
// "db_pw" -> "db_pw_v2", "db_pw_v2" -> "db_pw_v3", skipping names that
// already exist.
func nextVersionName(name string, exists func(string) bool) string {
	base, n := name, 1
	if m := versionSuffix.FindStringSubmatch(name); m != nil {
		base = m[1]
		n, _ = strconv.Atoi(m[2])
	}
	for {
		n++
		candidate := fmt.Sprintf("%s_v%d", base, n)
		if !exists(candidate) {
			return candidate
		}
	}
}

// rotationResult reports what a secret/config rotation did, for the
// result banner on the new object's page.
type rotationResult struct {
	OldName   string
	Updated   []string
	Deleted   bool
	DeleteErr string
}

func (s *Server) handleSecretsPage(w http.ResponseWriter, r *http.Request) {
	secrets, err := s.docker.SecretList(r.Context(), swarm.SecretListOptions{})
	if err != nil {
		http.Error(w, "list secrets: "+err.Error(), http.StatusBadGateway)
		return
	}
	usage, err := s.fileUsage(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	sort.Slice(secrets, func(i, j int) bool { return secrets[i].Spec.Name < secrets[j].Spec.Name })
	s.render(w, r, "secrets.html", map[string]any{
		"User":    userFromContext(r),
		"Secrets": secrets,
		"Usage":   usage.Secrets,
	})
}

func (s *Server) handleSecretCreate(w http.ResponseWriter, r *http.Request) {
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

	_, err := s.docker.SecretCreate(r.Context(), swarm.SecretSpec{
		Annotations: swarm.Annotations{Name: name},
		Data:        []byte(data),
	})
	if err != nil {
		http.Error(w, "create secret: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.audit(r, "secret.create", name, "", nil)
	redirect(w, r, "/secrets")
}

func (s *Server) handleSecretDetail(w http.ResponseWriter, r *http.Request) {
	s.renderSecretDetail(w, r, r.PathValue("id"), nil)
}

func (s *Server) renderSecretDetail(w http.ResponseWriter, r *http.Request, id string, result *rotationResult) {
	ctx := r.Context()
	sec, _, err := s.docker.SecretInspectWithRaw(ctx, id)
	if err != nil {
		http.Error(w, "secret not found", http.StatusNotFound)
		return
	}
	usage, err := s.fileUsage(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	uses := usage.Secrets[sec.ID]
	s.render(w, r, "secret_detail.html", map[string]any{
		"User":          userFromContext(r),
		"Secret":        sec,
		"Uses":          uses,
		"ComposeStacks": s.composeStacksAmong(uses),
		"SuggestedName": nextVersionName(sec.Spec.Name, func(n string) bool { _, err := s.findSecretByName(ctx, n); return err == nil }),
		"Result":        result,
	})
}

// handleSecretRotate replaces a secret with a new value. Swarm secrets are
// immutable, so this creates a new secret (default name: the next "_vN"),
// repoints every service using the old one at it - keeping each
// attachment's target path, so nothing inside the containers moves - and,
// if asked and every service switched over, deletes the old secret.
func (s *Server) handleSecretRotate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	old, _, err := s.docker.SecretInspectWithRaw(ctx, r.PathValue("id"))
	if err != nil {
		http.Error(w, "secret not found", http.StatusNotFound)
		return
	}
	data := r.FormValue("data")
	if data == "" {
		http.Error(w, "new value is required", http.StatusBadRequest)
		return
	}
	newName := strings.TrimSpace(r.FormValue("new_name"))
	if newName == "" {
		newName = nextVersionName(old.Spec.Name, func(n string) bool { _, err := s.findSecretByName(ctx, n); return err == nil })
	}

	created, err := s.docker.SecretCreate(ctx, swarm.SecretSpec{
		Annotations: swarm.Annotations{Name: newName, Labels: old.Spec.Labels},
		Data:        []byte(data),
	})
	if err != nil {
		s.audit(r, "secret.rotate", old.Spec.Name, "", err)
		http.Error(w, "create secret: "+err.Error(), http.StatusBadGateway)
		return
	}

	result := &rotationResult{OldName: old.Spec.Name}
	usage, err := s.fileUsage(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	for _, use := range usage.Secrets[old.ID] {
		if err := s.repointService(ctx, use.Service, func(cs *swarm.ContainerSpec) {
			for _, ref := range cs.Secrets {
				if ref.SecretID == old.ID {
					ref.SecretID, ref.SecretName = created.ID, newName
				}
			}
		}); err != nil {
			err = fmt.Errorf("created secret %q, but switching service %q to it failed (services before it were switched): %w", newName, use.Service, err)
			s.audit(r, "secret.rotate", old.Spec.Name, "new="+newName, err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		result.Updated = append(result.Updated, use.Service)
	}

	if r.FormValue("delete_old") == "on" {
		if err := s.docker.SecretRemove(ctx, old.ID); err != nil {
			result.DeleteErr = err.Error()
		} else {
			result.Deleted = true
		}
	}
	s.audit(r, "secret.rotate", old.Spec.Name, fmt.Sprintf("new=%s services=%d deleted_old=%v", newName, len(result.Updated), result.Deleted), nil)
	s.renderSecretDetail(w, r, created.ID, result)
}

// repointService re-reads a service's current spec, lets edit change its
// container spec, and applies it - a fresh read per service, so the update
// isn't rejected for a stale version.
func (s *Server) repointService(ctx context.Context, name string, edit func(*swarm.ContainerSpec)) error {
	svc, err := s.getService(ctx, name)
	if err != nil {
		return err
	}
	if svc.Spec.TaskTemplate.ContainerSpec == nil {
		return fmt.Errorf("service has no container spec")
	}
	edit(svc.Spec.TaskTemplate.ContainerSpec)
	_, err = s.docker.ServiceUpdate(ctx, svc.ID, svc.Version, svc.Spec, swarm.ServiceUpdateOptions{})
	return err
}

func (s *Server) handleSecretDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.docker.SecretRemove(r.Context(), id); err != nil {
		http.Error(w, "remove secret: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.audit(r, "secret.delete", id, "", nil)
	redirect(w, r, "/secrets")
}
