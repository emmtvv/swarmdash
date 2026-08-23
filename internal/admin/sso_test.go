package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

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

// g4FakeOIDCProvider stands up a minimal OIDC provider for driving
// handleSSOLogin/handleSSOCallback end to end: discovery, a token endpoint
// that always succeeds, and a userinfo endpoint returning whatever claims
// the test currently has loaded into the returned pointer.
func g4FakeOIDCProvider(t *testing.T) (issuer string, claims *map[string]any) {
	t.Helper()
	claims = &map[string]any{}
	mux := http.NewServeMux()
	var tsURL string
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"authorization_endpoint": tsURL + "/authorize",
			"token_endpoint":         tsURL + "/token",
			"userinfo_endpoint":      tsURL + "/userinfo",
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "at-1"})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(*claims)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	tsURL = ts.URL
	return ts.URL, claims
}

// g4DoSSOLogin drives handleSSOLogin then handleSSOCallback back to back
// (as a browser would, carrying the state/PKCE cookie between them) and
// returns the callback's response recorder.
func g4DoSSOLogin(t *testing.T, s *Server) *httptest.ResponseRecorder {
	t.Helper()

	loginReq := httptest.NewRequest(http.MethodGet, "/sso/login", nil)
	loginW := httptest.NewRecorder()
	s.handleSSOLogin(loginW, loginReq)
	if loginW.Code != http.StatusSeeOther {
		t.Fatalf("handleSSOLogin status = %d, want %d; body: %s", loginW.Code, http.StatusSeeOther, loginW.Body.String())
	}
	authURL, err := url.Parse(loginW.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse authorize redirect: %v", err)
	}
	state := authURL.Query().Get("state")
	if state == "" {
		t.Fatalf("authorize redirect missing state: %s", authURL)
	}
	var stateCookie *http.Cookie
	for _, c := range loginW.Result().Cookies() {
		if c.Name == ssoStateCookieName {
			stateCookie = c
		}
	}
	if stateCookie == nil {
		t.Fatal("handleSSOLogin did not set the state cookie")
	}

	cbReq := httptest.NewRequest(http.MethodGet, "/sso/callback?state="+state+"&code=auth-code-1", nil)
	cbReq.AddCookie(stateCookie)
	cbW := httptest.NewRecorder()
	s.handleSSOCallback(cbW, cbReq)
	return cbW
}

func g4SetupSSOConfig(t *testing.T, s *Server, issuer string) {
	t.Helper()
	secretEnc, err := s.encryptSecret("client-secret")
	if err != nil {
		t.Fatalf("encrypt client secret: %v", err)
	}
	if err := s.store.PutSSOConfig(store.SSOConfig{
		ID:              store.SSOConfigID,
		Enabled:         true,
		Issuer:          issuer,
		ClientID:        "client-1",
		ClientSecretEnc: secretEnc,
		Scopes:          "openid profile email",
		AutoCreateUsers: true,
		DefaultRole:     "viewer",
		UpdatedAt:       time.Now(),
	}); err != nil {
		t.Fatalf("seed sso config: %v", err)
	}
}

// TestHandleSSOCallback_CannotHijackExistingUsernameWithDifferentSubject is
// the regression test for the account-takeover fixed by keying
// re-authentication off the `sub` claim instead of preferred_username: an
// attacker who can make an IdP claim preferred_username="alice" for their
// own identity must not be able to sign into the real alice's account.
func TestHandleSSOCallback_CannotHijackExistingUsernameWithDifferentSubject(t *testing.T) {
	issuer, claims := g4FakeOIDCProvider(t)
	s := newTestServer(t, nil)
	g4SetupSSOConfig(t, s, issuer)

	if err := s.store.PutUser(store.User{
		Username:     "alice",
		PasswordHash: ssoUnusablePasswordHash,
		Role:         "admin",
		AuthSource:   "sso",
		SSOSubject:   "real-alice-sub",
		CreatedAt:    time.Now(),
	}); err != nil {
		t.Fatalf("seed existing sso user: %v", err)
	}

	*claims = map[string]any{"sub": "attacker-sub", "preferred_username": "alice", "email": "attacker@evil.example.com"}
	w := g4DoSSOLogin(t, s)

	loc := w.Header().Get("Location")
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/login?error=") {
		t.Fatalf("expected redirect to /login with an error, got status=%d location=%q body=%s", w.Code, loc, w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieName && c.Value != "" {
			t.Fatalf("a session cookie must not be issued for a rejected hijack attempt, got %q", c.Value)
		}
	}

	alice, err := s.store.GetUser("alice")
	if err != nil {
		t.Fatalf("get alice: %v", err)
	}
	if alice.SSOSubject != "real-alice-sub" {
		t.Fatalf("alice.SSOSubject = %q, want it untouched at real-alice-sub", alice.SSOSubject)
	}
}

