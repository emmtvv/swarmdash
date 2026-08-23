package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"swarmdash/internal/store"
)

func TestHandleWebhooksPage(t *testing.T) {
	s := newTestServer(t, nil)
	if err := s.store.PutWebhook(store.Webhook{ID: "h1", Name: "notify", URL: "https://example.com/hook"}); err != nil {
		t.Fatalf("seed webhook: %v", err)
	}

	r := withUser(httptest.NewRequest(http.MethodGet, "/settings/webhooks", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleWebhooksPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "notify") {
		t.Errorf("body missing webhook name, got: %s", w.Body.String())
	}
}

func TestHandleWebhookCreate(t *testing.T) {
	s := newTestServer(t, nil)

	form := url.Values{"name": {"alerts"}, "url": {"https://example.com/alerts"}}
	r := withUser(httptest.NewRequest(http.MethodPost, "/settings/webhooks", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleWebhookCreate(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	hooks, err := s.store.ListWebhooks()
	if err != nil {
		t.Fatalf("ListWebhooks: %v", err)
	}
	if len(hooks) != 1 || hooks[0].Name != "alerts" || hooks[0].URL != "https://example.com/alerts" {
		t.Fatalf("unexpected webhooks: %+v", hooks)
	}

	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "webhook.create" || !entries[0].Success {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleWebhookCreate_MissingFields(t *testing.T) {
	s := newTestServer(t, nil)

	form := url.Values{"name": {"alerts"}}
	r := withUser(httptest.NewRequest(http.MethodPost, "/settings/webhooks", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleWebhookCreate(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
	hooks, err := s.store.ListWebhooks()
	if err != nil {
		t.Fatalf("ListWebhooks: %v", err)
	}
	if len(hooks) != 0 {
		t.Fatalf("expected no webhooks created, got: %+v", hooks)
	}
}

func TestHandleWebhookDelete(t *testing.T) {
	s := newTestServer(t, nil)
	if err := s.store.PutWebhook(store.Webhook{ID: "h1", Name: "notify", URL: "https://example.com/hook"}); err != nil {
		t.Fatalf("seed webhook: %v", err)
	}

	r := withUser(httptest.NewRequest(http.MethodPost, "/settings/webhooks/h1/delete", nil), testAdmin)
	r.SetPathValue("id", "h1")
	w := httptest.NewRecorder()
	s.handleWebhookDelete(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	hooks, err := s.store.ListWebhooks()
	if err != nil {
		t.Fatalf("ListWebhooks: %v", err)
	}
	if len(hooks) != 0 {
		t.Fatalf("expected webhook deleted, got: %+v", hooks)
	}
}
