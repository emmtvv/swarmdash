package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/swarm"

	"swarmdash/internal/store"
)

func TestHandleDeployHookCreate(t *testing.T) {
	svc := g2Service("s1", "web_app", "")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services/{id}", jsonHandler(svc))
	mux.HandleFunc("GET /tasks", jsonHandler([]swarm.Task{}))
	mux.HandleFunc("GET /nodes", jsonHandler([]swarm.Node{}))
	mux.HandleFunc("GET /networks", jsonHandler([]network.Summary{}))
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/services/web_app/deploy-hooks", nil), testAdmin)
	r.SetPathValue("name", "web_app")
	w := httptest.NewRecorder()
	s.handleDeployHookCreate(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "/hooks/deploy/sdh_") {
		t.Errorf("body missing new deploy hook URL, got: %s", w.Body.String())
	}

	hooks, err := s.store.ListDeployHooksForService("web_app")
	if err != nil {
		t.Fatalf("ListDeployHooksForService: %v", err)
	}
	if len(hooks) != 1 || hooks[0].ServiceName != "web_app" || hooks[0].CreatedBy != testAdmin.Username {
		t.Fatalf("unexpected deploy hooks: %+v", hooks)
	}

	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "deploy_hook.create" || entries[0].Target != "web_app" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleDeployHookCreate_ServiceNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services/{id}", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such service", http.StatusNotFound)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/services/missing/deploy-hooks", nil), testAdmin)
	r.SetPathValue("name", "missing")
	w := httptest.NewRecorder()
	s.handleDeployHookCreate(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleDeployHookDelete(t *testing.T) {
	s := newTestServer(t, nil)
	if err := s.store.PutDeployHook(store.DeployHook{ID: "h1", Hash: "abc", ServiceName: "web_app"}); err != nil {
		t.Fatalf("seed deploy hook: %v", err)
	}

	r := withUser(httptest.NewRequest(http.MethodPost, "/services/web_app/deploy-hooks/h1/delete", nil), testAdmin)
	r.SetPathValue("name", "web_app")
	r.SetPathValue("id", "h1")
	w := httptest.NewRecorder()
	s.handleDeployHookDelete(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	hooks, err := s.store.ListDeployHooksForService("web_app")
	if err != nil {
		t.Fatalf("ListDeployHooksForService: %v", err)
	}
	if len(hooks) != 0 {
		t.Fatalf("expected deploy hook deleted, got: %+v", hooks)
	}
	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "deploy_hook.delete" || entries[0].Target != "web_app" || entries[0].Detail != "h1" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleDeployHookTrigger(t *testing.T) {
	svc := g2Service("s1", "web_app", "")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services/{id}", jsonHandler(svc))
	mux.HandleFunc("POST /services/{id}/update", jsonHandler(swarm.ServiceUpdateResponse{}))
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	if err := s.store.PutDeployHook(store.DeployHook{ID: "h1", Hash: hashToken("plaintoken"), ServiceName: "web_app"}); err != nil {
		t.Fatalf("seed deploy hook: %v", err)
	}

	r := httptest.NewRequest(http.MethodPost, "/hooks/deploy/plaintoken", nil)
	r.SetPathValue("token", "plaintoken")
	w := httptest.NewRecorder()
	s.handleDeployHookTrigger(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"status":"ok"`) || !strings.Contains(w.Body.String(), "web_app") {
		t.Errorf("unexpected response body: %s", w.Body.String())
	}

	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "deploy_hook.trigger" || entries[0].Username != "webhook:web_app" || !entries[0].Success {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleDeployHookTrigger_InvalidToken(t *testing.T) {
	s := newTestServer(t, nil)

	r := httptest.NewRequest(http.MethodPost, "/hooks/deploy/bogus", nil)
	r.SetPathValue("token", "bogus")
	w := httptest.NewRecorder()
	s.handleDeployHookTrigger(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleDeployHookTrigger_ServiceGone(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services/{id}", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such service", http.StatusNotFound)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	if err := s.store.PutDeployHook(store.DeployHook{ID: "h1", Hash: hashToken("plaintoken"), ServiceName: "gone_app"}); err != nil {
		t.Fatalf("seed deploy hook: %v", err)
	}

	r := httptest.NewRequest(http.MethodPost, "/hooks/deploy/plaintoken", nil)
	r.SetPathValue("token", "plaintoken")
	w := httptest.NewRecorder()
	s.handleDeployHookTrigger(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleDeployHookTrigger_ImageOverride(t *testing.T) {
	svc := g2Service("s1", "web_app", "")
	var capturedImage string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services/{id}", jsonHandler(svc))
	mux.HandleFunc("POST /services/{id}/update", func(w http.ResponseWriter, r *http.Request) {
		var body swarm.ServiceSpec
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.TaskTemplate.ContainerSpec != nil {
			capturedImage = body.TaskTemplate.ContainerSpec.Image
		}
		jsonHandler(swarm.ServiceUpdateResponse{})(w, r)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	if err := s.store.PutDeployHook(store.DeployHook{ID: "h1", Hash: hashToken("plaintoken"), ServiceName: "web_app"}); err != nil {
		t.Fatalf("seed deploy hook: %v", err)
	}

	r := httptest.NewRequest(http.MethodPost, "/hooks/deploy/plaintoken", strings.NewReader(`{"image":"nginx:1.26"}`))
	r.SetPathValue("token", "plaintoken")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleDeployHookTrigger(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if capturedImage != "nginx:1.26" {
		t.Errorf("captured image = %q, want nginx:1.26", capturedImage)
	}
}
