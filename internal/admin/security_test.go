package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func g4NextHandler(called *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*called = true
		w.WriteHeader(http.StatusOK)
	})
}

func TestSecurity_SetsHeaders(t *testing.T) {
	s := newTestServer(t, nil)
	var called bool
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	s.security(g4NextHandler(&called)).ServeHTTP(w, r)

	if !called {
		t.Fatal("next handler was not called")
	}
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := w.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
	if got := w.Header().Get("Referrer-Policy"); got != "same-origin" {
		t.Errorf("Referrer-Policy = %q, want same-origin", got)
	}
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "connect-src 'self' https://api.github.com") {
		t.Errorf("CSP missing connect-src with api.github.com, got: %s", csp)
	}
}

func TestSecurity_FirstContactSetsCSRFCookie(t *testing.T) {
	s := newTestServer(t, nil)
	var called bool
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	s.security(g4NextHandler(&called)).ServeHTTP(w, r)

	resp := w.Result()
	var found bool
	for _, c := range resp.Cookies() {
		if c.Name == csrfCookieName {
			found = true
			if c.Value == "" {
				t.Error("csrf cookie value is empty")
			}
		}
	}
	if !found {
		t.Errorf("no %s cookie set on first contact", csrfCookieName)
	}
}

func TestSecurity_CSRFRejectsMissingToken(t *testing.T) {
	s := newTestServer(t, nil)
	var called bool
	r := httptest.NewRequest(http.MethodPost, "/settings/webhooks", nil)
	w := httptest.NewRecorder()
	s.security(g4NextHandler(&called)).ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusForbidden, w.Body.String())
	}
	if called {
		t.Error("next handler should not be called when CSRF token is missing")
	}
}

func TestSecurity_CSRFRejectsMismatchedToken(t *testing.T) {
	s := newTestServer(t, nil)
	var called bool
	r := httptest.NewRequest(http.MethodPost, "/settings/webhooks", nil)
	r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "cookie-token"})
	r.Header.Set("X-CSRF-Token", "different-token")
	w := httptest.NewRecorder()
	s.security(g4NextHandler(&called)).ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
	if called {
		t.Error("next handler should not be called on mismatched CSRF token")
	}
}

func TestSecurity_CSRFAcceptsMatchingHeaderToken(t *testing.T) {
	s := newTestServer(t, nil)
	var called bool
	r := httptest.NewRequest(http.MethodPost, "/settings/webhooks", nil)
	r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "shared-token"})
	r.Header.Set("X-CSRF-Token", "shared-token")
	w := httptest.NewRecorder()
	s.security(g4NextHandler(&called)).ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if !called {
		t.Error("next handler should be called when CSRF tokens match")
	}
}

func TestSecurity_CSRFAcceptsMatchingFormToken(t *testing.T) {
	s := newTestServer(t, nil)
	var called bool
	body := strings.NewReader("csrf_token=shared-token")
	r := httptest.NewRequest(http.MethodPost, "/settings/webhooks", body)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "shared-token"})
	w := httptest.NewRecorder()
	s.security(g4NextHandler(&called)).ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if !called {
		t.Error("next handler should be called when csrf_token form field matches cookie")
	}
}
