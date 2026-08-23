package admin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarmdash/internal/store"
)

func TestNewPKCEPair(t *testing.T) {
	verifier, challenge := newPKCEPair()
	if len(verifier) == 0 || len(challenge) == 0 {
		t.Fatal("expected non-empty verifier and challenge")
	}
	if verifier == challenge {
		t.Error("challenge should be a derived hash of verifier, not equal to it")
	}
	// S256: challenge = base64url(sha256(verifier)), and must be deterministic.
	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if challenge != want {
		t.Errorf("challenge = %q, want %q (S256 of verifier)", challenge, want)
	}
	v2, c2 := newPKCEPair()
	if v2 == verifier || c2 == challenge {
		t.Error("two calls to newPKCEPair produced the same verifier/challenge - not random")
	}
}

func TestOIDCAuthURL(t *testing.T) {
	doc := oidcDiscovery{AuthorizationEndpoint: "https://idp.example.com/authorize"}
	got := oidcAuthURL(doc, "client-1", "https://admin.example.com/sso/callback", "openid profile email", "state-1", "challenge-1")

	if !strings.HasPrefix(got, "https://idp.example.com/authorize?") {
		t.Fatalf("unexpected URL prefix: %s", got)
	}
	for _, want := range []string{
		"response_type=code",
		"client_id=client-1",
		"state=state-1",
		"code_challenge=challenge-1",
		"code_challenge_method=S256",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("URL missing %q, got: %s", want, got)
		}
	}
}

func TestOIDCAuthURL_AppendsWhenEndpointAlreadyHasQuery(t *testing.T) {
	doc := oidcDiscovery{AuthorizationEndpoint: "https://idp.example.com/authorize?tenant=acme"}
	got := oidcAuthURL(doc, "client-1", "https://admin.example.com/sso/callback", "openid", "state-1", "challenge-1")
	if !strings.HasPrefix(got, "https://idp.example.com/authorize?tenant=acme&") {
		t.Fatalf("expected query to be appended with &, got: %s", got)
	}
}

func TestSSOIdentityFromClaims(t *testing.T) {
	tests := []struct {
		name              string
		claims            map[string]any
		wantSubject       string
		wantUsername      string
		wantEmail         string
		wantEmailVerified bool
	}{
		{
			name:              "subject always comes from sub, display prefers preferred_username",
			claims:            map[string]any{"preferred_username": "alice", "email": "alice@example.com", "sub": "abc123", "email_verified": true},
			wantSubject:       "abc123",
			wantUsername:      "alice",
			wantEmail:         "alice@example.com",
			wantEmailVerified: true,
		},
		{
			name:         "display falls back to email when no preferred_username",
			claims:       map[string]any{"email": "bob@example.com", "sub": "def456"},
			wantSubject:  "def456",
			wantUsername: "bob@example.com",
			wantEmail:    "bob@example.com",
		},
		{
			name:         "no username claims at all - subject is still returned, display is empty",
			claims:       map[string]any{"sub": "sub-only"},
			wantSubject:  "sub-only",
			wantUsername: "",
			wantEmail:    "",
		},
		{
			name:         "no sub claim at all - subject is empty even if a display name is present",
			claims:       map[string]any{"preferred_username": "alice"},
			wantSubject:  "",
			wantUsername: "alice",
			wantEmail:    "",
		},
		{
			name:              "email_verified false is preserved, not silently treated as true",
			claims:            map[string]any{"sub": "abc123", "email": "alice@example.com", "email_verified": false},
			wantSubject:       "abc123",
			wantUsername:      "alice@example.com",
			wantEmail:         "alice@example.com",
			wantEmailVerified: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subject, username, email, emailVerified := ssoIdentityFromClaims(tt.claims)
			if subject != tt.wantSubject {
				t.Errorf("subject = %q, want %q", subject, tt.wantSubject)
			}
			if username != tt.wantUsername {
				t.Errorf("username = %q, want %q", username, tt.wantUsername)
			}
			if email != tt.wantEmail {
				t.Errorf("email = %q, want %q", email, tt.wantEmail)
			}
			if emailVerified != tt.wantEmailVerified {
				t.Errorf("emailVerified = %v, want %v", emailVerified, tt.wantEmailVerified)
			}
		})
	}
}

func TestEmailDomainAllowed(t *testing.T) {
	tests := []struct {
		email, allowed string
		want           bool
	}{
		{"alice@example.com", "example.com", true},
		{"alice@example.com", "Example.COM", true}, // case-insensitive
		{"alice@example.com", "other.com,example.com", true},
		{"alice@evil.com", "example.com", false},
		{"", "example.com", false},
		{"no-at-sign", "example.com", false},
	}
	for _, tt := range tests {
		if got := emailDomainAllowed(tt.email, tt.allowed); got != tt.want {
			t.Errorf("emailDomainAllowed(%q, %q) = %v, want %v", tt.email, tt.allowed, got, tt.want)
		}
	}
}

