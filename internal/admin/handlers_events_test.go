package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarmdash/internal/store"
)

func TestHandleEventsPage(t *testing.T) {
	s := newTestServer(t, nil)
	if err := s.store.AppendTaskEvent(store.TaskEvent{ServiceName: "web", TaskID: "t1", Node: "node-a", State: "running"}); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if err := s.store.AppendTaskEvent(store.TaskEvent{ServiceName: "worker", TaskID: "t2", Node: "node-b", State: "failed"}); err != nil {
		t.Fatalf("seed event: %v", err)
	}

	r := withUser(httptest.NewRequest(http.MethodGet, "/events", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleEventsPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "web") || !strings.Contains(body, "worker") {
		t.Errorf("body missing expected event service names, got: %s", body)
	}
}

func TestHandleEventsPage_ServiceFilter(t *testing.T) {
	s := newTestServer(t, nil)
	if err := s.store.AppendTaskEvent(store.TaskEvent{ServiceName: "web", TaskID: "t1", Node: "node-a", State: "running"}); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if err := s.store.AppendTaskEvent(store.TaskEvent{ServiceName: "worker", TaskID: "t2", Node: "node-b", State: "failed"}); err != nil {
		t.Fatalf("seed event: %v", err)
	}

	r := withUser(httptest.NewRequest(http.MethodGet, "/events?service=web", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleEventsPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "web") {
		t.Errorf("body missing filtered service, got: %s", body)
	}
	if strings.Contains(body, "worker") {
		t.Errorf("body should not include events for other services, got: %s", body)
	}
}
