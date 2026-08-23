package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
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
// attempts are throttled per (username, client IP) pair regardless - see
// loginAttemptKey. maxLoginAttempts failures within loginAttemptWindow of
// each other locks that pair out for loginLockDuration; a failure outside
// the window starts the count over rather than compounding indefinitely.
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
	ip := s.clientIP(r)

	// Checked before touching the user record so an unknown username locks
	// out the same way a known one does - it doesn't create a timing/
	// enumeration gap between the two.
	if wait, err := s.loginLockedFor(username, ip); err != nil {
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
		// Burn roughly the same time a real bcrypt comparison below would
		// take, so an unknown username doesn't answer measurably faster
		// than a known one with a wrong password - see dummyPasswordHash.
		_ = bcrypt.CompareHashAndPassword(dummyPasswordHash, []byte(password))
		s.recordLoginFailure(username, ip)
		http.Redirect(w, r, "/login?error=invalid+credentials", http.StatusSeeOther)
		return
	}
	if user.AuthSource == "sso" {
		http.Redirect(w, r, "/login?error=this+account+signs+in+via+SSO", http.StatusSeeOther)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		s.recordLoginFailure(username, ip)
		http.Redirect(w, r, "/login?error=invalid+credentials", http.StatusSeeOther)
		return
	}

	if err := s.store.DeleteLoginAttempt(loginAttemptKey(username, ip)); err != nil {
		s.log.Error("clear login attempts", "err", err)
	}

	if err := s.startSession(w, r, username); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// dummyPasswordHash is compared against on an unknown-username login so
// that path takes roughly the same bcrypt-shaped time as a known username
// with a wrong password - otherwise the endpoint answers fast enough on an
// unknown username to let an attacker enumerate accounts by timing alone,
// even though the redirect the client sees is identical either way.
var dummyPasswordHash = func() []byte {
	h, err := bcrypt.GenerateFromPassword([]byte("swarmdash-timing-placeholder"), bcrypt.DefaultCost)
	if err != nil {
		panic(err)
	}
	return h
}()

// loginAttemptKey derives the lockout key for a (username, client IP)
// pair; see LoginAttempt's doc comment in internal/store/types.go for why
// it's the pair and not username alone.
func loginAttemptKey(username, ip string) string {
	return username + "\x00" + ip
}

// clientIP resolves the request's originating client address. By default
// (no --trusted-proxies) it's just RemoteAddr with net/http's port
// stripped: best-effort, since an admin behind a reverse proxy that
// doesn't forward the original client address sees every request as
// coming from the proxy, which just means the lockout keys off the
// proxy's address - no worse than treating every request as one shared
// bucket, which is what a username-only key already did.
//
// When --trusted-proxies is set and RemoteAddr matches it, X-Forwarded-For
// is trusted instead: read right-to-left (closest hop first, per RFC 7239
// intent) and return the first address that isn't itself a trusted proxy.
// Gated on the same trust decision as isSecureRequest (see security.go) so
// the two headers a proxied deployment relies on - X-Forwarded-For here,
// X-Forwarded-Proto there - are either both trusted or neither is; an
// operator who hasn't configured --trusted-proxies gets today's safe
// default on both.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !s.isTrustedProxy(host) {
		return host
	}
	xff := r.Header.Get("X-Forwarded-For")
	for _, part := range reverseSplit(xff, ",") {
		candidate := strings.TrimSpace(part)
		if candidate == "" || s.isTrustedProxy(candidate) {
			continue
		}
		return candidate
	}
	return host
}

// reverseSplit splits s on sep and returns the parts in reverse order.
func reverseSplit(s, sep string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, sep)
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return parts
}

