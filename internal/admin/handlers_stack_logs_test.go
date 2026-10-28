package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/docker/docker/api/types/swarm"
)

func TestHandleStackLogsDownload_MergesServicesByTime(t *testing.T) {
	stack := map[string]string{stackLabel: "shop"}
	services := []swarm.Service{
		{ID: "s1", Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "shop_api", Labels: stack}}},
		{ID: "s2", Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "shop_db", Labels: stack}}},
		{ID: "s3", Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "other_web"}}},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services", jsonHandler(services))
	mux.HandleFunc("GET /services/s1/logs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("timestamps") != "1" {
			t.Errorf("timestamps = %q, want 1", r.URL.Query().Get("timestamps"))
		}
		_, _ = w.Write(g5StdFrame(1, "2026-10-28T10:00:01.000000000Z api started\n"))
		_, _ = w.Write(g5StdFrame(2, "2026-10-28T10:00:03.000000000Z api error\n"))
	})
	mux.HandleFunc("GET /services/s2/logs", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(g5StdFrame(1, "2026-10-28T10:00:02.000000000Z db ready\n"))
	})
	s := newTestServer(t, newFakeDocker(t, mux))

	r := withUser(httptest.NewRequest(http.MethodGet, "/stacks/shop/logs/download", nil), testViewer)
	r.SetPathValue("name", "shop")
	w := httptest.NewRecorder()
	s.handleStackLogsDownload(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", w.Code, w.Body.String())
	}
	want := "2026-10-28 10:00:01.000 api | api started\n" +
		"2026-10-28 10:00:02.000 db  | db ready\n" +
		"2026-10-28 10:00:03.000 api | api error\n"
	if w.Body.String() != want {
		t.Errorf("body =\n%s\nwant\n%s", w.Body.String(), want)
	}
}

func TestHandleStackLogsDownload_UnknownStack(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services", jsonHandler([]swarm.Service{}))
	s := newTestServer(t, newFakeDocker(t, mux))

	r := withUser(httptest.NewRequest(http.MethodGet, "/stacks/nope/logs/download", nil), testViewer)
	r.SetPathValue("name", "nope")
	w := httptest.NewRecorder()
	s.handleStackLogsDownload(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}
