package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"swarmdash/internal/store"
)

func g4SSOFormRequest(method, path string, form url.Values) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return withUser(r, testAdmin)
}

func TestHandleSSOSettingsPage_DefaultsWhenUnconfigured(t *testing.T) {
	s := newTestServer(t, nil)
	r := withUser(httptest.NewRequest(http.MethodGet, "/settings/sso", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleSSOSettingsPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Single sign-on") {
		t.Errorf("body missing expected page content, got: %s", w.Body.String())
	}
}

func TestHandleSSOSettingsSave_PersistsConfig(t *testing.T) {
	s := newTestServer(t, nil)
	form := url.Values{
		"enabled":           {"on"},
		"label":             {"Okta"},
		"issuer":            {"https://example.okta.com"},
		"client_id":         {"client-123"},
		"client_secret":     {"super-secret"},
		"auto_create_users": {"on"},
		"default_role":      {"viewer"},
	}
	r := g4SSOFormRequest(http.MethodPost, "/settings/sso", form)
	w := httptest.NewRecorder()
	s.handleSSOSettingsSave(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusSeeOther, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "/settings/sso" {
		t.Errorf("Location = %q, want /settings/sso", loc)
	}

	cfg, err := s.store.GetSSOConfig()
	if err != nil {
		t.Fatalf("get sso config: %v", err)
	}
	if !cfg.Enabled {
		t.Error("Enabled = false, want true")
	}
	if cfg.Label != "Okta" {
		t.Errorf("Label = %q, want Okta", cfg.Label)
	}
	if cfg.Issuer != "https://example.okta.com" {
		t.Errorf("Issuer = %q", cfg.Issuer)
	}
	if cfg.Scopes != "openid profile email" {
		t.Errorf("Scopes = %q, want default", cfg.Scopes)
	}
	if len(cfg.ClientSecretEnc) == 0 {
		t.Error("ClientSecretEnc is empty, want the encrypted secret")
	}
	if cfg.DefaultRole != "viewer" {
		t.Errorf("DefaultRole = %q, want viewer", cfg.DefaultRole)
	}

	plaintext, err := s.decryptSecret(cfg.ClientSecretEnc)
	if err != nil {
		t.Fatalf("decrypt secret: %v", err)
	}
	if plaintext != "super-secret" {
		t.Errorf("decrypted secret = %q, want super-secret", plaintext)
	}
}

func TestHandleSSOSettingsSave_RejectsIncompleteConfigWhenEnabled(t *testing.T) {
	s := newTestServer(t, nil)
	form := url.Values{"enabled": {"on"}} // missing issuer/client_id/secret
	r := g4SSOFormRequest(http.MethodPost, "/settings/sso", form)
	w := httptest.NewRecorder()
	s.handleSSOSettingsSave(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
	if _, err := s.store.GetSSOConfig(); err == nil {
		t.Error("incomplete config should not have been persisted")
	}
}

func TestHandleSSOSettingsSave_DisablingClearsEnforceSSO(t *testing.T) {
	s := newTestServer(t, nil)
	form := url.Values{
		"enabled":      {"off"},
		"enforce_sso":  {"on"}, // should be ignored: enforcing SSO while off makes no sense
		"issuer":       {""},
		"client_id":    {""},
		"default_role": {"viewer"},
	}
	r := g4SSOFormRequest(http.MethodPost, "/settings/sso", form)
	w := httptest.NewRecorder()
	s.handleSSOSettingsSave(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusSeeOther, w.Body.String())
	}
	cfg, err := s.store.GetSSOConfig()
	if err != nil {
		t.Fatalf("get sso config: %v", err)
	}
	if cfg.EnforceSSO {
		t.Error("EnforceSSO = true, want false when Enabled is false")
	}
}

func TestHandleSSOSettingsDisable(t *testing.T) {
	s := newTestServer(t, nil)
	if err := s.store.PutSSOConfig(store.SSOConfig{
		ID:         store.SSOConfigID,
		Enabled:    true,
		EnforceSSO: true,
		Issuer:     "https://example.okta.com",
	}); err != nil {
		t.Fatalf("seed sso config: %v", err)
	}

	r := withUser(httptest.NewRequest(http.MethodPost, "/settings/sso/disable", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleSSOSettingsDisable(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusSeeOther, w.Body.String())
	}
	cfg, err := s.store.GetSSOConfig()
	if err != nil {
		t.Fatalf("get sso config: %v", err)
	}
	if cfg.Enabled {
		t.Error("Enabled = true, want false after disable")
	}
	if cfg.EnforceSSO {
		t.Error("EnforceSSO = true, want false after disable")
	}
}

func TestSSOSettingsPage_RequireRoleBlocksViewer(t *testing.T) {
	s := newTestServer(t, nil)
	r := withUser(httptest.NewRequest(http.MethodGet, "/settings/sso", nil), testViewer)
	w := httptest.NewRecorder()
	s.requireRole(http.HandlerFunc(s.handleSSOSettingsPage)).ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestSSOSettingsSave_RequireRoleBlocksViewer(t *testing.T) {
	s := newTestServer(t, nil)
	form := url.Values{"enabled": {"off"}}
	r := httptest.NewRequest(http.MethodPost, "/settings/sso", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r = withUser(r, testViewer)
	w := httptest.NewRecorder()
	s.requireRole(http.HandlerFunc(s.handleSSOSettingsSave)).ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}
