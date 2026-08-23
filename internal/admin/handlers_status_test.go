package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/swarm"
)

func g1StatusMux(nodes []swarm.Node, services []swarm.Service) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/nodes", jsonHandler(nodes))
	mux.HandleFunc("/services", jsonHandler(services))
	return mux
}

func TestHandleStatusPage_Healthy(t *testing.T) {
	nodes := []swarm.Node{
		{ID: "n1", Status: swarm.NodeStatus{State: swarm.NodeStateReady}},
	}
	services := []swarm.Service{
		{ID: "s1", ServiceStatus: &swarm.ServiceStatus{DesiredTasks: 2, RunningTasks: 2}},
	}
	docker := newFakeDocker(t, g1StatusMux(nodes, services))
	s := newTestServer(t, docker)

	r := httptest.NewRequest(http.MethodGet, "/status", nil)
	w := httptest.NewRecorder()
	s.handleStatusPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "1/1") {
		t.Errorf("body missing ready node count, got: %s", body)
	}
}

func TestHandleStatusPage_Degraded(t *testing.T) {
	nodes := []swarm.Node{
		{ID: "n1", Status: swarm.NodeStatus{State: swarm.NodeStateReady}},
		{ID: "n2", Status: swarm.NodeStatus{State: swarm.NodeStateDown}},
	}
	services := []swarm.Service{
		{ID: "s1", ServiceStatus: &swarm.ServiceStatus{DesiredTasks: 3, RunningTasks: 1}},
	}
	docker := newFakeDocker(t, g1StatusMux(nodes, services))
	s := newTestServer(t, docker)

	r := httptest.NewRequest(http.MethodGet, "/status", nil)
	w := httptest.NewRecorder()
	s.handleStatusPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "1/2") {
		t.Errorf("body missing ready node count, got: %s", body)
	}
	if !strings.Contains(body, "Degraded") {
		t.Errorf("body missing degraded indicator, got: %s", body)
	}
}

func TestHandleStatusPage_NodesUnavailable(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/nodes", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := httptest.NewRequest(http.MethodGet, "/status", nil)
	w := httptest.NewRecorder()
	s.handleStatusPage(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleStatusPage_ServicesUnavailable(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/nodes", jsonHandler([]swarm.Node{}))
	mux.HandleFunc("/services", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := httptest.NewRequest(http.MethodGet, "/status", nil)
	w := httptest.NewRecorder()
	s.handleStatusPage(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body: %s", w.Code, w.Body.String())
	}
}
