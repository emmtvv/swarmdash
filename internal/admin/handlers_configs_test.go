package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/swarm"
)

func g1ConfigsMux(configs []swarm.Config) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/configs", jsonHandler(configs))
	mux.HandleFunc("/configs/create", jsonHandler(swarm.ConfigCreateResponse{ID: "cfg-new"}))
	mux.HandleFunc("/configs/cfg1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func TestHandleConfigsPage(t *testing.T) {
	configs := []swarm.Config{
		{ID: "cfg1", Spec: swarm.ConfigSpec{Annotations: swarm.Annotations{Name: "app-config"}}},
	}
	docker := newFakeDocker(t, g1ConfigsMux(configs))
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/configs", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleConfigsPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "app-config") {
		t.Errorf("body missing config name, got: %s", w.Body.String())
	}
}

func TestHandleConfigsPage_DockerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/configs", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/configs", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleConfigsPage(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleConfigCreate(t *testing.T) {
	docker := newFakeDocker(t, g1ConfigsMux(nil))
	s := newTestServer(t, docker)

	form := url.Values{"name": {"app-config"}, "data": {"key=value"}}
	r := withUser(httptest.NewRequest(http.MethodPost, "/configs", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleConfigCreate(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "config.create" || entries[0].Target != "app-config" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleConfigCreate_MissingFields(t *testing.T) {
	s := newTestServer(t, nil)

	form := url.Values{"name": {"app-config"}}
	r := withUser(httptest.NewRequest(http.MethodPost, "/configs", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleConfigCreate(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleConfigCreate_DockerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/configs/create", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	form := url.Values{"name": {"app-config"}, "data": {"key=value"}}
	r := withUser(httptest.NewRequest(http.MethodPost, "/configs", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleConfigCreate(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleConfigDelete(t *testing.T) {
	docker := newFakeDocker(t, g1ConfigsMux(nil))
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/configs/cfg1/delete", nil), testAdmin)
	r.SetPathValue("id", "cfg1")
	w := httptest.NewRecorder()
	s.handleConfigDelete(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "config.delete" || entries[0].Target != "cfg1" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleConfigDelete_DockerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/configs/cfg1", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/configs/cfg1/delete", nil), testAdmin)
	r.SetPathValue("id", "cfg1")
	w := httptest.NewRecorder()
	s.handleConfigDelete(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}
