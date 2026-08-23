package admin

import (
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/swarm"
)

// g5StdFrame builds one docker multiplexed-stream frame (as stdcopy.StdCopy
// expects to demux): a stdout(1)/stderr(2) tag byte, three zero bytes, a
// big-endian uint32 payload length, then the payload itself.
func g5StdFrame(streamType byte, payload string) []byte {
	header := make([]byte, 8)
	header[0] = streamType
	binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
	return append(header, []byte(payload)...)
}

func TestHandleServiceLogsPage(t *testing.T) {
	s := newTestServer(t, nil)
	r := withUser(httptest.NewRequest(http.MethodGet, "/services/web/logs", nil), testAdmin)
	r.SetPathValue("name", "web")
	w := httptest.NewRecorder()
	s.handleServiceLogsPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "web") {
		t.Errorf("body missing service name, got: %s", w.Body.String())
	}
}

func TestHandleServiceLogsProxy_ServiceNotFound(t *testing.T) {
	docker := newFakeDocker(t, http.NewServeMux())
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/ws/services/missing/logs", nil), testAdmin)
	r.SetPathValue("name", "missing")
	w := httptest.NewRecorder()
	s.handleServiceLogsProxy(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleServiceLogsDownload(t *testing.T) {
	svc := swarm.Service{ID: "svc1", Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "web"}}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services/web", jsonHandler(svc))
	mux.HandleFunc("GET /services/svc1/logs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("tail") != "all" {
			t.Errorf("tail = %q, want all", r.URL.Query().Get("tail"))
		}
		_, _ = w.Write(g5StdFrame(1, "hello\n"))
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/services/web/logs/download", nil), testAdmin)
	r.SetPathValue("name", "web")
	w := httptest.NewRecorder()
	s.handleServiceLogsDownload(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if w.Body.String() != "hello\n" {
		t.Errorf("body = %q, want %q", w.Body.String(), "hello\n")
	}
	if !strings.Contains(w.Header().Get("Content-Disposition"), "web.log") {
		t.Errorf("Content-Disposition = %q, want web.log", w.Header().Get("Content-Disposition"))
	}
}

func TestHandleServiceLogsDownload_ServiceNotFound(t *testing.T) {
	docker := newFakeDocker(t, http.NewServeMux())
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/services/missing/logs/download", nil), testAdmin)
	r.SetPathValue("name", "missing")
	w := httptest.NewRecorder()
	s.handleServiceLogsDownload(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleServiceLogsDownload_LogsError(t *testing.T) {
	svc := swarm.Service{ID: "svc1", Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "web"}}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services/web", jsonHandler(svc))
	mux.HandleFunc("GET /services/svc1/logs", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/services/web/logs/download", nil), testAdmin)
	r.SetPathValue("name", "web")
	w := httptest.NewRecorder()
	s.handleServiceLogsDownload(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}
