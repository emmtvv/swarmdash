package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"swarmdash/internal/store"
)

func g4PutLocalUser(t *testing.T, s *Server, username, password, role string) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	if err := s.store.PutUser(store.User{
		Username:     username,
		PasswordHash: string(hash),
		Role:         role,
		CreatedAt:    time.Now(),
	}); err != nil {
		t.Fatalf("put user: %v", err)
	}
}

func g4LoginRequest(username, password string) *http.Request {
	form := url.Values{"username": {username}, "password": {password}}
	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func g4CookieByName(w *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestHandleLoginSubmit_Success(t *testing.T) {
	s := newTestServer(t, nil)
	g4PutLocalUser(t, s, "bob", "correct-horse", "admin")

	w := httptest.NewRecorder()
	s.handleLoginSubmit(w, g4LoginRequest("bob", "correct-horse"))

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusSeeOther, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want /", loc)
	}
	c := g4CookieByName(w, sessionCookieName)
	if c == nil || c.Value == "" {
		t.Fatal("no session cookie set on successful login")
	}
	sess, err := s.store.GetSession(hashToken(c.Value))
	if err != nil {
		t.Fatalf("session not persisted: %v", err)
	}
	if _, err := s.store.GetSession(c.Value); err == nil {
		t.Error("session lookup succeeded using the raw cookie value - the token should be hashed at rest, not stored in plaintext")
	}
	if sess.Username != "bob" {
		t.Errorf("session username = %q, want bob", sess.Username)
	}
}

func TestHandleLoginSubmit_UnknownVsWrongPassword_SameRedirect(t *testing.T) {
	s := newTestServer(t, nil)
	g4PutLocalUser(t, s, "carol", "rightpw", "admin")

	wUnknown := httptest.NewRecorder()
	s.handleLoginSubmit(wUnknown, g4LoginRequest("nobody", "whatever"))

	wWrong := httptest.NewRecorder()
	s.handleLoginSubmit(wWrong, g4LoginRequest("carol", "wrongpw"))

	if wUnknown.Code != http.StatusSeeOther || wWrong.Code != http.StatusSeeOther {
		t.Fatalf("status = %d/%d, want both %d", wUnknown.Code, wWrong.Code, http.StatusSeeOther)
	}
	locUnknown := wUnknown.Header().Get("Location")
	locWrong := wWrong.Header().Get("Location")
	if locUnknown != locWrong {
		t.Errorf("unknown-username redirect %q != wrong-password redirect %q (enumeration leak)", locUnknown, locWrong)
	}

	const testIP = "192.0.2.1" // httptest.NewRequest's fixed RemoteAddr
	if _, err := s.store.GetLoginAttempt(loginAttemptKey("nobody", testIP)); err != nil {
		t.Errorf("no login attempt recorded for unknown username: %v", err)
	}
	if _, err := s.store.GetLoginAttempt(loginAttemptKey("carol", testIP)); err != nil {
		t.Errorf("no login attempt recorded for wrong password: %v", err)
	}
}

func TestLoginLockout_AfterMaxAttempts(t *testing.T) {
	s := newTestServer(t, nil)
	g4PutLocalUser(t, s, "dave", "rightpw", "admin")

	for i := 0; i < maxLoginAttempts; i++ {
		w := httptest.NewRecorder()
		s.handleLoginSubmit(w, g4LoginRequest("dave", "wrongpw"))
	}

	wait, err := s.loginLockedFor("dave", "192.0.2.1")
	if err != nil {
		t.Fatalf("loginLockedFor: %v", err)
	}
	if wait <= 0 {
		t.Fatal("expected account to be locked out after maxLoginAttempts failures")
	}

	// Even the correct password is rejected while locked out.
	w := httptest.NewRecorder()
	s.handleLoginSubmit(w, g4LoginRequest("dave", "rightpw"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	if !strings.Contains(w.Header().Get("Location"), "too+many+failed+attempts") {
		t.Errorf("Location = %q, want lockout message", w.Header().Get("Location"))
	}
	if c := g4CookieByName(w, sessionCookieName); c != nil {
		t.Error("session cookie set despite lockout")
	}
}

// TestLoginLockout_KeyedPerIP_DoesNotBlockOtherClients guards against the
// targeted-DoS failure mode a username-only lockout key has: an attacker
// who doesn't know "dave"'s password can still lock the real "dave" out of
// his own account with a handful of bad requests, from anywhere. Keying on
// (username, IP) means the attacker's failures only lock out their own
// address.
func TestLoginLockout_KeyedPerIP_DoesNotBlockOtherClients(t *testing.T) {
	s := newTestServer(t, nil)
	g4PutLocalUser(t, s, "dave", "rightpw", "admin")

	for i := 0; i < maxLoginAttempts; i++ {
		s.recordLoginFailure("dave", "203.0.113.9") // attacker's address
	}
	if wait, err := s.loginLockedFor("dave", "203.0.113.9"); err != nil || wait <= 0 {
		t.Fatalf("attacker's address should be locked out: wait=%v err=%v", wait, err)
	}

	if wait, err := s.loginLockedFor("dave", "192.0.2.1"); err != nil {
		t.Fatalf("loginLockedFor: %v", err)
	} else if wait > 0 {
		t.Error("a different client's address was locked out by another address's failed attempts")
	}

	// The real dave, logging in correctly from his own address, isn't
	// blocked by the attacker's failures against the same username.
	w := httptest.NewRecorder()
	s.handleLoginSubmit(w, g4LoginRequest("dave", "rightpw"))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Errorf("status = %d, location = %q; want a successful login redirect to /", w.Code, w.Header().Get("Location"))
	}
}

func TestLoginLockout_ClearsAfterLockDuration(t *testing.T) {
	s := newTestServer(t, nil)
	if err := s.store.PutLoginAttempt(store.LoginAttempt{
		Key:         loginAttemptKey("erin", "192.0.2.1"),
		Username:    "erin",
		IP:          "192.0.2.1",
		FailCount:   maxLoginAttempts,
		LastFailure: time.Now().Add(-time.Minute),
		LockedUntil: time.Now().Add(-time.Second), // already expired
	}); err != nil {
		t.Fatalf("put login attempt: %v", err)
	}

	wait, err := s.loginLockedFor("erin", "192.0.2.1")
	if err != nil {
		t.Fatalf("loginLockedFor: %v", err)
	}
	if wait != 0 {
		t.Errorf("loginLockedFor() = %v, want 0 once LockedUntil has passed", wait)
	}
}

func TestLoginLockout_WindowResetsFailCount(t *testing.T) {
	s := newTestServer(t, nil)
	if err := s.store.PutLoginAttempt(store.LoginAttempt{
		Key:         loginAttemptKey("frank", "192.0.2.1"),
		Username:    "frank",
		IP:          "192.0.2.1",
		FailCount:   maxLoginAttempts - 1,
		LastFailure: time.Now().Add(-(loginAttemptWindow + time.Minute)),
	}); err != nil {
		t.Fatalf("put login attempt: %v", err)
	}

	s.recordLoginFailure("frank", "192.0.2.1")

	a, err := s.store.GetLoginAttempt(loginAttemptKey("frank", "192.0.2.1"))
	if err != nil {
		t.Fatalf("get login attempt: %v", err)
	}
	if a.FailCount != 1 {
		t.Errorf("FailCount = %d, want 1 (window should have reset the count)", a.FailCount)
	}
	if !a.LockedUntil.IsZero() {
		t.Errorf("LockedUntil = %v, want zero (shouldn't lock on a single failure after reset)", a.LockedUntil)
	}
}

func g4AuthOKHandler(called *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*called = true
		w.WriteHeader(http.StatusOK)
	})
}