func TestSSORedirectURI(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/sso/login", nil)
	r.Host = "admin.internal:8080"

	t.Run("derives from request when RedirectBaseURL unset", func(t *testing.T) {
		got := ssoRedirectURI(store.SSOConfig{}, r)
		want := "http://admin.internal:8080/sso/callback"
		if got != want {
			t.Errorf("ssoRedirectURI() = %q, want %q", got, want)
		}
	})

	t.Run("uses configured RedirectBaseURL when set", func(t *testing.T) {
		got := ssoRedirectURI(store.SSOConfig{RedirectBaseURL: "https://swarmdash.example.com"}, r)
		want := "https://swarmdash.example.com/sso/callback"
		if got != want {
			t.Errorf("ssoRedirectURI() = %q, want %q", got, want)
		}
	})

	t.Run("respects X-Forwarded-Proto", func(t *testing.T) {
		r2 := httptest.NewRequest(http.MethodGet, "/sso/login", nil)
		r2.Host = "admin.internal"
		r2.Header.Set("X-Forwarded-Proto", "https")
		got := ssoRedirectURI(store.SSOConfig{}, r2)
		want := "https://admin.internal/sso/callback"
		if got != want {
			t.Errorf("ssoRedirectURI() = %q, want %q", got, want)
		}
	})
}

// fetchOIDCDiscovery, exchangeOIDCCode and fetchOIDCUserinfo all take the
// provider's URLs as explicit parameters (issuer / doc.TokenEndpoint /
// doc.UserinfoEndpoint), so they can be pointed straight at an
// httptest.Server without needing to fake DNS or swap oidcHTTPClient - the
// same pattern newFakeDocker uses for the Docker Engine API.

func TestFetchOIDCDiscovery(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"authorization_endpoint": "https://idp.example.com/authorize",
			"token_endpoint":         "https://idp.example.com/token",
			"userinfo_endpoint":      "https://idp.example.com/userinfo",
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	doc, err := fetchOIDCDiscovery(context.Background(), ts.URL)
	if err != nil {
		t.Fatalf("fetchOIDCDiscovery: %v", err)
	}
	if doc.TokenEndpoint != "https://idp.example.com/token" {
		t.Errorf("TokenEndpoint = %q", doc.TokenEndpoint)
	}
}

func TestFetchOIDCDiscovery_MissingRequiredFields(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", jsonHandler(map[string]string{}))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	if _, err := fetchOIDCDiscovery(context.Background(), ts.URL); err == nil {
		t.Fatal("expected an error when discovery document lacks required endpoints")
	}
}

func TestExchangeOIDCCode(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if r.FormValue("code") != "auth-code-1" {
			t.Errorf("code = %q, want auth-code-1", r.FormValue("code"))
		}
		if r.FormValue("code_verifier") != "verifier-1" {
			t.Errorf("code_verifier = %q, want verifier-1", r.FormValue("code_verifier"))
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "at-1"})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	doc := oidcDiscovery{TokenEndpoint: ts.URL + "/token"}
	tok, err := exchangeOIDCCode(context.Background(), doc, "client-1", "secret-1", "https://admin/sso/callback", "auth-code-1", "verifier-1")
	if err != nil {
		t.Fatalf("exchangeOIDCCode: %v", err)
	}
	if tok.AccessToken != "at-1" {
		t.Errorf("AccessToken = %q, want at-1", tok.AccessToken)
	}
}

func TestExchangeOIDCCode_ProviderError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", jsonHandler(map[string]string{"error": "invalid_grant", "error_description": "code expired"}))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	doc := oidcDiscovery{TokenEndpoint: ts.URL + "/token"}
	_, err := exchangeOIDCCode(context.Background(), doc, "client-1", "secret-1", "https://admin/sso/callback", "bad-code", "verifier-1")
	if err == nil {
		t.Fatal("expected an error when the provider rejects the exchange")
	}
	if !strings.Contains(err.Error(), "code expired") {
		t.Errorf("error = %v, want it to include the provider's error_description", err)
	}
}

func TestFetchOIDCUserinfo(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer at-1" {
			t.Errorf("Authorization = %q, want Bearer at-1", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"sub": "u1", "email": "u1@example.com"})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	doc := oidcDiscovery{UserinfoEndpoint: ts.URL + "/userinfo"}
	claims, err := fetchOIDCUserinfo(context.Background(), doc, "at-1")
	if err != nil {
		t.Fatalf("fetchOIDCUserinfo: %v", err)
	}
	if claims["email"] != "u1@example.com" {
		t.Errorf("claims[email] = %v, want u1@example.com", claims["email"])
	}
}

func TestFetchOIDCUserinfo_NoEndpoint(t *testing.T) {
	if _, err := fetchOIDCUserinfo(context.Background(), oidcDiscovery{}, "at-1"); err == nil {
		t.Fatal("expected an error when the provider has no userinfo_endpoint")
	}
}
