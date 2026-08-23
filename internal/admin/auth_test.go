package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
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
	sess, err := s.store.GetSession(c.Value)
	if err != nil {
		t.Fatalf("session not persisted: %v", err)
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

	if _, err := s.store.GetLoginAttempt("nobody"); err != nil {
		t.Errorf("no login attempt recorded for unknown username: %v", err)
	}
	if _, err := s.store.GetLoginAttempt("carol"); err != nil {
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

	wait, err := s.loginLockedFor("dave")
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

func TestLoginLockout_ClearsAfterLockDuration(t *testing.T) {
	s := newTestServer(t, nil)
	if err := s.store.PutLoginAttempt(store.LoginAttempt{
		Username:    "erin",
		FailCount:   maxLoginAttempts,
		LastFailure: time.Now().Add(-time.Minute),
		LockedUntil: time.Now().Add(-time.Second), // already expired
	}); err != nil {
		t.Fatalf("put login attempt: %v", err)
	}

	wait, err := s.loginLockedFor("erin")
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
		Username:    "frank",
		FailCount:   maxLoginAttempts - 1,
		LastFailure: time.Now().Add(-(loginAttemptWindow + time.Minute)),
	}); err != nil {
		t.Fatalf("put login attempt: %v", err)
	}

	s.recordLoginFailure("frank")

	a, err := s.store.GetLoginAttempt("frank")
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
	if err := s.startSession(w, username); err != nil {
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

func TestRequireRole_ViewerBlockedFromMutationsAndPrivilegedGETs(t *testing.T) {
	s := newTestServer(t, nil)

	cases := []struct {
		name   string
		method string
		path   string
		user   *store.User
		want   int
	}{
		{"viewer POST mutating route", http.MethodPost, "/services/web/delete", testViewer, http.StatusForbidden},
		{"viewer GET settings", http.MethodGet, "/settings/tokens", testViewer, http.StatusForbidden},
		{"viewer GET exec console", http.MethodGet, "/exec/task1", testViewer, http.StatusForbidden},
		{"viewer GET ordinary page", http.MethodGet, "/services", testViewer, http.StatusOK},
		{"viewer GET account password", http.MethodGet, "/account/password", testViewer, http.StatusOK},
		{"admin POST mutating route", http.MethodPost, "/services/web/delete", testAdmin, http.StatusOK},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var called bool
			r := httptest.NewRequest(tt.method, tt.path, nil)
			r = withUser(r, tt.user)
			w := httptest.NewRecorder()
			s.requireRole(g4AuthOKHandler(&called)).ServeHTTP(w, r)
			if w.Code != tt.want {
				t.Errorf("status = %d, want %d", w.Code, tt.want)
			}
			if (tt.want == http.StatusOK) != called {
				t.Errorf("next called = %v, want %v", called, tt.want == http.StatusOK)
			}
		})
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
		Hash:      hashAPIToken(legacyPlaintext),
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
