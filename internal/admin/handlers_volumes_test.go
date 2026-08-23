package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/api/types/volume"
)

// g5VolumesDockerMux mirrors g5ImagesDockerMux: node list plus a node
// inspect per node, needed to resolve the agent address.
func g5VolumesDockerMux(nodes []swarm.Node) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /nodes", jsonHandler(nodes))
	for _, n := range nodes {
		mux.HandleFunc("GET /nodes/"+n.ID, jsonHandler(n))
	}
	return mux
}

func TestHandleVolumesPage(t *testing.T) {
	agentMux := http.NewServeMux()
	agentMux.HandleFunc("GET /v1/volumes", jsonHandler([]*volume.Volume{
		{Name: "zvol", Driver: "local"},
		{Name: "avol", Driver: "local"},
	}))
	agentAddr, agentPort := newFakeAgent(t, agentMux)

	nodes := []swarm.Node{{ID: "n1", Description: swarm.NodeDescription{Hostname: "node-a"}, Status: swarm.NodeStatus{Addr: agentAddr}}}
	docker := newFakeDocker(t, g5VolumesDockerMux(nodes))
	s := newTestServer(t, docker)
	s.cfg.AgentPort = agentPort

	r := withUser(httptest.NewRequest(http.MethodGet, "/volumes", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleVolumesPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "avol") || !strings.Contains(body, "zvol") {
		t.Errorf("body missing volume names, got: %s", body)
	}
	if strings.Index(body, "avol") > strings.Index(body, "zvol") {
		t.Errorf("volumes not sorted alphabetically: %s", body)
	}
}

func TestHandleVolumesPage_NoNodes(t *testing.T) {
	docker := newFakeDocker(t, g5VolumesDockerMux(nil))
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/volumes", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleVolumesPage(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleVolumeCreate(t *testing.T) {
	var gotBody string
	agentMux := http.NewServeMux()
	agentMux.HandleFunc("POST /v1/volumes", func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.WriteHeader(http.StatusOK)
	})
	agentAddr, agentPort := newFakeAgent(t, agentMux)

	nodes := []swarm.Node{{ID: "n1", Status: swarm.NodeStatus{Addr: agentAddr}}}
	docker := newFakeDocker(t, g5VolumesDockerMux(nodes))
	s := newTestServer(t, docker)
	s.cfg.AgentPort = agentPort

	form := url.Values{"node": {"n1"}, "name": {"myvol"}, "driver": {"local"}}
	r := withUser(httptest.NewRequest(http.MethodPost, "/volumes", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleVolumeCreate(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(gotBody, "myvol") {
		t.Errorf("agent request body = %q, want it to contain myvol", gotBody)
	}
	if loc := w.Header().Get("Location"); loc != "/volumes?node=n1" {
		t.Errorf("Location = %q, want /volumes?node=n1", loc)
	}
	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "volume.create" || entries[0].Target != "myvol" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleVolumeCreate_MissingName(t *testing.T) {
	docker := newFakeDocker(t, g5VolumesDockerMux(nil))
	s := newTestServer(t, docker)

	form := url.Values{"node": {"n1"}}
	r := withUser(httptest.NewRequest(http.MethodPost, "/volumes", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleVolumeCreate(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleVolumeCreate_AgentNonOK(t *testing.T) {
	agentMux := http.NewServeMux()
	agentMux.HandleFunc("POST /v1/volumes", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "conflict", http.StatusConflict)
	})
	agentAddr, agentPort := newFakeAgent(t, agentMux)

	nodes := []swarm.Node{{ID: "n1", Status: swarm.NodeStatus{Addr: agentAddr}}}
	docker := newFakeDocker(t, g5VolumesDockerMux(nodes))
	s := newTestServer(t, docker)
	s.cfg.AgentPort = agentPort

	form := url.Values{"node": {"n1"}, "name": {"myvol"}}
	r := withUser(httptest.NewRequest(http.MethodPost, "/volumes", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleVolumeCreate(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleVolumeDelete(t *testing.T) {
	deleteCalls := 0
	agentMux := http.NewServeMux()
	agentMux.HandleFunc("POST /v1/volumes/myvol/delete", func(w http.ResponseWriter, r *http.Request) {
		deleteCalls++
		w.WriteHeader(http.StatusOK)
	})
	agentAddr, agentPort := newFakeAgent(t, agentMux)

	nodes := []swarm.Node{{ID: "n1", Status: swarm.NodeStatus{Addr: agentAddr}}}
	docker := newFakeDocker(t, g5VolumesDockerMux(nodes))
	s := newTestServer(t, docker)
	s.cfg.AgentPort = agentPort

	r := withUser(httptest.NewRequest(http.MethodPost, "/volumes/myvol/delete?node=n1", nil), testAdmin)
	r.SetPathValue("name", "myvol")
	w := httptest.NewRecorder()
	s.handleVolumeDelete(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	if deleteCalls != 1 {
		t.Errorf("delete calls = %d, want 1", deleteCalls)
	}
	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "volume.delete" || entries[0].Target != "myvol" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleVolumeDelete_AgentUnreachable(t *testing.T) {
	docker := newFakeDocker(t, g5VolumesDockerMux(nil))
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/volumes/myvol/delete?node=missing", nil), testAdmin)
	r.SetPathValue("name", "myvol")
	w := httptest.NewRecorder()
	s.handleVolumeDelete(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}
