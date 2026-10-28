package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"swarmdash/internal/imageupdate"
	"swarmdash/internal/store"
)

func TestHandleRegistriesPage(t *testing.T) {
	s := newTestServer(t, nil)
	if err := s.store.PutRegistryCredential(store.RegistryCredential{Server: "registry.example.com", Username: "bob"}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}

	r := withUser(httptest.NewRequest(http.MethodGet, "/settings/registries", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleRegistriesPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "registry.example.com") {
		t.Errorf("body missing registry server, got: %s", w.Body.String())
	}
}

func TestHandleRegistryCreate(t *testing.T) {
	s := newTestServer(t, nil)
	s.cfg.ClusterSecret = "test-secret"

	form := url.Values{"server": {"registry.example.com"}, "username": {"bob"}, "password": {"hunter2"}, "skip_check": {"1"}}
	r := withUser(httptest.NewRequest(http.MethodPost, "/settings/registries", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleRegistryCreate(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	creds, err := s.store.ListRegistryCredentials()
	if err != nil {
		t.Fatalf("ListRegistryCredentials: %v", err)
	}
	if len(creds) != 1 || creds[0].Server != "registry.example.com" || creds[0].Username != "bob" {
		t.Fatalf("unexpected registries: %+v", creds)
	}
	plain, err := s.decryptSecret(creds[0].PasswordEnc)
	if err != nil {
		t.Fatalf("decryptSecret: %v", err)
	}
	if plain != "hunter2" {
		t.Errorf("decrypted password = %q, want hunter2", plain)
	}

	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "registry.create" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleRegistryCreate_MissingFields(t *testing.T) {
	s := newTestServer(t, nil)

	form := url.Values{"server": {"registry.example.com"}}
	r := withUser(httptest.NewRequest(http.MethodPost, "/settings/registries", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleRegistryCreate(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleRegistryDelete(t *testing.T) {
	s := newTestServer(t, nil)
	if err := s.store.PutRegistryCredential(store.RegistryCredential{Server: "registry.example.com", Username: "bob"}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}

	r := withUser(httptest.NewRequest(http.MethodPost, "/settings/registries/registry.example.com/delete", nil), testAdmin)
	r.SetPathValue("server", "registry.example.com")
	w := httptest.NewRecorder()
	s.handleRegistryDelete(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	creds, err := s.store.ListRegistryCredentials()
	if err != nil {
		t.Fatalf("ListRegistryCredentials: %v", err)
	}
	if len(creds) != 0 {
		t.Fatalf("expected registry deleted, got: %+v", creds)
	}
}

func TestRegistryServerFor(t *testing.T) {
	tests := []struct {
		image string
		want  string
	}{
		{"nginx:latest", "docker.io"},
		{"library/nginx", "docker.io"},
		{"registry.example.com/team/app:v1", "registry.example.com"},
		{"localhost:5000/app", "localhost:5000"},
		{"registry.example.com/team/app@sha256:abcdef", "registry.example.com"},
	}
	for _, tt := range tests {
		if got := registryServerFor(tt.image); got != tt.want {
			t.Errorf("registryServerFor(%q) = %q, want %q", tt.image, got, tt.want)
		}
	}
}

func TestEncodedRegistryAuthFor(t *testing.T) {
	s := newTestServer(t, nil)
	s.cfg.ClusterSecret = "test-secret"
	enc, err := s.encryptSecret("hunter2")
	if err != nil {
		t.Fatalf("encryptSecret: %v", err)
	}
	if err := s.store.PutRegistryCredential(store.RegistryCredential{
		Server:      "registry.example.com",
		Username:    "bob",
		PasswordEnc: enc,
	}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}

	auth, err := s.encodedRegistryAuthFor("registry.example.com/team/app:v1")
	if err != nil {
		t.Fatalf("encodedRegistryAuthFor: %v", err)
	}
	if auth == "" {
		t.Error("expected non-empty auth for configured registry")
	}

	auth, err = s.encodedRegistryAuthFor("nginx:latest")
	if err != nil {
		t.Fatalf("encodedRegistryAuthFor (no cred): %v", err)
	}
	if auth != "" {
		t.Errorf("expected empty auth for unconfigured registry, got %q", auth)
	}
}

// fakeLoginRegistry is a TLS registry that answers /v2/ with a basic-auth
// challenge and accepts only bob/hunter2.
func fakeLoginRegistry(t *testing.T, s *Server) string {
	t.Helper()
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, _ := r.BasicAuth(); user != "bob" || pass != "hunter2" {
			w.Header().Set("WWW-Authenticate", `Basic realm="reg"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	s.registryClient = &imageupdate.Client{HTTP: ts.Client()}
	return strings.TrimPrefix(ts.URL, "https://")
}

func TestHandleRegistryCreate_ChecksLogin(t *testing.T) {
	s := newTestServer(t, nil)
	s.cfg.ClusterSecret = "test-secret"
	host := fakeLoginRegistry(t, s)

	w := httptest.NewRecorder()
	s.handleRegistryCreate(w, postForm("/settings/registries", url.Values{"server": {host}, "username": {"bob"}, "password": {"wrong"}}))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "login check failed") {
		t.Fatalf("wrong password: status = %d, body = %s", w.Code, w.Body.String())
	}
	if creds, _ := s.store.ListRegistryCredentials(); len(creds) != 0 {
		t.Fatalf("a rejected login was saved: %+v", creds)
	}

	w = httptest.NewRecorder()
	s.handleRegistryCreate(w, postForm("/settings/registries", url.Values{"server": {"https://" + host + "/"}, "username": {"bob"}, "password": {"hunter2"}}))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("right password: status = %d, body = %s", w.Code, w.Body.String())
	}
	creds, _ := s.store.ListRegistryCredentials()
	if len(creds) != 1 || creds[0].Server != host {
		t.Fatalf("saved %+v, want server normalized to %q", creds, host)
	}
}

func TestNormalizeRegistryServer(t *testing.T) {
	cases := map[string]string{
		" https://GHCR.io/ ":            "ghcr.io",
		"http://registry.local:5000/v2": "registry.local:5000",
		"index.docker.io":               "docker.io",
		"https://registry-1.docker.io":  "docker.io",
		"docker.io":                     "docker.io",
	}
	for in, want := range cases {
		if got := normalizeRegistryServer(in); got != want {
			t.Errorf("normalizeRegistryServer(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestStoredRegistryLogin_MatchesUnnormalizedServer covers credentials
// saved before addresses were normalized on save.
func TestStoredRegistryLogin_MatchesUnnormalizedServer(t *testing.T) {
	s := newTestServer(t, nil)
	s.cfg.ClusterSecret = "test-secret"
	enc, err := s.encryptSecret("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.PutRegistryCredential(store.RegistryCredential{Server: "https://ghcr.io/", Username: "bob", PasswordEnc: enc}); err != nil {
		t.Fatal(err)
	}
	cred := s.registryCredential("ghcr.io/org/app:1.0")
	if cred.Username != "bob" || cred.Password != "hunter2" {
		t.Errorf("credential = %+v, want bob/hunter2", cred)
	}
}