func TestRequireAuth_RedirectsUnauthenticatedBrowserRequest(t *testing.T) {
	s := newTestServer(t, nil)
	var called bool
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	s.requireAuth(g4AuthOKHandler(&called)).ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	if loc := w.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
	if called {
		t.Error("next handler should not run for unauthenticated request")
	}
}

func TestRequireAuth_BadBearerTokenReturns401(t *testing.T) {
	s := newTestServer(t, nil)
	var called bool
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer sdt_bogus")
	w := httptest.NewRecorder()
	s.requireAuth(g4AuthOKHandler(&called)).ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
	if called {
		t.Error("next handler should not run for a bad bearer token")
	}
}

func TestRequireAuth_ValidBearerTokenMapsRole(t *testing.T) {
	s := newTestServer(t, nil)
	plaintext, rec := newAPIToken("ci", "viewer", "admin-user")
	if err := s.store.PutAPIToken(rec); err != nil {
		t.Fatalf("put api token: %v", err)
	}

	var gotUser *store.User
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = userFromContext(r)
		w.WriteHeader(http.StatusOK)
	})
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+plaintext)
	w := httptest.NewRecorder()
	s.requireAuth(next).ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if gotUser == nil || gotUser.Role != "viewer" {
		t.Fatalf("context user = %+v, want role viewer", gotUser)
	}
}

