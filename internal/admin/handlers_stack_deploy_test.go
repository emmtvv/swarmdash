package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/swarm"
)

const g2SimpleCompose = `services:
  web:
    image: nginx:alpine
    deploy:
      replicas: 2
`

func g2DeployMux(serviceNotFound bool, existing swarm.Service) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services/{id}", func(w http.ResponseWriter, r *http.Request) {
		if serviceNotFound {
			http.Error(w, "no such service", http.StatusNotFound)
			return
		}
		jsonHandler(existing)(w, r)
	})
	mux.HandleFunc("POST /services/create", jsonHandler(swarm.ServiceCreateResponse{ID: "new1"}))
	mux.HandleFunc("POST /services/{id}/update", jsonHandler(swarm.ServiceUpdateResponse{}))
	return mux
}

func TestHandleStackDeployPage(t *testing.T) {
	s := newTestServer(t, nil)

	r := withUser(httptest.NewRequest(http.MethodGet, "/stacks/deploy", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleStackDeployPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleStackDeployPage_WithTemplate(t *testing.T) {
	s := newTestServer(t, nil)

	r := withUser(httptest.NewRequest(http.MethodGet, "/stacks/deploy?template=postgres", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleStackDeployPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "postgres:16-alpine") {
		t.Errorf("body missing template compose content, got: %s", w.Body.String())
	}
}

func TestHandleTemplatesPage(t *testing.T) {
	s := newTestServer(t, nil)

	r := withUser(httptest.NewRequest(http.MethodGet, "/stacks/templates", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleTemplatesPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "PostgreSQL") {
		t.Errorf("body missing built-in template name, got: %s", w.Body.String())
	}
}

func TestHandleStackDeployPreview_Create(t *testing.T) {
	docker := newFakeDocker(t, g2DeployMux(true, swarm.Service{}))
	s := newTestServer(t, docker)

	form := url.Values{"name": {"myapp"}, "compose": {g2SimpleCompose}}
	r := withUser(httptest.NewRequest(http.MethodPost, "/stacks/deploy/preview", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleStackDeployPreview(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "create") {
		t.Errorf("body missing create action, got: %s", w.Body.String())
	}
}

func TestHandleStackDeployPreview_InvalidName(t *testing.T) {
	s := newTestServer(t, nil)

	form := url.Values{"name": {"bad name!"}, "compose": {g2SimpleCompose}}
	r := withUser(httptest.NewRequest(http.MethodPost, "/stacks/deploy/preview", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleStackDeployPreview(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid stack name") {
		t.Errorf("body missing invalid name error, got: %s", w.Body.String())
	}
}

func TestHandleStackDeployPreview_BadYAML(t *testing.T) {
	s := newTestServer(t, nil)

	form := url.Values{"name": {"myapp"}, "compose": {"not: [valid"}}
	r := withUser(httptest.NewRequest(http.MethodPost, "/stacks/deploy/preview", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleStackDeployPreview(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "error-box") {
		t.Errorf("body missing rendered parse error, got: %s", w.Body.String())
	}
}

func TestHandleStackDeploySubmit_Create(t *testing.T) {
	docker := newFakeDocker(t, g2DeployMux(true, swarm.Service{}))
	s := newTestServer(t, docker)

	form := url.Values{"name": {"myapp"}, "compose": {g2SimpleCompose}}
	r := withUser(httptest.NewRequest(http.MethodPost, "/stacks/deploy", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleStackDeploySubmit(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "/stacks/myapp" {
		t.Errorf("Location = %q, want /stacks/myapp", loc)
	}
	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "stack.deploy" || entries[0].Detail != "created=1 updated=0" || !entries[0].Success {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleStackDeploySubmit_InvalidName(t *testing.T) {
	s := newTestServer(t, nil)

	form := url.Values{"name": {"bad name!"}, "compose": {g2SimpleCompose}}
	r := withUser(httptest.NewRequest(http.MethodPost, "/stacks/deploy", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleStackDeploySubmit(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid stack name") {
		t.Errorf("body missing invalid name error, got: %s", w.Body.String())
	}
	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no audit entries, got: %+v", entries)
	}
}

func TestHandleStackDeploySubmit_ApplyError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services/{id}", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such service", http.StatusNotFound)
	})
	mux.HandleFunc("POST /services/create", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	form := url.Values{"name": {"myapp"}, "compose": {g2SimpleCompose}}
	r := withUser(httptest.NewRequest(http.MethodPost, "/stacks/deploy", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleStackDeploySubmit(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "error-box") {
		t.Errorf("body missing rendered apply error, got: %s", w.Body.String())
	}
	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "stack.deploy" || entries[0].Success {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleStackExport(t *testing.T) {
	services := []swarm.Service{g2Service("s1", "myapp_web", "myapp")}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services", jsonHandler(services))
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/stacks/myapp/export", nil), testAdmin)
	r.SetPathValue("name", "myapp")
	w := httptest.NewRecorder()
	s.handleStackExport(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Content-Disposition"), "myapp.yml") {
		t.Errorf("Content-Disposition = %q, want myapp.yml", w.Header().Get("Content-Disposition"))
	}
	if !strings.Contains(w.Body.String(), "nginx:alpine") {
		t.Errorf("body missing exported image, got: %s", w.Body.String())
	}
}

func TestHandleStackExport_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services", jsonHandler([]swarm.Service{}))
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/stacks/missing/export", nil), testAdmin)
	r.SetPathValue("name", "missing")
	w := httptest.NewRecorder()
	s.handleStackExport(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleServiceExport(t *testing.T) {
	svc := g2Service("s1", "web_app", "")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services/{id}", jsonHandler(svc))
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/services/web_app/export", nil), testAdmin)
	r.SetPathValue("name", "web_app")
	w := httptest.NewRecorder()
	s.handleServiceExport(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "nginx:alpine") {
		t.Errorf("body missing exported image, got: %s", w.Body.String())
	}
}

func TestHandleServiceExport_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services/{id}", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such service", http.StatusNotFound)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/services/missing/export", nil), testAdmin)
	r.SetPathValue("name", "missing")
	w := httptest.NewRecorder()
	s.handleServiceExport(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}
