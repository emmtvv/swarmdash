package admin

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"swarmdash/internal/store"
)

// ssoUnusablePasswordHash is stored on SSO-provisioned accounts instead of
// a real bcrypt hash, so local password login is structurally impossible
// for them - bcrypt.CompareHashAndPassword always errors on a string
// that isn't a valid bcrypt hash, regardless of what password is tried.
const ssoUnusablePasswordHash = "!sso-account-no-local-password!"

const ssoStateCookieName = "swarmdash_sso_state"

func (s *Server) handleSSOSettingsPage(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.store.GetSSOConfig()
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		http.Error(w, "load sso config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, "sso.html", map[string]any{
		"User":        userFromContext(r),
		"Config":      cfg,
		"HasSecret":   len(cfg.ClientSecretEnc) > 0,
		"RedirectURI": ssoRedirectURI(cfg, r),
	})
}

func (s *Server) handleSSOSettingsSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	existing, err := s.store.GetSSOConfig()
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		http.Error(w, "load sso config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	cfg := store.SSOConfig{
		ID:              store.SSOConfigID,
		Enabled:         r.FormValue("enabled") == "on",
		Label:           strings.TrimSpace(r.FormValue("label")),
		Issuer:          strings.TrimRight(strings.TrimSpace(r.FormValue("issuer")), "/"),
		ClientID:        strings.TrimSpace(r.FormValue("client_id")),
		Scopes:          strings.TrimSpace(r.FormValue("scopes")),
		AutoCreateUsers: r.FormValue("auto_create_users") == "on",
		EnforceSSO:      r.FormValue("enforce_sso") == "on",
		DefaultRole:     r.FormValue("default_role"),
		AllowedDomains:  strings.TrimSpace(r.FormValue("allowed_domains")),
		RedirectBaseURL: strings.TrimRight(strings.TrimSpace(r.FormValue("redirect_base_url")), "/"),
		ClientSecretEnc: existing.ClientSecretEnc,
		UpdatedAt:       time.Now(),
	}
	if cfg.Label == "" {
		cfg.Label = "SSO"
	}
	if cfg.Scopes == "" {
		cfg.Scopes = "openid profile email"
	}
	if !validRole(cfg.DefaultRole) {
		cfg.DefaultRole = "viewer"
	}
	if secret := r.FormValue("client_secret"); secret != "" {
		enc, err := s.encryptSecret(secret)
		if err != nil {
			http.Error(w, "encrypt client secret: "+err.Error(), http.StatusInternalServerError)
			return
		}
		cfg.ClientSecretEnc = enc
	}
	if cfg.Enabled && (cfg.Issuer == "" || cfg.ClientID == "" || len(cfg.ClientSecretEnc) == 0) {
		http.Error(w, "issuer, client ID and client secret are all required to enable SSO", http.StatusBadRequest)
		return
	}
	if !cfg.Enabled {
		// Enforcing SSO while it's off would just lock everyone out with
		// no way in - not a state worth persisting.
		cfg.EnforceSSO = false
	}

	if err := s.store.PutSSOConfig(cfg); err != nil {
		http.Error(w, "save sso config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "sso.configure", cfg.Issuer, fmt.Sprintf("enabled=%v", cfg.Enabled), nil)
	redirect(w, r, "/settings/sso")
}

func (s *Server) handleSSOSettingsDisable(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.store.GetSSOConfig()
	if err != nil {
		http.Error(w, "load sso config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	cfg.Enabled = false
	cfg.EnforceSSO = false // re-disabling local login needs an explicit, separate opt-in each time
	cfg.UpdatedAt = time.Now()
	if err := s.store.PutSSOConfig(cfg); err != nil {
		http.Error(w, "save sso config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "sso.disable", cfg.Issuer, "", nil)
	redirect(w, r, "/settings/sso")
}

// handleSSOLogin kicks off the OIDC Authorization Code + PKCE flow: stash
// a random state + PKCE verifier in a short-lived cookie scoped to the
// callback path, then redirect the browser to the provider.
func (s *Server) handleSSOLogin(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.store.GetSSOConfig()
	if err != nil || !cfg.Enabled {
		http.Redirect(w, r, "/login?error=sso+is+not+configured", http.StatusSeeOther)
		return
	}
	doc, err := fetchOIDCDiscovery(r.Context(), cfg.Issuer)
	if err != nil {
		s.log.Error("sso discovery", "err", err)
		http.Redirect(w, r, "/login?error=sso+provider+unreachable", http.StatusSeeOther)
		return
	}

	state := randomToken(16)
	verifier, challenge := newPKCEPair()
	http.SetCookie(w, &http.Cookie{
		Name:     ssoStateCookieName,
		Value:    state + ":" + verifier,
		Path:     "/sso/callback",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   300,
	})

	authURL := oidcAuthURL(doc, cfg.ClientID, ssoRedirectURI(cfg, r), cfg.Scopes, state, challenge)
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

func (s *Server) handleSSOCallback(w http.ResponseWriter, r *http.Request) {
	fail := func(msg string) {
		http.Redirect(w, r, "/login?error="+url.QueryEscape(msg), http.StatusSeeOther)
	}

	cfg, err := s.store.GetSSOConfig()
	if err != nil || !cfg.Enabled {
		fail("sso is not configured")
		return
	}

	stateCookie, err := r.Cookie(ssoStateCookieName)
	if err != nil {
		fail("sso session expired, please try again")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: ssoStateCookieName, Value: "", Path: "/sso/callback", MaxAge: -1})
	parts := strings.SplitN(stateCookie.Value, ":", 2)
	if len(parts) != 2 || parts[0] != r.URL.Query().Get("state") {
		fail("sso state mismatch, please try again")
		return
	}
	verifier := parts[1]

	if errParam := r.URL.Query().Get("error"); errParam != "" {
		fail("sso login failed: " + errParam)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		fail("sso callback missing authorization code")
		return
	}

	doc, err := fetchOIDCDiscovery(r.Context(), cfg.Issuer)
	if err != nil {
		fail("sso provider unreachable")
		return
	}
	clientSecret, err := s.decryptSecret(cfg.ClientSecretEnc)
	if err != nil {
		s.log.Error("sso decrypt client secret", "err", err)
		fail("sso is misconfigured")
		return
	}
	tok, err := exchangeOIDCCode(r.Context(), doc, cfg.ClientID, clientSecret, ssoRedirectURI(cfg, r), code, verifier)
	if err != nil {
		s.log.Error("sso token exchange", "err", err)
		fail("sso login failed")
		return
	}
	claims, err := fetchOIDCUserinfo(r.Context(), doc, tok.AccessToken)
	if err != nil {
		s.log.Error("sso userinfo", "err", err)
		fail("sso login failed")
		return
	}

	username, email := ssoIdentityFromClaims(claims)
	if username == "" {
		fail("identity provider did not return a usable username or email")
		return
	}
	if cfg.AllowedDomains != "" && !emailDomainAllowed(email, cfg.AllowedDomains) {
		fail("this account's email domain is not allowed to sign in")
		return
	}

	user, err := s.store.GetUser(username)
	switch {
	case err == nil:
		if user.AuthSource != "sso" {
			fail("a local account with this username already exists")
			return
		}
	case errors.Is(err, store.ErrNotFound):
		if !cfg.AutoCreateUsers {
			fail("no local account exists for this identity; ask an admin to create one")
			return
		}
		user = store.User{
			Username:     username,
			PasswordHash: ssoUnusablePasswordHash,
			Role:         cfg.DefaultRole,
			AuthSource:   "sso",
			CreatedAt:    time.Now(),
		}
		if err := s.store.PutUser(user); err != nil {
			s.log.Error("sso auto-create user", "err", err)
			fail("could not create local account")
			return
		}
		s.audit(r, "user.sso_provision", user.Username, "role="+user.Role, nil)
	default:
		s.log.Error("sso lookup user", "err", err)
		fail("internal error")
		return
	}

	if err := s.startSession(w, user.Username); err != nil {
		fail("internal error")
		return
	}
	s.audit(r, "user.sso_login", user.Username, "", nil)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func ssoIdentityFromClaims(claims map[string]any) (username, email string) {
	str := func(k string) string {
		v, _ := claims[k].(string)
		return v
	}
	email = strings.TrimSpace(str("email"))
	username = strings.TrimSpace(str("preferred_username"))
	if username == "" {
		username = email
	}
	if username == "" {
		username = strings.TrimSpace(str("sub"))
	}
	return username, email
}

func emailDomainAllowed(email, allowedCSV string) bool {
	if email == "" {
		return false
	}
	at := strings.LastIndex(email, "@")
	if at == -1 {
		return false
	}
	domain := strings.ToLower(email[at+1:])
	for _, d := range strings.Split(allowedCSV, ",") {
		if strings.ToLower(strings.TrimSpace(d)) == domain {
			return true
		}
	}
	return false
}

// ssoRedirectURI derives the OAuth redirect_uri: cfg.RedirectBaseURL when
// set (needed whenever admin sits behind a reverse proxy/load balancer, so
// the URI registered with the identity provider is stable and correct
// regardless of how the request actually arrived), otherwise the scheme +
// Host of the incoming request.
func ssoRedirectURI(cfg store.SSOConfig, r *http.Request) string {
	base := cfg.RedirectBaseURL
	if base == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		} else if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
			scheme = proto
		}
		base = scheme + "://" + r.Host
	}
	return base + "/sso/callback"
}
