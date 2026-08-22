package admin

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types/registry"

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
	server := strings.TrimSpace(r.FormValue("server"))
	username := r.FormValue("username")
	password := r.FormValue("password")
	if server == "" || username == "" || password == "" {
		http.Error(w, "server, username and password are all required", http.StatusBadRequest)
		return
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
	server := registryServerFor(image)
	cred, err := s.store.GetRegistryCredential(server)
	if err != nil {
		return "", nil
	}
	password, err := s.decryptSecret(cred.PasswordEnc)
	if err != nil {
		return "", err
	}
	return registry.EncodeAuthConfig(registry.AuthConfig{
		Username:      cred.Username,
		Password:      password,
		ServerAddress: cred.Server,
	})
}
