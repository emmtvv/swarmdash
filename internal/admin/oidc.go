package admin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// This file implements just enough of OpenID Connect (Authorization Code
// flow with PKCE) to support "Sign in with <provider>": discovery, code
// exchange, and reading identity claims from the userinfo endpoint.
// Deliberately not a general-purpose OIDC client - notably, it never
// verifies the id_token's JWT signature. Identity is instead established
// by calling the provider's userinfo_endpoint with the access token
// obtained from the (server-to-server, TLS) token exchange, which gives
// the same trust guarantee (the provider itself vouches for the claims
// over a channel only it and admin see) without needing a JWKS/JWT
// verification stack.

type oidcDiscovery struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserinfoEndpoint      string `json:"userinfo_endpoint"`
}

var oidcHTTPClient = &http.Client{Timeout: 10 * time.Second}

const discoveryTTL = 10 * time.Minute

// discoveryCache avoids re-fetching the provider's discovery document on
// every single login. Keyed by issuer only (in practice there's ever just
// one configured at a time) with a short TTL so a provider-side endpoint
// migration is picked up without a restart.
var discoveryCache struct {
	mu      sync.Mutex
	issuer  string
	doc     oidcDiscovery
	fetched time.Time
}

func fetchOIDCDiscovery(ctx context.Context, issuer string) (oidcDiscovery, error) {
	discoveryCache.mu.Lock()
	if discoveryCache.issuer == issuer && time.Since(discoveryCache.fetched) < discoveryTTL {
		doc := discoveryCache.doc
		discoveryCache.mu.Unlock()
		return doc, nil
	}
	discoveryCache.mu.Unlock()

	wellKnown := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wellKnown, nil)
	if err != nil {
		return oidcDiscovery{}, err
	}
	resp, err := oidcHTTPClient.Do(req)
	if err != nil {
		return oidcDiscovery{}, fmt.Errorf("fetch discovery document: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return oidcDiscovery{}, fmt.Errorf("discovery document returned %s", resp.Status)
	}
	var doc oidcDiscovery
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return oidcDiscovery{}, fmt.Errorf("decode discovery document: %w", err)
	}
	if doc.AuthorizationEndpoint == "" || doc.TokenEndpoint == "" {
		return oidcDiscovery{}, errors.New("discovery document is missing authorization_endpoint/token_endpoint")
	}

	discoveryCache.mu.Lock()
	discoveryCache.issuer = issuer
	discoveryCache.doc = doc
	discoveryCache.fetched = time.Now()
	discoveryCache.mu.Unlock()
	return doc, nil
}

// newPKCEPair generates an S256 PKCE code_verifier/code_challenge pair
// (RFC 7636). Used unconditionally, whether or not the provider requires
// it - free defense in depth against authorization-code interception.
func newPKCEPair() (verifier, challenge string) {
	verifier = randomToken(32) // 64 hex chars: within the RFC's 43-128 char range
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge
}

func oidcAuthURL(doc oidcDiscovery, clientID, redirectURI, scopes, state, codeChallenge string) string {
	v := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {scopes},
		"state":                 {state},
		"code_challenge":        {codeChallenge},
		"code_challenge_method": {"S256"},
	}
	sep := "?"
	if strings.Contains(doc.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	return doc.AuthorizationEndpoint + sep + v.Encode()
}

type oidcTokenResponse struct {
	AccessToken string `json:"access_token"`
	Error       string `json:"error"`
	ErrorDesc   string `json:"error_description"`
}

func exchangeOIDCCode(ctx context.Context, doc oidcDiscovery, clientID, clientSecret, redirectURI, code, verifier string) (oidcTokenResponse, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {verifier},
	}
	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, doc.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return oidcTokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := oidcHTTPClient.Do(req)
	if err != nil {
		return oidcTokenResponse{}, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return oidcTokenResponse{}, err
	}
	var tok oidcTokenResponse
	if jsonErr := json.Unmarshal(body, &tok); jsonErr != nil {
		return oidcTokenResponse{}, fmt.Errorf("decode token response: %w", jsonErr)
	}
	if resp.StatusCode != http.StatusOK || tok.Error != "" {
		msg := tok.ErrorDesc
		if msg == "" {
			msg = tok.Error
		}
		if msg == "" {
			msg = resp.Status
		}
		return oidcTokenResponse{}, fmt.Errorf("token endpoint rejected the exchange: %s", msg)
	}
	if tok.AccessToken == "" {
		return oidcTokenResponse{}, errors.New("token response had no access_token")
	}
	return tok, nil
}

// fetchOIDCUserinfo calls the provider's userinfo endpoint with the access
// token and returns the raw claims (sub, email, preferred_username, ...).
func fetchOIDCUserinfo(ctx context.Context, doc oidcDiscovery, accessToken string) (map[string]any, error) {
	if doc.UserinfoEndpoint == "" {
		return nil, errors.New("identity provider did not advertise a userinfo_endpoint")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, doc.UserinfoEndpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := oidcHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("userinfo request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo endpoint returned %s", resp.Status)
	}
	var claims map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&claims); err != nil {
		return nil, fmt.Errorf("decode userinfo response: %w", err)
	}
	return claims, nil
}
