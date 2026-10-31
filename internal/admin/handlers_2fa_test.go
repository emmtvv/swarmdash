package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"swarmdash/internal/store"
	"swarmdash/internal/totp"
)

// enable2FA turns two-factor on for an existing user and returns its secret
// and one plaintext recovery code.
func enable2FA(t *testing.T, s *Server, username string) (secret, recovery string) {
	t.Helper()
	u, err := s.store.GetUser(username)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	secret = totp.GenerateSecret()
	enc, err := s.encryptSecret(secret)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	plain, hashes := newRecoveryCodes()
	u.TOTPSecretEnc, u.TOTPEnabled, u.RecoveryCodeHashes = enc, true, hashes
	if err := s.store.PutUser(u); err != nil {
		t.Fatalf("put user: %v", err)
	}
	return secret, plain[0]
}

func post2FA(s *Server, pending *http.Cookie, code string) *httptest.ResponseRecorder {
	form := url.Values{"code": {code}}
	r := httptest.NewRequest(http.MethodPost, "/login/2fa", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if pending != nil {
		r.AddCookie(pending)
	}
	w := httptest.NewRecorder()
	s.handleLogin2FASubmit(w, r)
	return w
}

func TestLogin2FA_PasswordAloneStartsNoSession(t *testing.T) {
	s := newTestServer(t, nil)
	s.cfg.ClusterSecret = "test-secret"
	g4PutLocalUser(t, s, "bob", "correct-horse", "admin")
	secret, _ := enable2FA(t, s, "bob")

	w := httptest.NewRecorder()
	s.handleLoginSubmit(w, g4LoginRequest("bob", "correct-horse"))
	if loc := w.Header().Get("Location"); loc != "/login/2fa" {
		t.Fatalf("Location = %q, want /login/2fa", loc)
	}
	if g4CookieByName(w, sessionCookieName) != nil {
		t.Fatal("session cookie set before the second factor was checked")
	}
	pending := g4CookieByName(w, mfaCookieName)
	if pending == nil {
		t.Fatal("no pending-2FA cookie set")
	}

	if w := post2FA(s, pending, "000000"); g4CookieByName(w, sessionCookieName) != nil {
		t.Fatal("wrong code started a session")
	}

	code, _ := totp.Code(secret, totp.Step(time.Now()))
	w = post2FA(s, pending, code)
	if w.Header().Get("Location") != "/" || g4CookieByName(w, sessionCookieName) == nil {
		t.Fatalf("correct code: status %d location %q, want session + redirect to /", w.Code, w.Header().Get("Location"))
	}

	// The same code can't be used twice.
	if w := post2FA(s, pending, code); g4CookieByName(w, sessionCookieName) != nil {
		t.Fatal("replayed code started a second session")
	}
}

func TestLogin2FA_RecoveryCodeIsSingleUse(t *testing.T) {
	s := newTestServer(t, nil)
	s.cfg.ClusterSecret = "test-secret"
	g4PutLocalUser(t, s, "bob", "pw", "admin")
	_, recovery := enable2FA(t, s, "bob")

	pending := &http.Cookie{Name: mfaCookieName, Value: s.signMFAPending("bob", time.Now().Add(time.Minute))}
	if w := post2FA(s, pending, strings.ToUpper(recovery)); g4CookieByName(w, sessionCookieName) == nil {
		t.Fatal("recovery code (retyped in upper case) was not accepted")
	}
	u, _ := s.store.GetUser("bob")
	if len(u.RecoveryCodeHashes) != recoveryCodeCount-1 {
		t.Fatalf("recovery codes left = %d, want %d", len(u.RecoveryCodeHashes), recoveryCodeCount-1)
	}
	if w := post2FA(s, pending, recovery); g4CookieByName(w, sessionCookieName) != nil {
		t.Fatal("recovery code accepted twice")
	}
}

func TestLogin2FA_ForgedOrExpiredPendingCookieRejected(t *testing.T) {
	s := newTestServer(t, nil)
	s.cfg.ClusterSecret = "test-secret"
	g4PutLocalUser(t, s, "bob", "pw", "admin")
	secret, _ := enable2FA(t, s, "bob")
	code, _ := totp.Code(secret, totp.Step(time.Now()))

	for name, c := range map[string]*http.Cookie{
		"missing": nil,
		"expired": {Name: mfaCookieName, Value: s.signMFAPending("bob", time.Now().Add(-time.Second))},
		"forged":  {Name: mfaCookieName, Value: url.QueryEscape("bob|9999999999|deadbeef")},
	} {
		w := post2FA(s, c, code)
		if g4CookieByName(w, sessionCookieName) != nil || !strings.HasPrefix(w.Header().Get("Location"), "/login?") {
			t.Errorf("%s pending cookie: location %q, want bounce to /login", name, w.Header().Get("Location"))
		}
	}
}

func TestRequireAuth_Require2FARedirectsUnenrolledLocalUsers(t *testing.T) {
	s := newTestServer(t, nil)
	s.setRequire2FA(true)
	for _, u := range []store.User{
		{Username: "local", Role: "admin", CreatedAt: time.Now()},
		{Username: "sso", Role: "admin", AuthSource: "sso", CreatedAt: time.Now()},
	} {
		if err := s.store.PutUser(u); err != nil {
			t.Fatal(err)
		}
	}
	for user, wantRedirect := range map[string]bool{"local": true, "sso": false} {
		for path, exempt := range map[string]bool{"/nodes": false, "/account/security": true} {
			var called bool
			r := httptest.NewRequest(http.MethodGet, path, nil)
			r.AddCookie(g4NewSessionCookie(t, s, user))
			w := httptest.NewRecorder()
			s.requireAuth(g4AuthOKHandler(&called)).ServeHTTP(w, r)
			if want := !(wantRedirect && !exempt); called != want {
				t.Errorf("user %s path %s: handler called = %v, want %v", user, path, called, want)
			}
		}
	}
}
