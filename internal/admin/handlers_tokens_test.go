package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"swarmdash/internal/store"
)

func TestHandleTokensPage(t *testing.T) {
	s := newTestServer(t, nil)
	_, rec := newAPIToken("ci", "admin", "admin-user")
	if err := s.store.PutAPIToken(rec); err != nil {
		t.Fatalf("seed token: %v", err)
	}

	r := withUser(httptest.NewRequest(http.MethodGet, "/settings/tokens", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleTokensPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "ci") {
		t.Errorf("body missing token name, got: %s", w.Body.String())
	}
}

func TestHandleTokenCreate(t *testing.T) {
	s := newTestServer(t, nil)

	form := url.Values{"name": {"deploy-bot"}, "role": {"viewer"}}
	r := withUser(httptest.NewRequest(http.MethodPost, "/settings/tokens", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleTokenCreate(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "sdt_") {
		t.Errorf("body missing plaintext token, got: %s", w.Body.String())
	}

	tokens, err := s.store.ListAPITokens()
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	if len(tokens) != 1 || tokens[0].Name != "deploy-bot" || tokens[0].Role != "viewer" {
		t.Fatalf("unexpected tokens: %+v", tokens)
	}

	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "token.create" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleTokenCreate_Defaults(t *testing.T) {
	s := newTestServer(t, nil)

	r := withUser(httptest.NewRequest(http.MethodPost, "/settings/tokens", strings.NewReader("")), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleTokenCreate(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	tokens, err := s.store.ListAPITokens()
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	if len(tokens) != 1 || tokens[0].Name != "unnamed" || tokens[0].Role != "admin" {
		t.Fatalf("unexpected default token: %+v", tokens)
	}
}

func TestHandleTokenCreate_InvalidRole(t *testing.T) {
	s := newTestServer(t, nil)

	form := url.Values{"name": {"bad"}, "role": {"superuser"}}
	r := withUser(httptest.NewRequest(http.MethodPost, "/settings/tokens", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleTokenCreate(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
	tokens, err := s.store.ListAPITokens()
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	if len(tokens) != 0 {
		t.Fatalf("expected no tokens created, got: %+v", tokens)
	}
}

func TestHandleTokenDelete(t *testing.T) {
	s := newTestServer(t, nil)
	if err := s.store.PutAPIToken(store.APIToken{ID: "tok1", Name: "ci"}); err != nil {
		t.Fatalf("seed token: %v", err)
	}

	r := withUser(httptest.NewRequest(http.MethodPost, "/settings/tokens/tok1/delete", nil), testAdmin)
	r.SetPathValue("id", "tok1")
	w := httptest.NewRecorder()
	s.handleTokenDelete(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	tokens, err := s.store.ListAPITokens()
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	if len(tokens) != 0 {
		t.Fatalf("expected token deleted, got: %+v", tokens)
	}
}
