package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"swarmdash/internal/store"
)

func validRole(role string) bool {
	return role == "admin" || role == "viewer"
}

const sessionCookieName = "swarmdash_session"
const sessionTTL = 7 * 24 * time.Hour

// Local-login lockout: bootstrap/generated passwords are long random
// tokens, but operators can still set short ones by hand, so failed
// attempts are throttled per-username regardless. maxLoginAttempts failures
// within loginAttemptWindow of each other locks that username out for
// loginLockDuration; a failure outside the window starts the count over
// rather than compounding indefinitely.
const (
	maxLoginAttempts   = 5
	loginAttemptWindow = 15 * time.Minute
	loginLockDuration  = 15 * time.Minute
)

type ctxKey int

const (
	ctxKeyUser ctxKey = iota
	ctxKeyCSRF
	ctxKeyNonce
)

// ensureBootstrapUser creates the initial admin account on first run. If a
// username/password wasn't provided via flags/env, a random password is
// generated and printed once so the operator can log in.
func (s *Server) ensureBootstrapUser() error {
	has, err := s.store.HasAnyUser()
	if err != nil {
		return err
	}
	if has {
		return nil
	}

	username := s.cfg.BootstrapUsername
	if username == "" {
		username = "admin"
	}
	password := s.cfg.BootstrapPassword
	generated := false
	if password == "" {
		password = randomToken(12)
		generated = true
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	if err := s.store.PutUser(store.User{
		Username:           username,
		PasswordHash:       string(hash),
		Role:               "admin",
		CreatedAt:          time.Now(),
		MustChangePassword: true,
	}); err != nil {
		return err
	}

	if generated {
		s.log.Warn("generated bootstrap admin credentials - store these somewhere safe",
			"username", username, "password", password)
	} else {
		s.log.Info("created bootstrap admin user", "username", username)
	}
	return nil
}

func randomToken(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failing means the system is unusable anyway
	}
	return hex.EncodeToString(b)
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if s.currentUser(r) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	sso, _ := s.store.GetSSOConfig()
	s.render(w, r, "login.html", map[string]any{
		"Error":              r.URL.Query().Get("error"),
		"SSOLabel":           sso.Label,
		"SSOActive":          sso.Enabled,
		"LocalLoginDisabled": sso.Enabled && sso.EnforceSSO,
	})
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/login?error=bad+request", http.StatusSeeOther)
		return
	}

	// Enforced server-side, not just hidden in the UI: even with the form
	// removed from login.html, POST /login would otherwise still work for
	// anyone who knew a local password.
	if sso, _ := s.store.GetSSOConfig(); sso.Enabled && sso.EnforceSSO {
		http.Redirect(w, r, "/login?error=password+login+is+disabled+-+use+SSO", http.StatusSeeOther)
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	// Checked before touching the user record so an unknown username locks
	// out the same way a known one does - it doesn't create a timing/
	// enumeration gap between the two.
	if wait, err := s.loginLockedFor(username); err != nil {
		s.log.Error("check login lockout", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if wait > 0 {
		http.Redirect(w, r, "/login?error="+url.QueryEscape(fmt.Sprintf("too many failed attempts - try again in %dm", int(wait.Minutes())+1)), http.StatusSeeOther)
		return
	}

	user, err := s.store.GetUser(username)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Error("get user", "err", err)
		}
		s.recordLoginFailure(username)
		http.Redirect(w, r, "/login?error=invalid+credentials", http.StatusSeeOther)
		return
	}
	if user.AuthSource == "sso" {
		http.Redirect(w, r, "/login?error=this+account+signs+in+via+SSO", http.StatusSeeOther)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		s.recordLoginFailure(username)
		http.Redirect(w, r, "/login?error=invalid+credentials", http.StatusSeeOther)
		return
	}

	if err := s.store.DeleteLoginAttempt(username); err != nil {
		s.log.Error("clear login attempts", "err", err)
	}

	if err := s.startSession(w, username); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// loginLockedFor reports how much longer username is locked out of local
// login, or 0 if it isn't locked.
func (s *Server) loginLockedFor(username string) (time.Duration, error) {
	a, err := s.store.GetLoginAttempt(username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	if wait := time.Until(a.LockedUntil); wait > 0 {
		return wait, nil
	}
	return 0, nil
}

// recordLoginFailure increments username's failed-attempt count, resetting
// it first if the last failure fell outside loginAttemptWindow, and sets a
// lockout once maxLoginAttempts is reached. Best-effort: a store error here
// only means this one failure wasn't counted, so it's logged rather than
// surfaced to the client - a login that already failed on bad credentials
// shouldn't turn into a 500.
func (s *Server) recordLoginFailure(username string) {
	a, err := s.store.GetLoginAttempt(username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Error("get login attempt", "err", err)
		return
	}
	now := time.Now()
	if now.Sub(a.LastFailure) > loginAttemptWindow {
		a.FailCount = 0
	}
	a.Username = username
	a.FailCount++
	a.LastFailure = now
	if a.FailCount >= maxLoginAttempts {
		a.LockedUntil = now.Add(loginLockDuration)
		s.log.Warn("account locked out after repeated failed logins", "username", username, "attempts", a.FailCount)
	}
	if err := s.store.PutLoginAttempt(a); err != nil {
		s.log.Error("put login attempt", "err", err)
	}
}

// startSession mints a new session token for username and sets the session
// cookie on w. Shared by local password login (handleLoginSubmit) and the
// OIDC callback (handleSSOCallback).
func (s *Server) startSession(w http.ResponseWriter, username string) error {
	token := randomToken(32)
	now := time.Now()
	if err := s.store.PutSession(store.Session{
		Token:     token,
		Username:  username,
		CreatedAt: now,
		ExpiresAt: now.Add(sessionTTL),
	}); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  now.Add(sessionTTL),
	})
	return nil
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		_ = s.store.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) currentUser(r *http.Request) *store.User {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil
	}
	sess, err := s.store.GetSession(c.Value)
	if err != nil || time.Now().After(sess.ExpiresAt) {
		return nil
	}
	user, err := s.store.GetUser(sess.Username)
	if err != nil {
		return nil
	}
	return &user
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token := bearerToken(r); token != "" {
			user, err := s.userFromAPIToken(token)
			if err != nil {
				http.Error(w, "invalid or revoked API token", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(newUserContext(r, user)))
			return
		}

		user := s.currentUser(r)
		if user == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		// A password set by someone other than the account holder (the
		// generated/env-provided bootstrap password, or an admin's reset of
		// another user's password) must be replaced before anything else is
		// reachable - checked on every request, not just at login, so
		// navigating straight to a bookmarked URL doesn't skip it.
		if user.MustChangePassword && r.URL.Path != "/account/password" {
			http.Redirect(w, r, "/account/password", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r.WithContext(newUserContext(r, user)))
	})
}