func g4NewSessionCookie(t *testing.T, s *Server, username string) *http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	if err := s.startSession(w, httptest.NewRequest(http.MethodPost, "/login", nil), username); err != nil {
		t.Fatalf("start session: %v", err)
	}
	c := g4CookieByName(w, sessionCookieName)
	if c == nil {
		t.Fatal("startSession did not set a session cookie")
	}
	return c
}

func TestRequireAuth_MustChangePassword_OnlyExemptsAccountPasswordPath(t *testing.T) {
	s := newTestServer(t, nil)
	if err := s.store.PutUser(store.User{
		Username:           "gina",
		PasswordHash:       "!unused!",
		Role:               "admin",
		CreatedAt:          time.Now(),
		MustChangePassword: true,
	}); err != nil {
		t.Fatalf("put user: %v", err)
	}
	cookie := g4NewSessionCookie(t, s, "gina")

	var called bool
	r := httptest.NewRequest(http.MethodGet, "/nodes", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	s.requireAuth(g4AuthOKHandler(&called)).ServeHTTP(w, r)
	if called {
		t.Error("next handler should not run for a must-change-password user on a non-exempt path")
	}
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/account/password" {
		t.Errorf("got status %d, location %q; want redirect to /account/password", w.Code, w.Header().Get("Location"))
	}

	called = false
	r2 := httptest.NewRequest(http.MethodGet, "/account/password", nil)
	r2.AddCookie(cookie)
	w2 := httptest.NewRecorder()
	s.requireAuth(g4AuthOKHandler(&called)).ServeHTTP(w2, r2)
	if !called {
		t.Error("next handler should run for /account/password even with MustChangePassword set")
	}
	if w2.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w2.Code, http.StatusOK)
	}
}

// routeParamRE matches a {param} (or {param...}) segment in a registered
// mux pattern, so a sample concrete path can be built for it.
var routeParamRE = regexp.MustCompile(`\{[^}]+\}`)

// samplePath turns a registered pattern's path half (e.g. "/nodes/{id}" or
// the exact-match "/{$}") into a concrete path a request can target.
func samplePath(pattern string) string {
	if pattern == "/{$}" {
		return "/"
	}
	return routeParamRE.ReplaceAllString(pattern, "x")
}