// isTrustedProxy reports whether addr (no port) falls inside one of the
// --trusted-proxies networks. Empty s.trustedProxies (the default) means
// nothing is trusted, which is what makes clientIP/isSecureRequest fail
// closed to today's safe behavior when the flag isn't set.
func (s *Server) isTrustedProxy(addr string) bool {
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	for _, n := range s.trustedProxies {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// parseTrustedProxies turns --trusted-proxies' comma-separated list of IPs
// and/or CIDRs into networks for isTrustedProxy. A bare IP is treated as a
// /32 (or /128 for IPv6) - the common case of naming a proxy's single
// address rather than a whole subnet.
func parseTrustedProxies(raw []string) ([]*net.IPNet, error) {
	var nets []*net.IPNet
	for _, entry := range raw {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !strings.Contains(entry, "/") {
			ip := net.ParseIP(entry)
			if ip == nil {
				return nil, fmt.Errorf("invalid trusted proxy address %q", entry)
			}
			if ip.To4() != nil {
				entry += "/32"
			} else {
				entry += "/128"
			}
		}
		_, n, err := net.ParseCIDR(entry)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy %q: %w", entry, err)
		}
		nets = append(nets, n)
	}
	return nets, nil
}

// loginLockedFor reports how much longer the (username, ip) pair is locked
// out of local login, or 0 if it isn't locked.
func (s *Server) loginLockedFor(username, ip string) (time.Duration, error) {
	a, err := s.store.GetLoginAttempt(loginAttemptKey(username, ip))
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

// recordLoginFailure increments the (username, ip) pair's failed-attempt
// count, resetting it first if the last failure fell outside
// loginAttemptWindow, and sets a lockout once maxLoginAttempts is reached.
// Best-effort: a store error here only means this one failure wasn't
// counted, so it's logged rather than surfaced to the client - a login
// that already failed on bad credentials shouldn't turn into a 500.
func (s *Server) recordLoginFailure(username, ip string) {
	key := loginAttemptKey(username, ip)
	a, err := s.store.GetLoginAttempt(key)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Error("get login attempt", "err", err)
		return
	}
	now := time.Now()
	if now.Sub(a.LastFailure) > loginAttemptWindow {
		a.FailCount = 0
	}
	a.Key = key
	a.Username = username
	a.IP = ip
	a.FailCount++
	a.LastFailure = now
	if a.FailCount >= maxLoginAttempts {
		a.LockedUntil = now.Add(loginLockDuration)
		s.log.Warn("account locked out after repeated failed logins", "username", username, "ip", ip, "attempts", a.FailCount)
	}
	if err := s.store.PutLoginAttempt(a); err != nil {
		s.log.Error("put login attempt", "err", err)
	}
}

// startSession mints a new session token for username and sets the session
// cookie on w. Shared by local password login (handleLoginSubmit) and the
// OIDC callback (handleSSOCallback). Only the token's hash is persisted
// (see hashToken) - like an API token, the plaintext is a bearer
// credential, and with --storage-driver=local that credential now lives in
// a SQLite file on disk (trivial to copy out via a backup or `docker cp`),
// so it shouldn't be recoverable from a stolen database the way a raw
// primary key would be.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, username string) error {
	token := randomToken(32)
	now := time.Now()
	if err := s.store.PutSession(store.Session{
		Token:     hashToken(token),
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
		Secure:   s.isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		Expires:  now.Add(sessionTTL),
	})
	return nil
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		_ = s.store.DeleteSession(hashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", Secure: s.isSecureRequest(r), MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) currentUser(r *http.Request) *store.User {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil
	}
	sess, err := s.store.GetSession(hashToken(c.Value))
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
// requireRole enforces the two-role model on top of requireAuth's
// authentication: "viewer" accounts get read-only access. Enforced in one
// place, wrapped around the whole protected mux (see server.go), so no
// individual route registration can be missed.
func (s *Server) requireRole(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user := userFromContext(r); user.Role != "admin" && s.rbacTable().needsAdmin(r) {
			http.Error(w, "forbidden: this account has read-only access", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// rbacTable lazily builds the route -> role-requirement lookup from
// protectedRoutes (see routes.go). Lazy + cached on first use rather than
// built in Handler(), since tests exercise requireRole directly without
// necessarily calling Handler() first.
func (s *Server) rbacTable() *rbac {
	s.rbacOnce.Do(func() {
		s.rbacCache = newRBAC(protectedRoutes(s))
	})
	return s.rbacCache
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.HasPrefix(h, prefix) {
		return h[len(prefix):]
	}
	return ""
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// userFromAPIToken validates a bearer token against the stored hashes and
// returns a synthetic user representing that service account, scoped to
// the role the token was created with. Tokens created before roles
// existed have no stored Role - grandfathered in as "admin" so existing
// CI credentials don't silently lose access.
func (s *Server) userFromAPIToken(token string) (*store.User, error) {
	t, err := s.store.FindAPITokenByHash(hashToken(token))
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
		Hash:      hashToken(plaintext),
		Role:      role,
		CreatedBy: createdBy,
		CreatedAt: time.Now(),
	}
	return plaintext, rec
}