// requireRole enforces the two-role model on top of requireAuth's
// authentication: "viewer" accounts get read-only access. Enforced in one
// place, wrapped around the whole protected mux (see server.go), so no
// individual route registration can be missed.
func (s *Server) requireRole(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user := userFromContext(r); user.Role != "admin" && needsAdmin(r) {
			http.Error(w, "forbidden: this account has read-only access", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// needsAdmin reports whether a request requires the admin role: any
// mutating request, plus three read-only-by-method areas that are
// privileged in practice - settings (registries, tokens, webhooks, swarm
// join tokens, users), and the interactive console (page + websocket),
// which lets you run arbitrary commands in a container despite being a
// GET.
func needsAdmin(r *http.Request) bool {
	// Changing your own password isn't a privileged action - every account
	// needs it, including a viewer stuck behind MustChangePassword.
	if r.URL.Path == "/account/password" {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return true
	}
	p := r.URL.Path
	return strings.HasPrefix(p, "/settings/") || strings.HasPrefix(p, "/exec/") || strings.HasPrefix(p, "/ws/exec/")
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.HasPrefix(h, prefix) {
		return h[len(prefix):]
	}
	return ""
}

func hashAPIToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// userFromAPIToken validates a bearer token against the stored hashes and
// returns a synthetic user representing that service account, scoped to
// the role the token was created with. Tokens created before roles
// existed have no stored Role - grandfathered in as "admin" so existing
// CI credentials don't silently lose access.
func (s *Server) userFromAPIToken(token string) (*store.User, error) {
	t, err := s.store.FindAPITokenByHash(hashAPIToken(token))
	if err != nil {
		return nil, err
	}
	go func() { _ = s.store.TouchAPIToken(t.ID) }()
	role := t.Role
	if role == "" {
		role = "admin"
	}
	return &store.User{Username: "token:" + t.Name, Role: role}, nil
}

// newAPIToken generates a new plaintext token (shown to the caller exactly
// once) and its storage record.
func newAPIToken(name, role, createdBy string) (plaintext string, rec store.APIToken) {
	plaintext = "sdt_" + randomToken(24)
	rec = store.APIToken{
		ID:        randomToken(8),
		Name:      name,
		Hash:      hashAPIToken(plaintext),
		Role:      role,
		CreatedBy: createdBy,
		CreatedAt: time.Now(),
	}
	return plaintext, rec
}
