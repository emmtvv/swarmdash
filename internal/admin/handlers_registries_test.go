package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

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

	form := url.Values{"server": {"registry.example.com"}, "username": {"bob"}, "password": {"hunter2"}}
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
