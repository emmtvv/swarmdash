package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandleExecProxy_RejectsOverCapacity guards the concurrent-exec-session
// cap: with execSlots full, a new session must be turned away with 503
// before it ever tries to resolve the task or dial an agent, rather than an
// unbounded number of interactive shells being allowed to pile up.
func TestHandleExecProxy_RejectsOverCapacity(t *testing.T) {
	s := newTestServer(t, nil)
	s.execSlots = make(chan struct{}, 1)
	s.execSlots <- struct{}{} // fill the only slot

	r := httptest.NewRequest(http.MethodGet, "/ws/exec/task1", nil)
	r.SetPathValue("taskID", "task1")
	w := httptest.NewRecorder()

	s.handleExecProxy(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

func TestHandleExecProxy_AcquiresAndReleasesSlotOnFailure(t *testing.T) {
	// A docker mux with no /tasks/missing route, so taskContainer fails
	// cleanly with a 404 instead of panicking on a nil client.
	docker := newFakeDocker(t, http.NewServeMux())
	s := newTestServer(t, docker)
	s.execSlots = make(chan struct{}, 1)

	r := httptest.NewRequest(http.MethodGet, "/ws/exec/missing", nil)
	r.SetPathValue("taskID", "missing")
	w := httptest.NewRecorder()

	s.handleExecProxy(w, r)

	if w.Code == http.StatusServiceUnavailable {
		t.Fatalf("first call should not see the capacity error")
	}
	select {
	case s.execSlots <- struct{}{}:
	default:
		t.Error("slot was not released after the handler returned")
	}
}
