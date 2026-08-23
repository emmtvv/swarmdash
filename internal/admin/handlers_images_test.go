package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/swarm"
)

// g5ImagesDockerMux registers the swarm-manager endpoints
// handlers_images.go needs to resolve nodes and dial their agent: node list
// plus a node inspect per node (nodeAgentAddr -> NodeInspectWithRaw).
func g5ImagesDockerMux(nodes []swarm.Node) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /nodes", jsonHandler(nodes))
	for _, n := range nodes {
		mux.HandleFunc("GET /nodes/"+n.ID, jsonHandler(n))
	}
	return mux
}

func TestHandleImagesPage(t *testing.T) {
	agentMux := http.NewServeMux()
	agentMux.HandleFunc("GET /v1/images", jsonHandler([]image.Summary{
		{ID: "sha256:aaa", RepoTags: []string{"nginx:latest"}, Size: 100},
		{ID: "sha256:bbb", RepoTags: []string{"redis:latest"}, Size: 200},
	}))
	agentAddr, agentPort := newFakeAgent(t, agentMux)

	nodes := []swarm.Node{{ID: "n1", Description: swarm.NodeDescription{Hostname: "node-a"}, Status: swarm.NodeStatus{Addr: agentAddr}}}
	docker := newFakeDocker(t, g5ImagesDockerMux(nodes))
	s := newTestServer(t, docker)
	s.cfg.AgentPort = agentPort

	r := withUser(httptest.NewRequest(http.MethodGet, "/images", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleImagesPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "redis:latest") || !strings.Contains(body, "nginx:latest") {
		t.Errorf("body missing image tags, got: %s", body)
	}
	// sorted by size descending: redis (200) before nginx (100).
	if strings.Index(body, "redis:latest") > strings.Index(body, "nginx:latest") {
		t.Errorf("images not sorted by size descending: %s", body)
	}
}

func TestHandleImagesPage_NoNodes(t *testing.T) {
	docker := newFakeDocker(t, g5ImagesDockerMux(nil))
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/images", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleImagesPage(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleImagesPage_ListNodesError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /nodes", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/images", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleImagesPage(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleImagesPage_AgentUnreachable(t *testing.T) {
	// No /nodes/{id} handler registered, so nodeAgentAddr's
	// NodeInspectWithRaw call 404s and agentGet surfaces that as an error.
	nodes := []swarm.Node{{ID: "n1", Description: swarm.NodeDescription{Hostname: "node-a"}}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /nodes", jsonHandler(nodes))
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/images", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleImagesPage(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleImagesPage_AgentNonOKStatus(t *testing.T) {
	agentMux := http.NewServeMux()
	agentMux.HandleFunc("GET /v1/images", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "agent down", http.StatusInternalServerError)
	})
	agentAddr, agentPort := newFakeAgent(t, agentMux)

	nodes := []swarm.Node{{ID: "n1", Description: swarm.NodeDescription{Hostname: "node-a"}, Status: swarm.NodeStatus{Addr: agentAddr}}}
	docker := newFakeDocker(t, g5ImagesDockerMux(nodes))
	s := newTestServer(t, docker)
	s.cfg.AgentPort = agentPort

	r := withUser(httptest.NewRequest(http.MethodGet, "/images", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleImagesPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (agent errors render an empty list, not a page error); body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "No images on this node") {
		t.Errorf("body missing empty-state message, got: %s", w.Body.String())
	}
}

func TestHandleImagesPrune(t *testing.T) {
	var gotQuery url.Values
	agentMux := http.NewServeMux()
	agentMux.HandleFunc("POST /v1/images/prune", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.WriteHeader(http.StatusOK)
	})
	agentAddr, agentPort := newFakeAgent(t, agentMux)

	nodes := []swarm.Node{{ID: "n1", Status: swarm.NodeStatus{Addr: agentAddr}}}
	docker := newFakeDocker(t, g5ImagesDockerMux(nodes))
	s := newTestServer(t, docker)
	s.cfg.AgentPort = agentPort

	form := url.Values{"node": {"n1"}, "all": {"on"}}
	r := withUser(httptest.NewRequest(http.MethodPost, "/images/prune", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleImagesPrune(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	if gotQuery.Get("all") != "true" {
		t.Errorf("agent query all = %q, want true", gotQuery.Get("all"))
	}
	if loc := w.Header().Get("Location"); loc != "/images?node=n1" {
		t.Errorf("Location = %q, want /images?node=n1", loc)
	}
	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "images.prune" || entries[0].Target != "n1" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleImagesPrune_AgentUnreachable(t *testing.T) {
	docker := newFakeDocker(t, g5ImagesDockerMux(nil))
	s := newTestServer(t, docker)

	form := url.Values{"node": {"missing-node"}}
	r := withUser(httptest.NewRequest(http.MethodPost, "/images/prune", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleImagesPrune(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}
