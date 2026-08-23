package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/network"
)

func g1NetworksMux(nets []network.Summary) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/networks", jsonHandler(nets))
	mux.HandleFunc("/networks/create", jsonHandler(network.CreateResponse{ID: "net-new"}))
	mux.HandleFunc("/networks/net1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func TestHandleNetworksPage(t *testing.T) {
	nets := []network.Summary{
		{ID: "net1", Name: "backend", Driver: "overlay", Scope: "swarm"},
	}
	docker := newFakeDocker(t, g1NetworksMux(nets))
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/networks", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleNetworksPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "backend") {
		t.Errorf("body missing network name, got: %s", w.Body.String())
	}
}

func TestHandleNetworksPage_DockerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/networks", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/networks", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleNetworksPage(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleNetworkCreate(t *testing.T) {
	docker := newFakeDocker(t, g1NetworksMux(nil))
	s := newTestServer(t, docker)

	form := url.Values{"name": {"backend"}, "attachable": {"on"}}
	r := withUser(httptest.NewRequest(http.MethodPost, "/networks", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleNetworkCreate(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "network.create" || entries[0].Target != "backend" || entries[0].Detail != "driver=overlay" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleNetworkCreate_MissingName(t *testing.T) {
	s := newTestServer(t, nil)

	r := withUser(httptest.NewRequest(http.MethodPost, "/networks", strings.NewReader("")), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleNetworkCreate(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleNetworkCreate_DockerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/networks/create", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	form := url.Values{"name": {"backend"}}
	r := withUser(httptest.NewRequest(http.MethodPost, "/networks", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleNetworkCreate(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleNetworkDelete(t *testing.T) {
	docker := newFakeDocker(t, g1NetworksMux(nil))
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/networks/net1/delete", nil), testAdmin)
	r.SetPathValue("id", "net1")
	w := httptest.NewRecorder()
	s.handleNetworkDelete(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "network.delete" || entries[0].Target != "net1" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleNetworkDelete_DockerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/networks/net1", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/networks/net1/delete", nil), testAdmin)
	r.SetPathValue("id", "net1")
	w := httptest.NewRecorder()
	s.handleNetworkDelete(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}
