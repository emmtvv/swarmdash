package admin

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"swarmdash/internal/store"
	"swarmdash/internal/web"
)

func TestHandleHealth_Healthy(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/_ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("API-Version", "1.51")
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	s.handleHealth(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("status field = %v, want ok", resp["status"])
	}
	checks, _ := resp["checks"].(map[string]any)
	if checks["store"] != "ok" || checks["docker"] != "ok" {
		t.Errorf("unexpected checks: %+v", checks)
	}
}

func TestHandleHealth_DockerDown(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/_ping", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	s.handleHealth(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "unhealthy" {
		t.Errorf("status field = %v, want unhealthy", resp["status"])
	}
	checks, _ := resp["checks"].(map[string]any)
	if checks["store"] != "ok" {
		t.Errorf("expected store check ok, got: %+v", checks)
	}
	if checks["docker"] == "ok" || checks["docker"] == nil {
		t.Errorf("expected docker check to report an error, got: %+v", checks)
	}
}

func TestHandleHealth_StoreDown(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/_ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("API-Version", "1.51")
	})
	docker := newFakeDocker(t, mux)
	// Built directly (not via newTestStore/newTestServer) so the store can be
	// closed mid-test without double-closing it during t.Cleanup.
	st, err := store.OpenSQLite(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	s := &Server{
		cfg:      Config{AgentPort: "0"},
		docker:   docker,
		store:    st,
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		renderer: web.NewRenderer(),
	}

	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	s.handleHealth(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "unhealthy" {
		t.Errorf("status field = %v, want unhealthy", resp["status"])
	}
}
