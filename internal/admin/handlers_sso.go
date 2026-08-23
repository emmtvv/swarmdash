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
		Secure:   s.isSecureRequest(r),
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
	http.SetCookie(w, &http.Cookie{Name: ssoStateCookieName, Value: "", Path: "/sso/callback", Secure: s.isSecureRequest(r), MaxAge: -1})
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

	subject, username, email, emailVerified := ssoIdentityFromClaims(claims)
	if subject == "" {
		fail("identity provider did not return a stable subject (sub) claim")
		return
	}
	if username == "" {
		username = subject
	}
	if cfg.AllowedDomains != "" {
		if !emailVerified {
			fail("this identity's email is not verified, so its domain cannot be checked against the allowlist")
			return
		}
		if !emailDomainAllowed(email, cfg.AllowedDomains) {
			fail("this account's email domain is not allowed to sign in")
			return
		}
	}

	// Re-authentication is keyed off `sub` - the one OIDC claim guaranteed
	// unique and immutable for a given identity - never off username or
	// email, either of which can be edited at some IdPs and would otherwise
	// let one identity take over another's local account by claiming its
	// username. See User.SSOSubject's doc comment (internal/store/types.go).
	user, err := s.store.GetUserBySSOSubject(subject)
	switch {
	case err == nil:
		// Already bound to this subject on a previous login: sign in under
		// the account as it exists today. The display username is
		// deliberately NOT refreshed from preferred_username/email here -
		// once bound, an identity keeps the local username it was
		// provisioned/claimed under, so a claim edit at the IdP can't rename
		// (and can never collide with) an existing local account.
	case errors.Is(err, store.ErrNotFound):
		existing, lookupErr := s.store.GetUser(username)
		switch {
		case lookupErr == nil:
			if existing.AuthSource != "sso" {
				fail("a local account with this username already exists")
				return
			}
			if existing.SSOSubject != "" {
				// A different subject already owns this username - most
				// likely someone changed their preferred_username/email at
				// the IdP to collide with another account. Reject rather
				// than silently signing into the existing user's session.
				fail("this identity's username is already in use by a different account")
				return
			}
			// Pre-migration SSO account with no subject bound yet: claim it
			// for this subject now (one-time backfill), rather than
			// requiring every existing SSO user to be recreated.
			existing.SSOSubject = subject
			if err := s.store.PutUser(existing); err != nil {
				s.log.Error("sso backfill subject", "err", err)
				fail("internal error")
				return
			}
			user = existing
		case errors.Is(lookupErr, store.ErrNotFound):
			if !cfg.AutoCreateUsers {
				fail("no local account exists for this identity; ask an admin to create one")
				return
			}
			user = store.User{
				Username:     username,
				PasswordHash: ssoUnusablePasswordHash,
				Role:         cfg.DefaultRole,
				AuthSource:   "sso",
				SSOSubject:   subject,
				CreatedAt:    time.Now(),
			}
			if err := s.store.PutUser(user); err != nil {
				s.log.Error("sso auto-create user", "err", err)
				fail("could not create local account")
				return
			}
			s.auditAs(r, user.Username, "user.sso_provision", user.Username, "role="+user.Role, nil)
		default:
			s.log.Error("sso lookup user", "err", lookupErr)
			fail("internal error")
			return
		}
	default:
		s.log.Error("sso lookup user by subject", "err", err)
		fail("internal error")
		return
	}

	if err := s.startSession(w, r, user.Username); err != nil {
		fail("internal error")
		return
	}
	s.auditAs(r, user.Username, "user.sso_login", user.Username, "", nil)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// ssoIdentityFromClaims reads the identity provider's claims for the
// account signing in. subject (`sub`) is the only claim used to
// re-authenticate a returning identity - see the doc comment on its use in
// handleSSOCallback above. username and email are display/allowlist
// information only, sourced from claims that a user can often change at
// the IdP itself and so must never be trusted to look up or distinguish
// accounts.
func ssoIdentityFromClaims(claims map[string]any) (subject, username, email string, emailVerified bool) {
	str := func(k string) string {
		v, _ := claims[k].(string)
		return v
	}
	subject = strings.TrimSpace(str("sub"))
	email = strings.TrimSpace(str("email"))
	username = strings.TrimSpace(str("preferred_username"))
	if username == "" {
		username = email
	}
	emailVerified, _ = claims["email_verified"].(bool)
	return subject, username, email, emailVerified
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