// wantAdminOnlyByRoute is this test's own record of the intended RBAC
// policy, keyed by the exact "METHOD /pattern" registered in
// protectedRoutes (routes.go). It is maintained independently of that
// table's ViewerOK column - deliberately, so this test isn't just an
// echo of the implementation: it walks every *currently registered* route
// below and fails if either (a) a route has no entry here at all (added
// to protectedRoutes without anyone deciding who may reach it), or (b)
// this policy disagrees with what routes.go actually declares. Either
// failure mode means a route's access level was never explicitly reviewed.
var wantAdminOnlyByRoute = map[string]bool{
	"GET /{$}":               false,
	"GET /account/password":  false,
	"POST /account/password": false,

	"GET /nodes":                           false,
	"GET /nodes/stats":                     false,
	"GET /nodes/{id}":                      false,
	"GET /nodes/{id}/stats":                false,
	"POST /nodes/{id}/availability":        true,
	"POST /nodes/{id}/promote":             true,
	"POST /nodes/{id}/demote":              true,
	"POST /nodes/{id}/labels":              true,
	"POST /nodes/{id}/labels/{key}/delete": true,

	"GET /stacks":                     false,
	"GET /stacks/templates":           false,
	"GET /stacks/deploy":              false,
	"POST /stacks/deploy/preview":     true,
	"POST /stacks/deploy":             true,
	"GET /stacks/gitops":              false,
	"POST /stacks/gitops":             true,
	"POST /stacks/gitops/{id}/sync":   true,
	"POST /stacks/gitops/{id}/delete": true,
	"GET /stacks/{name}":              false,
	"GET /stacks/{name}/export.yml":   false,
	"POST /stacks/{name}/restart":     true,
	"POST /stacks/{name}/delete":      true,

	"GET /services":                                  false,
	"GET /services/{name}":                           false,
	"GET /services/{name}/events":                    false,
	"GET /services/{name}/export.yml":                false,
	"POST /services/{name}/scale":                    true,
	"POST /services/{name}/image":                    true,
	"POST /services/{name}/spec":                     true,
	"POST /services/{name}/rollback":                 true,
	"POST /services/{name}/restart":                  true,
	"POST /services/{name}/update-latest":            true,
	"POST /services/{name}/delete":                   true,
	"POST /services/bulk":                            true,
	"POST /services/{name}/deploy-hooks":             true,
	"POST /services/{name}/deploy-hooks/{id}/delete": true,

	"GET /topology": false,

	"GET /networks":              false,
	"POST /networks":             true,
	"POST /networks/{id}/delete": true,

	"GET /secrets":              false,
	"POST /secrets":             true,
	"POST /secrets/{id}/delete": true,

	"GET /configs":              false,
	"POST /configs":             true,
	"POST /configs/{id}/delete": true,

	"GET /images":        false,
	"POST /images/prune": true,

	"GET /volumes":                false,
	"POST /volumes":               true,
	"POST /volumes/{name}/delete": true,

	"GET /audit":  false,
	"GET /events": false,

	// Settings are entirely admin-only: registry/webhook/token/SSO
	// credentials, swarm join tokens, and user management all live here.
	"GET /settings/webhooks":              true,
	"POST /settings/webhooks":             true,
	"POST /settings/webhooks/{id}/delete": true,

	"GET /settings/tokens":              true,
	"POST /settings/tokens":             true,
	"POST /settings/tokens/{id}/delete": true,

	"GET /settings/users":                      true,
	"POST /settings/users":                     true,
	"POST /settings/users/{username}/delete":   true,
	"POST /settings/users/{username}/password": true,

	"GET /settings/swarm":                      true,
	"POST /settings/swarm/spec":                true,
	"POST /settings/swarm/rotate-token/{role}": true,
	"POST /settings/swarm/rotate-ca":           true,
	"POST /settings/swarm/rebalance":           true,
	"POST /settings/swarm/prune-failed-tasks":  true,
	"POST /settings/swarm/prune-resources":     true,

	"GET /settings/registries":                  true,
	"POST /settings/registries":                 true,
	"POST /settings/registries/{server}/delete": true,

	"GET /settings/sso":          true,
	"POST /settings/sso":         true,
	"POST /settings/sso/disable": true,

	"GET /settings/backup":          true,
	"GET /settings/backup/export":   true,
	"POST /settings/backup/restore": true,

	"GET /settings/general":  true,
	"POST /settings/general": true,

	// The interactive console (page + websocket) runs arbitrary commands
	// in a container despite being a GET. The file browser/downloader is
	// the same class of risk: containers routinely bind-mount Docker
	// secrets (e.g. /run/secrets/*), so unrestricted path read is a
	// secrets-exfiltration primitive, not an ordinary read-only view.
	"GET /exec/{taskID}":           true,
	"GET /ws/exec/{taskID}":        true,
	"GET /files/{taskID}":          true,
	"GET /files/{taskID}/download": true,

	"GET /logs/{taskID}":          false,
	"GET /ws/logs/{taskID}":       false,
	"GET /ws/stats/{taskID}":      false,
	"GET /logs/{taskID}/download": false,

	"GET /services/{name}/logs":          false,
	"GET /ws/services/{name}/logs":       false,
	"GET /services/{name}/logs/download": false,
}

