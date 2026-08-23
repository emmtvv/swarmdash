package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRedirect_PlainRequest(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/settings/webhooks", nil)
	w := httptest.NewRecorder()
	redirect(w, r, "/settings/webhooks")

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/settings/webhooks" {
		t.Errorf("Location = %q, want /settings/webhooks", loc)
	}
	if w.Header().Get("HX-Redirect") != "" {
		t.Errorf("HX-Redirect should not be set for a plain request")
	}
}

func TestRedirect_HTMXRequest(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/settings/webhooks", nil)
	r.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	redirect(w, r, "/settings/webhooks")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if loc := w.Header().Get("HX-Redirect"); loc != "/settings/webhooks" {
		t.Errorf("HX-Redirect = %q, want /settings/webhooks", loc)
	}
	if w.Header().Get("Location") != "" {
		t.Errorf("Location should not be set for an htmx request")
	}
}
