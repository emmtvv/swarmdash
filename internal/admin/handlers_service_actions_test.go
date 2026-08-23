package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/swarm"
)

func g2ServiceUpdateMux(svc swarm.Service) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services/{id}", jsonHandler(svc))
	mux.HandleFunc("POST /services/{id}/update", jsonHandler(swarm.ServiceUpdateResponse{}))
	return mux
}

func TestHandleServiceScale(t *testing.T) {
	svc := g2Service("s1", "web_app", "")
	docker := newFakeDocker(t, g2ServiceUpdateMux(svc))
	s := newTestServer(t, docker)

	form := "replicas=5"
	r := withUser(httptest.NewRequest(http.MethodPost, "/services/web_app/scale", strings.NewReader(form)), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("name", "web_app")
	w := httptest.NewRecorder()
	s.handleServiceScale(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "service.scale" || entries[0].Detail != "replicas=5" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleServiceScale_BadForm(t *testing.T) {
	s := newTestServer(t, nil)

	form := "replicas=notanumber"
	r := withUser(httptest.NewRequest(http.MethodPost, "/services/web_app/scale", strings.NewReader(form)), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("name", "web_app")
	w := httptest.NewRecorder()
	s.handleServiceScale(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleServiceScale_NotReplicated(t *testing.T) {
	svc := g2Service("s1", "global_app", "")
	svc.Spec.Mode = swarm.ServiceMode{Global: &swarm.GlobalService{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services/{id}", jsonHandler(svc))
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	form := "replicas=5"
	r := withUser(httptest.NewRequest(http.MethodPost, "/services/global_app/scale", strings.NewReader(form)), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("name", "global_app")
	w := httptest.NewRecorder()
	s.handleServiceScale(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleServiceUpdateImage(t *testing.T) {
	svc := g2Service("s1", "web_app", "")
	docker := newFakeDocker(t, g2ServiceUpdateMux(svc))
	s := newTestServer(t, docker)

	form := "image=nginx:1.25"
	r := withUser(httptest.NewRequest(http.MethodPost, "/services/web_app/image", strings.NewReader(form)), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("name", "web_app")
	w := httptest.NewRecorder()
	s.handleServiceUpdateImage(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "service.update_image" || entries[0].Detail != "image=nginx:1.25" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleServiceUpdateImage_MissingImage(t *testing.T) {
	s := newTestServer(t, nil)

	r := withUser(httptest.NewRequest(http.MethodPost, "/services/web_app/image", strings.NewReader("")), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("name", "web_app")
	w := httptest.NewRecorder()
	s.handleServiceUpdateImage(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleServiceUpdateImage_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services/{id}", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such service", http.StatusNotFound)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	form := "image=nginx:1.25"
	r := withUser(httptest.NewRequest(http.MethodPost, "/services/missing/image", strings.NewReader(form)), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("name", "missing")
	w := httptest.NewRecorder()
	s.handleServiceUpdateImage(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleServiceRollback(t *testing.T) {
	svc := g2Service("s1", "web_app", "")
	svc.PreviousSpec = &swarm.ServiceSpec{}
	docker := newFakeDocker(t, g2ServiceUpdateMux(svc))
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/services/web_app/rollback", nil), testAdmin)
	r.SetPathValue("name", "web_app")
	w := httptest.NewRecorder()
	s.handleServiceRollback(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "service.rollback" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleServiceRollback_NoPreviousSpec(t *testing.T) {
	svc := g2Service("s1", "web_app", "")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services/{id}", jsonHandler(svc))
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/services/web_app/rollback", nil), testAdmin)
	r.SetPathValue("name", "web_app")
	w := httptest.NewRecorder()
	s.handleServiceRollback(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleServiceRestart(t *testing.T) {
	svc := g2Service("s1", "web_app", "")
	docker := newFakeDocker(t, g2ServiceUpdateMux(svc))
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/services/web_app/restart", nil), testAdmin)
	r.SetPathValue("name", "web_app")
	w := httptest.NewRecorder()
	s.handleServiceRestart(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "service.restart" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleServiceUpdateLatest(t *testing.T) {
	svc := g2Service("s1", "web_app", "")
	svc.Spec.TaskTemplate.ContainerSpec.Image = "nginx:alpine@sha256:abc123"
	docker := newFakeDocker(t, g2ServiceUpdateMux(svc))
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/services/web_app/latest", nil), testAdmin)
	r.SetPathValue("name", "web_app")
	w := httptest.NewRecorder()
	s.handleServiceUpdateLatest(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "service.update_latest" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleServiceUpdateLatest_GetServiceError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services/{id}", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such service", http.StatusNotFound)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/services/missing/latest", nil), testAdmin)
	r.SetPathValue("name", "missing")
	w := httptest.NewRecorder()
	s.handleServiceUpdateLatest(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleServiceDelete_Standalone(t *testing.T) {
	svc := g2Service("s1", "web_app", "")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services/{id}", jsonHandler(svc))
	mux.HandleFunc("DELETE /services/{id}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/services/web_app/delete", nil), testAdmin)
	r.SetPathValue("name", "web_app")
	w := httptest.NewRecorder()
	s.handleServiceDelete(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "/services" {
		t.Errorf("Location = %q, want /services", loc)
	}
	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "service.delete" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleServiceDelete_InStack(t *testing.T) {
	svc := g2Service("s1", "web_app", "web")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services/{id}", jsonHandler(svc))
	mux.HandleFunc("DELETE /services/{id}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/services/web_app/delete", nil), testAdmin)
	r.SetPathValue("name", "web_app")
	w := httptest.NewRecorder()
	s.handleServiceDelete(w, r)

	if loc := w.Header().Get("Location"); loc != "/stacks/web" {
		t.Errorf("Location = %q, want /stacks/web", loc)
	}
}

func TestHandleServiceDelete_Error(t *testing.T) {
	svc := g2Service("s1", "web_app", "")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services/{id}", jsonHandler(svc))
	mux.HandleFunc("DELETE /services/{id}", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/services/web_app/delete", nil), testAdmin)
	r.SetPathValue("name", "web_app")
	w := httptest.NewRecorder()
	s.handleServiceDelete(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleServicesBulk_Restart(t *testing.T) {
	svc1 := g2Service("s1", "web_app", "")
	svc2 := g2Service("s2", "api_app", "")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		switch id {
		case "s1":
			jsonHandler(svc1)(w, r)
		case "s2":
			jsonHandler(svc2)(w, r)
		default:
			http.Error(w, "no such service", http.StatusNotFound)
		}
	})
	mux.HandleFunc("POST /services/{id}/update", jsonHandler(swarm.ServiceUpdateResponse{}))
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	form := "action=restart&ids=s1&ids=s2"
	r := withUser(httptest.NewRequest(http.MethodPost, "/services/bulk", strings.NewReader(form)), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleServicesBulk(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 audit entries, got %d: %+v", len(entries), entries)
	}
	for _, e := range entries {
		if e.Action != "service.bulk_restart" || !e.Success {
			t.Errorf("unexpected audit entry: %+v", e)
		}
	}
}

func TestHandleServicesBulk_PartialFailure(t *testing.T) {
	svc1 := g2Service("s1", "web_app", "")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") == "s1" {
			jsonHandler(svc1)(w, r)
			return
		}
		http.Error(w, "no such service", http.StatusNotFound)
	})
	mux.HandleFunc("POST /services/{id}/update", jsonHandler(swarm.ServiceUpdateResponse{}))
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	form := "action=restart&ids=s1&ids=missing"
	r := withUser(httptest.NewRequest(http.MethodPost, "/services/bulk", strings.NewReader(form)), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleServicesBulk(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 audit entries, got %d: %+v", len(entries), entries)
	}
	var successes, failures int
	for _, e := range entries {
		if e.Success {
			successes++
		} else {
			failures++
		}
	}
	if successes != 1 || failures != 1 {
		t.Errorf("successes=%d failures=%d, want 1/1", successes, failures)
	}
}

func TestHandleServicesBulk_UnknownAction(t *testing.T) {
	s := newTestServer(t, nil)

	form := "action=nuke&ids=s1"
	r := withUser(httptest.NewRequest(http.MethodPost, "/services/bulk", strings.NewReader(form)), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleServicesBulk(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleServicesBulk_ScaleBadReplicas(t *testing.T) {
	s := newTestServer(t, nil)

	form := "action=scale&ids=s1&replicas=notanumber"
	r := withUser(httptest.NewRequest(http.MethodPost, "/services/bulk", strings.NewReader(form)), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleServicesBulk(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}
