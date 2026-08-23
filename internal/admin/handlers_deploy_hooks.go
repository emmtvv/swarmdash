package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/distribution/reference"
	"github.com/docker/docker/api/types/swarm"

	"swarmdash/internal/store"
)

// handleDeployHookCreate mints a new CI/CD deploy webhook scoped to one
// service and re-renders the service detail page with the plaintext URL
// shown once - same one-time-reveal convention as API tokens
// (handlers_tokens.go): only the hash is ever persisted.
func (s *Server) handleDeployHookCreate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ctx := r.Context()

	svc, err := s.getService(ctx, name)
	if err != nil {
		http.Error(w, "get service: "+err.Error(), http.StatusNotFound)
		return
	}

	token := "sdh_" + randomToken(24)
	hook := store.DeployHook{
		ID:                 randomToken(8),
		Hash:               hashToken(token),
		ServiceName:        svc.Spec.Name,
		CreatedBy:          userFromContext(r).Username,
		CreatedAt:          time.Now(),
		AllowImageOverride: r.FormValue("allow_image_override") == "on",
	}
	if err := s.store.PutDeployHook(hook); err != nil {
		http.Error(w, "create deploy hook: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "deploy_hook.create", svc.Spec.Name, "", nil)

	data, err := s.loadServiceDetailData(ctx, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	data["User"] = userFromContext(r)
	data["NewDeployHookURL"] = deployHookURL(r, token)
	s.render(w, r, "service_detail.html", data)
}

func (s *Server) handleDeployHookDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	id := r.PathValue("id")
	if err := s.store.DeleteDeployHook(id); err != nil {
		http.Error(w, "delete deploy hook: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "deploy_hook.delete", name, id, nil)
	redirect(w, r, "/services/"+name)
}

// sameImageRepo reports whether current and candidate name the same image
// repository (ignoring tag/digest) once both are normalized the way Docker
// itself normalizes references - e.g. "nginx" and "docker.io/library/nginx:1.27"
// are the same repo. Used to keep an unauthenticated deploy-hook caller
// (see handleDeployHookTrigger) from swapping a service to a completely
// different, potentially attacker-controlled image via the optional
// {"image": ...} body: without AllowImageOverride explicitly set on the
// hook, only a same-repo tag/digest change is permitted. A candidate that
// fails to parse as a reference at all is rejected (returns false) rather
// than erroring the request differently, so malformed input hits the same
// "forbidden" response as a deliberate cross-repo swap.
func sameImageRepo(current, candidate string) bool {
	a, err := reference.ParseNormalizedNamed(current)
	if err != nil {
		return false
	}
	b, err := reference.ParseNormalizedNamed(candidate)
	if err != nil {
		return false
	}
	return a.Name() == b.Name()
}

// deployHookURL builds the absolute URL a CI pipeline should POST to,
// reflecting the scheme/host the request actually arrived on (so it works
// whether admin sits directly on :8870 or behind a reverse proxy).
func deployHookURL(r *http.Request, token string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/hooks/deploy/" + token
}

// handleDeployHookTrigger is deliberately unauthenticated - the token in the
// URL path is itself the credential, the same model GitHub/GitLab/Portainer
// deploy webhooks use. It forces a fresh pull/resolve of the service's
// image, optionally swapping to a new tag first.
func (s *Server) handleDeployHookTrigger(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	ctx := r.Context()

	hook, err := s.store.FindDeployHookByHash(hashToken(token))
	if err != nil {
		http.Error(w, "invalid or revoked deploy hook", http.StatusNotFound)
		return
	}

	svc, err := s.getService(ctx, hook.ServiceName)
	if err != nil {
		http.Error(w, "service no longer exists: "+err.Error(), http.StatusNotFound)
		return
	}

	var body struct {
		Image string `json:"image"`
	}
	if r.ContentLength != 0 {
		_ = json.NewDecoder(r.Body).Decode(&body) // best-effort; a missing/empty body just means "no image override"
	}
	if body.Image != "" && body.Image != svc.Spec.TaskTemplate.ContainerSpec.Image {
		if !hook.AllowImageOverride && !sameImageRepo(svc.Spec.TaskTemplate.ContainerSpec.Image, body.Image) {
			s.auditAs(r, "webhook:"+svc.Spec.Name, "deploy_hook.trigger", svc.Spec.Name, body.Image,
				errors.New("rejected cross-repository image override"))
			http.Error(w, "this hook is not allowed to switch to a different image repository - enable "+
				"\"allow full image override\" on the hook if that's intended", http.StatusForbidden)
			return
		}
		svc.Spec.TaskTemplate.ContainerSpec.Image = body.Image
	}
	svc.Spec.TaskTemplate.ForceUpdate++

	auth, err := s.encodedRegistryAuthFor(svc.Spec.TaskTemplate.ContainerSpec.Image)
	if err != nil {
		http.Error(w, "registry credential: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := s.docker.ServiceUpdate(ctx, svc.ID, svc.Version, svc.Spec, swarm.ServiceUpdateOptions{
		QueryRegistry:       true,
		EncodedRegistryAuth: auth,
	}); err != nil {
		http.Error(w, "update service: "+err.Error(), http.StatusBadGateway)
		return
	}

	go func() { _ = s.store.TouchDeployHook(hook.ID) }()
	s.auditAs(r, "webhook:"+svc.Spec.Name, "deploy_hook.trigger", svc.Spec.Name, body.Image, nil)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": svc.Spec.Name})
}