// TestHandleSSOCallback_BackfillsSubjectThenPinsIt covers the migration
// path (an SSO account created before SSOSubject existed) and confirms
// that once backfilled, the binding is enforced on future logins the same
// as an account that was always bound.
func TestHandleSSOCallback_BackfillsSubjectThenPinsIt(t *testing.T) {
	issuer, claims := g4FakeOIDCProvider(t)
	s := newTestServer(t, nil)
	g4SetupSSOConfig(t, s, issuer)

	if err := s.store.PutUser(store.User{
		Username:     "bob",
		PasswordHash: ssoUnusablePasswordHash,
		Role:         "viewer",
		AuthSource:   "sso",
		CreatedAt:    time.Now(),
		// SSOSubject intentionally left empty: simulates an account
		// provisioned before this field existed.
	}); err != nil {
		t.Fatalf("seed pre-migration sso user: %v", err)
	}

	*claims = map[string]any{"sub": "bob-sub-1", "preferred_username": "bob"}
	w := g4DoSSOLogin(t, s)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("first login: status=%d location=%q body=%s", w.Code, w.Header().Get("Location"), w.Body.String())
	}

	bob, err := s.store.GetUser("bob")
	if err != nil {
		t.Fatalf("get bob: %v", err)
	}
	if bob.SSOSubject != "bob-sub-1" {
		t.Fatalf("bob.SSOSubject = %q, want it backfilled to bob-sub-1", bob.SSOSubject)
	}

	// A different subject now claiming preferred_username="bob" must be
	// rejected, exactly like the already-bound case.
	*claims = map[string]any{"sub": "attacker-sub-2", "preferred_username": "bob"}
	w2 := g4DoSSOLogin(t, s)
	loc := w2.Header().Get("Location")
	if w2.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/login?error=") {
		t.Fatalf("second login: expected rejection, got status=%d location=%q body=%s", w2.Code, loc, w2.Body.String())
	}
}

// TestHandleSSOCallback_EmailVerifiedRequiredForDomainAllowlist covers the
// second half of the SSO identity fix: an unverified email must not pass
// the allowed-domains check even if its domain looks right.
func TestHandleSSOCallback_EmailVerifiedRequiredForDomainAllowlist(t *testing.T) {
	issuer, claims := g4FakeOIDCProvider(t)
	s := newTestServer(t, nil)
	g4SetupSSOConfig(t, s, issuer)
	cfg, err := s.store.GetSSOConfig()
	if err != nil {
		t.Fatalf("get sso config: %v", err)
	}
	cfg.AllowedDomains = "example.com"
	if err := s.store.PutSSOConfig(cfg); err != nil {
		t.Fatalf("update sso config: %v", err)
	}

	*claims = map[string]any{"sub": "carol-sub", "email": "carol@example.com", "email_verified": false}
	w := g4DoSSOLogin(t, s)
	loc := w.Header().Get("Location")
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/login?error=") {
		t.Fatalf("expected rejection for unverified email, got status=%d location=%q body=%s", w.Code, loc, w.Body.String())
	}

	*claims = map[string]any{"sub": "carol-sub", "email": "carol@example.com", "email_verified": true}
	w2 := g4DoSSOLogin(t, s)
	if w2.Code != http.StatusSeeOther || w2.Header().Get("Location") != "/" {
		t.Fatalf("expected success once verified, got status=%d location=%q body=%s", w2.Code, w2.Header().Get("Location"), w2.Body.String())
	}
}
