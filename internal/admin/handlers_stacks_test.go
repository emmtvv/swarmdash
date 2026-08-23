package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/swarm"
)

func g2Service(id, name, stack string) swarm.Service {
	svc := swarm.Service{
		ID:   id,
		Meta: swarm.Meta{Version: swarm.Version{Index: 1}},
		Spec: swarm.ServiceSpec{
			Annotations:  swarm.Annotations{Name: name},
			TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{Image: "nginx:alpine"}},
			Mode:         swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: g2Uint64(2)}},
		},
		ServiceStatus: &swarm.ServiceStatus{DesiredTasks: 2, RunningTasks: 2},
	}
	if stack != "" {
		svc.Spec.Labels = map[string]string{stackLabel: stack}
	}
	return svc
}

func g2Uint64(v uint64) *uint64 { return &v }

func TestHandleStacksPage(t *testing.T) {
	services := []swarm.Service{g2Service("s1", "web_app", "web")}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services", jsonHandler(services))
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/stacks", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleStacksPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "web") {
		t.Errorf("body missing stack name, got: %s", w.Body.String())
	}
}

func TestHandleStacksPage_DockerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/stacks", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleStacksPage(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleStackDetail(t *testing.T) {
	services := []swarm.Service{
		g2Service("s1", "web_app", "web"),
		g2Service("s2", "other_app", "other"),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services", jsonHandler(services))
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/stacks/web", nil), testAdmin)
	r.SetPathValue("name", "web")
	w := httptest.NewRecorder()
	s.handleStackDetail(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "web_app") {
		t.Errorf("body missing service name, got: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "other_app") {
		t.Errorf("body should not contain services from other stacks, got: %s", w.Body.String())
	}
}

func TestHandleStackDetail_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services", jsonHandler([]swarm.Service{}))
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/stacks/missing", nil), testAdmin)
	r.SetPathValue("name", "missing")
	w := httptest.NewRecorder()
	s.handleStackDetail(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleStackDelete(t *testing.T) {
	services := []swarm.Service{g2Service("s1", "web_app", "web")}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services", jsonHandler(services))
	mux.HandleFunc("DELETE /services/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/stacks/web/delete", nil), testAdmin)
	r.SetPathValue("name", "web")
	w := httptest.NewRecorder()
	s.handleStackDelete(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "stack.delete" || entries[0].Target != "web" || !entries[0].Success {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleStackDelete_DockerError(t *testing.T) {
	services := []swarm.Service{g2Service("s1", "web_app", "web")}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services", jsonHandler(services))
	mux.HandleFunc("DELETE /services/{id}", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/stacks/web/delete", nil), testAdmin)
	r.SetPathValue("name", "web")
	w := httptest.NewRecorder()
	s.handleStackDelete(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleStackRestart(t *testing.T) {
	svc := g2Service("s1", "web_app", "web")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services", jsonHandler([]swarm.Service{svc}))
	mux.HandleFunc("GET /services/{id}", jsonHandler(svc))
	mux.HandleFunc("POST /services/{id}/update", jsonHandler(swarm.ServiceUpdateResponse{}))
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/stacks/web/restart", nil), testAdmin)
	r.SetPathValue("name", "web")
	w := httptest.NewRecorder()
	s.handleStackRestart(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "stack.restart" || entries[0].Detail != "services=1" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleStackRestart_UpdateError(t *testing.T) {
	svc := g2Service("s1", "web_app", "web")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services", jsonHandler([]swarm.Service{svc}))
	mux.HandleFunc("GET /services/{id}", jsonHandler(svc))
	mux.HandleFunc("POST /services/{id}/update", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/stacks/web/restart", nil), testAdmin)
	r.SetPathValue("name", "web")
	w := httptest.NewRecorder()
	s.handleStackRestart(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}
