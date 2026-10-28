package admin

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types/registry"

	"swarmdash/internal/imageupdate"
	"swarmdash/internal/store"
)

func (s *Server) handleRegistriesPage(w http.ResponseWriter, r *http.Request) {
	creds, err := s.store.ListRegistryCredentials()
	if err != nil {
		http.Error(w, "list registries: "+err.Error(), http.StatusInternalServerError)
		return
	}
	sort.Slice(creds, func(i, j int) bool { return creds[i].Server < creds[j].Server })
	s.render(w, r, "registries.html", map[string]any{
		"User":       userFromContext(r),
		"Registries": creds,
	})
}

func (s *Server) handleRegistryCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	server := normalizeRegistryServer(r.FormValue("server"))
	username := r.FormValue("username")
	password := r.FormValue("password")
	if server == "" || username == "" || password == "" {
		http.Error(w, "server, username and password are all required", http.StatusBadRequest)
		return
	}
	// Check the login now rather than when a deploy or an automatic image
	// update first needs it - unless the operator says admin can't reach
	// the registry (the nodes pulling from it may still be able to).
	if r.FormValue("skip_check") == "" {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		if err := s.imageRegistry().CheckLogin(ctx, server, imageupdate.Credential{Username: username, Password: password}); err != nil {
			http.Error(w, "login check failed: "+err.Error()+" - fix the credentials, or tick \"Save without checking\" if admin can't reach this registry", http.StatusBadRequest)
			return
		}
	}

	enc, err := s.encryptSecret(password)
	if err != nil {
		http.Error(w, "encrypt credential: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.store.PutRegistryCredential(store.RegistryCredential{
		Server:      server,
		Username:    username,
		PasswordEnc: enc,
		CreatedAt:   time.Now(),
	}); err != nil {
		http.Error(w, "save registry: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "registry.create", server, "user="+username, nil)
	redirect(w, r, "/settings/registries")
}

func (s *Server) handleRegistryDelete(w http.ResponseWriter, r *http.Request) {
	server := r.PathValue("server")
	if err := s.store.DeleteRegistryCredential(server); err != nil {
		http.Error(w, "delete registry: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "registry.delete", server, "", nil)
	redirect(w, r, "/settings/registries")
}

// normalizeRegistryServer turns what someone types as a registry address
// into the form registryServerFor produces from an image reference, so the
// two match: "https://ghcr.io/" -> "ghcr.io", and Docker Hub's various
// hostnames -> "docker.io".
func normalizeRegistryServer(server string) string {
	server = strings.ToLower(strings.TrimSpace(server))
	server = strings.TrimPrefix(strings.TrimPrefix(server, "https://"), "http://")
	if i := strings.IndexByte(server, '/'); i != -1 {
		server = server[:i]
	}
	switch server {
	case "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com", "hub.docker.com":
		return "docker.io"
	}
	return server
}

// registryServerFor extracts the registry host from an image reference,
// the same way Docker itself resolves it: an explicit host (containing a
// "." or ":" in its first path segment, or literally "localhost") means a
// private registry; anything else defaults to Docker Hub.
func registryServerFor(image string) string {
	ref := image
	if i := strings.IndexByte(ref, '@'); i != -1 {
		ref = ref[:i]
	}
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) == 2 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost") {
		return parts[0]
	}
	return "docker.io"
}

// encodedRegistryAuthFor looks up a stored credential for the image's
// registry and, if found, returns the base64-encoded auth header Docker's
// service create/update APIs expect. Returns "" (no error) when no
// credential is configured for that registry - the pull is then attempted
// anonymously, same as if swarmdash weren't involved at all.
func (s *Server) encodedRegistryAuthFor(image string) (string, error) {
	cred, password, ok, err := s.storedRegistryLogin(image)
	if !ok || err != nil {
		return "", err
	}
	return registry.EncodeAuthConfig(registry.AuthConfig{
		Username:      cred.Username,
		Password:      password,
		ServerAddress: cred.Server,
	})
}

// storedRegistryLogin finds the saved credential for image's registry and
// decrypts its password; ok is false when none is saved. Credentials saved
// before addresses were normalized on save (e.g. as "https://ghcr.io") are
// still matched, by normalizing each stored address.
func (s *Server) storedRegistryLogin(image string) (cred store.RegistryCredential, password string, ok bool, err error) {
	server := registryServerFor(image)
	cred, err = s.store.GetRegistryCredential(server)
	if err != nil {
		all, listErr := s.store.ListRegistryCredentials()
		if listErr != nil {
			return cred, "", false, nil
		}
		for _, c := range all {
			if normalizeRegistryServer(c.Server) == server {
				cred, ok = c, true
				break
			}
		}
		if !ok {
			return cred, "", false, nil
		}
	}
	password, err = s.decryptSecret(cred.PasswordEnc)
	if err != nil {
		return cred, "", false, err
	}
	return cred, password, true, nil
}