// TestRequireRole_AllRoutesHaveExplicitRBACDecision walks every route
// actually registered in protectedRoutes (not a hand-picked sample) and
// checks requireRole's real behavior against this test's independent
// policy map above. A route that's new, renamed, or had its ViewerOK
// flipped without a matching, deliberate update here fails the build -
// see the comment on wantAdminOnlyByRoute.
func TestRequireRole_AllRoutesHaveExplicitRBACDecision(t *testing.T) {
	s := newTestServer(t, nil)
	routes := protectedRoutes(s)

	seen := make(map[string]bool, len(routes))
	for _, rt := range routes {
		seen[rt.Pattern] = true

		wantAdmin, ok := wantAdminOnlyByRoute[rt.Pattern]
		if !ok {
			t.Errorf("route %q is registered but has no RBAC policy in wantAdminOnlyByRoute - decide whether a viewer may reach it and add an entry", rt.Pattern)
			continue
		}
		if wantAdmin == rt.ViewerOK {
			t.Errorf("route %q: routes.go declares ViewerOK=%v, but this test's policy wants admin-only=%v - one of them is wrong", rt.Pattern, rt.ViewerOK, wantAdmin)
		}

		method, path, found := strings.Cut(rt.Pattern, " ")
		if !found {
			t.Fatalf("route pattern %q has no method prefix", rt.Pattern)
		}
		reqPath := samplePath(path)

		for _, tc := range []struct {
			user *store.User
			want int
		}{
			{testAdmin, http.StatusOK},
			{testViewer, map[bool]int{true: http.StatusForbidden, false: http.StatusOK}[wantAdmin]},
		} {
			var called bool
			r := withUser(httptest.NewRequest(method, reqPath, nil), tc.user)
			w := httptest.NewRecorder()
			s.requireRole(g4AuthOKHandler(&called)).ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Errorf("%s %s as %s: status = %d, want %d", method, reqPath, tc.user.Role, w.Code, tc.want)
			}
			if (tc.want == http.StatusOK) != called {
				t.Errorf("%s %s as %s: next called = %v, want %v", method, reqPath, tc.user.Role, called, tc.want == http.StatusOK)
			}
		}
	}

	for pattern := range wantAdminOnlyByRoute {
		if !seen[pattern] {
			t.Errorf("wantAdminOnlyByRoute has a policy for %q but no such route is registered anymore - remove the stale entry", pattern)
		}
	}
}

func TestUserFromAPIToken_RoleMapping(t *testing.T) {
	s := newTestServer(t, nil)

	plaintext, rec := newAPIToken("ci", "viewer", "admin-user")
	if err := s.store.PutAPIToken(rec); err != nil {
		t.Fatalf("put api token: %v", err)
	}
	user, err := s.userFromAPIToken(plaintext)
	if err != nil {
		t.Fatalf("userFromAPIToken: %v", err)
	}
	if user.Role != "viewer" {
		t.Errorf("Role = %q, want viewer", user.Role)
	}
	if user.Username != "token:ci" {
		t.Errorf("Username = %q, want token:ci", user.Username)
	}
}

func TestUserFromAPIToken_GrandfatheredEmptyRoleIsAdmin(t *testing.T) {
	s := newTestServer(t, nil)

	legacyPlaintext := "sdt_legacy"
	if err := s.store.PutAPIToken(store.APIToken{
		ID:        "legacy1",
		Name:      "legacy",
		Hash:      hashToken(legacyPlaintext),
		Role:      "",
		CreatedBy: "admin-user",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("put api token: %v", err)
	}

	user, err := s.userFromAPIToken(legacyPlaintext)
	if err != nil {
		t.Fatalf("userFromAPIToken: %v", err)
	}
	if user.Role != "admin" {
		t.Errorf("Role = %q, want admin (grandfathered)", user.Role)
	}
}

func TestUserFromAPIToken_UnknownTokenErrors(t *testing.T) {
	s := newTestServer(t, nil)
	if _, err := s.userFromAPIToken("sdt_does-not-exist"); err == nil {
		t.Fatal("expected an error for an unknown token")
	}
}
