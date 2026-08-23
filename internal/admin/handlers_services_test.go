package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/swarm"
)

func g2ServiceDetailMux(svc swarm.Service, tasks []swarm.Task, nodes []swarm.Node) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services/{id}", jsonHandler(svc))
	mux.HandleFunc("GET /tasks", jsonHandler(tasks))
	mux.HandleFunc("GET /nodes", jsonHandler(nodes))
	mux.HandleFunc("GET /networks", jsonHandler([]network.Summary{}))
	return mux
}

func TestHandleServicesPage(t *testing.T) {
	services := []swarm.Service{g2Service("s1", "web_app", "")}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services", jsonHandler(services))
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/services", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleServicesPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "web_app") {
		t.Errorf("body missing service name, got: %s", w.Body.String())
	}
}

func TestHandleServicesPage_DockerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/services", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleServicesPage(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleServiceDetail(t *testing.T) {
	svc := g2Service("s1", "web_app", "")
	tasks := []swarm.Task{{ID: "t1", ServiceID: "s1", Status: swarm.TaskStatus{State: swarm.TaskStateRunning}}}
	nodes := []swarm.Node{{ID: "n1", Description: swarm.NodeDescription{Hostname: "worker-1"}}}
	docker := newFakeDocker(t, g2ServiceDetailMux(svc, tasks, nodes))
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/services/web_app", nil), testAdmin)
	r.SetPathValue("name", "web_app")
	w := httptest.NewRecorder()
	s.handleServiceDetail(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "web_app") {
		t.Errorf("body missing service name, got: %s", w.Body.String())
	}
}

func TestHandleServiceDetail_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services/{id}", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such service", http.StatusNotFound)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/services/missing", nil), testAdmin)
	r.SetPathValue("name", "missing")
	w := httptest.NewRecorder()
	s.handleServiceDetail(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleServiceDetail_TaskListError(t *testing.T) {
	svc := g2Service("s1", "web_app", "")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services/{id}", jsonHandler(svc))
	mux.HandleFunc("GET /tasks", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/services/web_app", nil), testAdmin)
	r.SetPathValue("name", "web_app")
	w := httptest.NewRecorder()
	s.handleServiceDetail(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}
