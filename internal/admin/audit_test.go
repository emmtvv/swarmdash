package admin

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarmdash/internal/store"
)

func TestHandleAuditPage(t *testing.T) {
	s := newTestServer(t, nil)
	if err := s.store.AppendAudit(store.AuditEntry{Username: "alice", Action: "service.scale", Target: "web", Success: true}); err != nil {
		t.Fatalf("seed audit: %v", err)
	}
	if err := s.store.AppendAudit(store.AuditEntry{Username: "bob", Action: "service.delete", Target: "api", Success: false, Error: "boom"}); err != nil {
		t.Fatalf("seed audit: %v", err)
	}

	r := withUser(httptest.NewRequest(http.MethodGet, "/audit", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleAuditPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "alice") || !strings.Contains(body, "service.scale") {
		t.Errorf("body missing first entry, got: %s", body)
	}
	if !strings.Contains(body, "bob") || !strings.Contains(body, "failed") {
		t.Errorf("body missing failed entry, got: %s", body)
	}
}

func TestHandleAuditPage_Empty(t *testing.T) {
	s := newTestServer(t, nil)

	r := withUser(httptest.NewRequest(http.MethodGet, "/audit", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleAuditPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "No audit entries yet.") {
		t.Errorf("body missing empty-state message, got: %s", w.Body.String())
	}
}

func TestHandleAuditPage_Pagination(t *testing.T) {
	s := newTestServer(t, nil)
	for i := 1; i <= 55; i++ {
		if err := s.store.AppendAudit(store.AuditEntry{Username: fmt.Sprintf("user%d", i), Action: "login"}); err != nil {
			t.Fatalf("seed audit %d: %v", i, err)
		}
	}

	r1 := withUser(httptest.NewRequest(http.MethodGet, "/audit", nil), testAdmin)
	w1 := httptest.NewRecorder()
	s.handleAuditPage(w1, r1)
	if w1.Code != http.StatusOK {
		t.Fatalf("page 1 status = %d, want 200", w1.Code)
	}
	body1 := w1.Body.String()
	if !strings.Contains(body1, "user55") {
		t.Errorf("page 1 missing most recent entry user55, got: %s", body1)
	}
	if strings.Contains(body1, "user1<") {
		t.Errorf("page 1 unexpectedly contains oldest entry user1: %s", body1)
	}

	r2 := withUser(httptest.NewRequest(http.MethodGet, "/audit?page=2", nil), testAdmin)
	w2 := httptest.NewRecorder()
	s.handleAuditPage(w2, r2)
	if w2.Code != http.StatusOK {
		t.Fatalf("page 2 status = %d, want 200", w2.Code)
	}
	body2 := w2.Body.String()
	if !strings.Contains(body2, "user1<") {
		t.Errorf("page 2 missing oldest entry user1, got: %s", body2)
	}
	if strings.Contains(body2, "user55") {
		t.Errorf("page 2 unexpectedly contains most recent entry user55: %s", body2)
	}
}
