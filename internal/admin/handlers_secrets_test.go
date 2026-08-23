package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/swarm"
)

func g1SecretsMux(secrets []swarm.Secret) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/secrets", jsonHandler(secrets))
	mux.HandleFunc("/secrets/create", jsonHandler(swarm.SecretCreateResponse{ID: "sec-new"}))
	mux.HandleFunc("/secrets/sec1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func TestHandleSecretsPage(t *testing.T) {
	secrets := []swarm.Secret{
		{ID: "sec1", Spec: swarm.SecretSpec{Annotations: swarm.Annotations{Name: "db-password"}}},
	}
	docker := newFakeDocker(t, g1SecretsMux(secrets))
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/secrets", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleSecretsPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "db-password") {
		t.Errorf("body missing secret name, got: %s", w.Body.String())
	}
}

func TestHandleSecretsPage_DockerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/secrets", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/secrets", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleSecretsPage(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleSecretCreate(t *testing.T) {
	docker := newFakeDocker(t, g1SecretsMux(nil))
	s := newTestServer(t, docker)

	form := url.Values{"name": {"db-password"}, "data": {"s3cr3t"}}
	r := withUser(httptest.NewRequest(http.MethodPost, "/secrets", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleSecretCreate(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "secret.create" || entries[0].Target != "db-password" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleSecretCreate_MissingFields(t *testing.T) {
	s := newTestServer(t, nil)

	form := url.Values{"name": {"db-password"}}
	r := withUser(httptest.NewRequest(http.MethodPost, "/secrets", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleSecretCreate(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleSecretCreate_DockerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/secrets/create", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	form := url.Values{"name": {"db-password"}, "data": {"s3cr3t"}}
	r := withUser(httptest.NewRequest(http.MethodPost, "/secrets", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleSecretCreate(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleSecretDelete(t *testing.T) {
	docker := newFakeDocker(t, g1SecretsMux(nil))
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/secrets/sec1/delete", nil), testAdmin)
	r.SetPathValue("id", "sec1")
	w := httptest.NewRecorder()
	s.handleSecretDelete(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "secret.delete" || entries[0].Target != "sec1" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleSecretDelete_DockerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/secrets/sec1", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/secrets/sec1/delete", nil), testAdmin)
	r.SetPathValue("id", "sec1")
	w := httptest.NewRecorder()
	s.handleSecretDelete(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}
