package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/swarm"
)

// g3NodeMux serves GET /nodes/{id} (returning node) and POST
// /nodes/{id}/update (returning updateStatus, http.StatusOK meaning success)
// - the two Docker Engine API calls setNodeRole/handleNodeAvailability/
// handleNodeLabel* make against a single node.
func g3NodeMux(node swarm.Node, updateStatus int) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /nodes/{id}", jsonHandler(node))
	mux.HandleFunc("POST /nodes/{id}/update", func(w http.ResponseWriter, r *http.Request) {
		if updateStatus != http.StatusOK {
			http.Error(w, "boom", updateStatus)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func g3NodeNotFoundMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /nodes/{id}", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such node", http.StatusNotFound)
	})
	return mux
}

func g3FormRequest(method, target string, form url.Values) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func TestHandleNodesPage(t *testing.T) {
	mux := http.NewServeMux()
	node := swarm.Node{ID: "n1", Description: swarm.NodeDescription{Hostname: "node-a"}}
	mux.HandleFunc("/nodes", jsonHandler([]swarm.Node{node}))
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/nodes", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleNodesPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "node-a") {
		t.Errorf("body missing node hostname, got: %s", w.Body.String())
	}
}

func TestHandleNodesPage_DockerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/nodes", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/nodes", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleNodesPage(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleNodesStatsJSON(t *testing.T) {
	agentMux := http.NewServeMux()
	agentMux.HandleFunc("/v1/stats/summary", jsonHandler(map[string]any{
		"mem_used_bytes":   int64(1 << 30),
		"cpu_used_cores":   1.5,
		"disk_total_bytes": int64(100 << 30),
		"disk_used_bytes":  int64(20 << 30),
	}))
	agentAddr, agentPort := newFakeAgent(t, agentMux)

	node := swarm.Node{
		ID:          "n1",
		Description: swarm.NodeDescription{Hostname: "node-a", Resources: swarm.Resources{NanoCPUs: 4_000_000_000, MemoryBytes: 8 << 30}},
		Status:      swarm.NodeStatus{State: swarm.NodeStateReady, Addr: agentAddr},
	}
	dockerMux := http.NewServeMux()
	dockerMux.HandleFunc("GET /nodes", jsonHandler([]swarm.Node{node}))
	// nodeStats resolves each node's agent address via NodeInspectWithRaw
	// (see nodeAgentAddr in agentclient.go), so /nodes/{id} needs to be
	// served too, not just the /nodes list.
	dockerMux.HandleFunc("GET /nodes/{id}", jsonHandler(node))
	docker := newFakeDocker(t, dockerMux)
	s := newTestServer(t, docker)
	s.cfg.AgentPort = agentPort

	r := httptest.NewRequest(http.MethodGet, "/nodes/stats", nil)
	w := httptest.NewRecorder()
	s.handleNodesStatsJSON(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var out map[string]nodeStatsJSON
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	st, ok := out["n1"]
	if !ok {
		t.Fatalf("missing stats for n1: %v", out)
	}
	if !st.OK {
		t.Errorf("stats not OK: %+v", st)
	}
	if st.MemUsedBytes != 1<<30 {
		t.Errorf("MemUsedBytes = %d, want %d", st.MemUsedBytes, int64(1<<30))
	}
}

func TestHandleNodeStatsJSON(t *testing.T) {
	agentMux := http.NewServeMux()
	agentMux.HandleFunc("/v1/stats/summary", jsonHandler(map[string]any{
		"mem_used_bytes": int64(512 << 20),
		"cpu_used_cores": 0.5,
	}))
	agentAddr, agentPort := newFakeAgent(t, agentMux)

	node := swarm.Node{
		ID:          "n1",
		Description: swarm.NodeDescription{Hostname: "node-a", Resources: swarm.Resources{NanoCPUs: 2_000_000_000, MemoryBytes: 4 << 30}},
		Status:      swarm.NodeStatus{State: swarm.NodeStateReady, Addr: agentAddr},
	}
	dockerMux := http.NewServeMux()
	dockerMux.HandleFunc("GET /nodes/{id}", jsonHandler(node))
	docker := newFakeDocker(t, dockerMux)
	s := newTestServer(t, docker)
	s.cfg.AgentPort = agentPort

	r := httptest.NewRequest(http.MethodGet, "/nodes/n1/stats", nil)
	r.SetPathValue("id", "n1")
	w := httptest.NewRecorder()
	s.handleNodeStatsJSON(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var out nodeStatsJSON
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !out.OK || out.MemUsedBytes != 512<<20 {
		t.Errorf("unexpected stats: %+v", out)
	}
}

func TestHandleNodeStatsJSON_NotFound(t *testing.T) {
	docker := newFakeDocker(t, g3NodeNotFoundMux())
	s := newTestServer(t, docker)

	r := httptest.NewRequest(http.MethodGet, "/nodes/missing/stats", nil)
	r.SetPathValue("id", "missing")
	w := httptest.NewRecorder()
	s.handleNodeStatsJSON(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleNodeAvailability(t *testing.T) {
	node := swarm.Node{
		ID:          "n1",
		Meta:        swarm.Meta{Version: swarm.Version{Index: 5}},
		Description: swarm.NodeDescription{Hostname: "node-a"},
		Spec:        swarm.NodeSpec{Role: swarm.NodeRoleWorker, Availability: swarm.NodeAvailabilityActive},
	}
	docker := newFakeDocker(t, g3NodeMux(node, http.StatusOK))
	s := newTestServer(t, docker)

	r := g3FormRequest(http.MethodPost, "/nodes/n1/availability", url.Values{"availability": {"drain"}})
	r.SetPathValue("id", "n1")
	r = withUser(r, testAdmin)
	w := httptest.NewRecorder()
	s.handleNodeAvailability(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "/nodes" {
		t.Errorf("Location = %q, want /nodes", loc)
	}

	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Action == "node.availability" && e.Target == "node-a" && e.Detail == "drain" {
			found = true
		}
	}
	if !found {
		t.Errorf("no audit entry for node.availability, got: %+v", entries)
	}
}

func TestHandleNodeAvailability_InvalidValue(t *testing.T) {
	docker := newFakeDocker(t, http.NewServeMux())
	s := newTestServer(t, docker)

	r := g3FormRequest(http.MethodPost, "/nodes/n1/availability", url.Values{"availability": {"bogus"}})
	r.SetPathValue("id", "n1")
	r = withUser(r, testAdmin)
	w := httptest.NewRecorder()
	s.handleNodeAvailability(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleNodeAvailability_BadForm(t *testing.T) {
	docker := newFakeDocker(t, http.NewServeMux())
	s := newTestServer(t, docker)

	r := httptest.NewRequest(http.MethodPost, "/nodes/n1/availability", strings.NewReader("a=%zz"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("id", "n1")
	r = withUser(r, testAdmin)
	w := httptest.NewRecorder()
	s.handleNodeAvailability(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleNodeAvailability_NodeNotFound(t *testing.T) {
	docker := newFakeDocker(t, g3NodeNotFoundMux())
	s := newTestServer(t, docker)

	r := g3FormRequest(http.MethodPost, "/nodes/n1/availability", url.Values{"availability": {"drain"}})
	r.SetPathValue("id", "n1")
	r = withUser(r, testAdmin)
	w := httptest.NewRecorder()
	s.handleNodeAvailability(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleNodeAvailability_UpdateError(t *testing.T) {
	node := swarm.Node{ID: "n1", Meta: swarm.Meta{Version: swarm.Version{Index: 1}}, Description: swarm.NodeDescription{Hostname: "node-a"}}
	docker := newFakeDocker(t, g3NodeMux(node, http.StatusInternalServerError))
	s := newTestServer(t, docker)

	r := g3FormRequest(http.MethodPost, "/nodes/n1/availability", url.Values{"availability": {"drain"}})
	r.SetPathValue("id", "n1")
	r = withUser(r, testAdmin)
	w := httptest.NewRecorder()
	s.handleNodeAvailability(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleNodePromote(t *testing.T) {
	node := swarm.Node{ID: "n1", Meta: swarm.Meta{Version: swarm.Version{Index: 1}}, Description: swarm.NodeDescription{Hostname: "node-a"}, Spec: swarm.NodeSpec{Role: swarm.NodeRoleWorker}}
	docker := newFakeDocker(t, g3NodeMux(node, http.StatusOK))
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/nodes/n1/promote", nil), testAdmin)
	r.SetPathValue("id", "n1")
	w := httptest.NewRecorder()
	s.handleNodePromote(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	entries, _ := s.store.ListAudit(0, 10)
	found := false
	for _, e := range entries {
		if e.Action == "node.role" && e.Detail == "manager" {
			found = true
		}
	}
	if !found {
		t.Errorf("no audit entry for node.role=manager, got: %+v", entries)
	}
}

func TestHandleNodeDemote(t *testing.T) {
	node := swarm.Node{ID: "n1", Meta: swarm.Meta{Version: swarm.Version{Index: 1}}, Description: swarm.NodeDescription{Hostname: "node-a"}, Spec: swarm.NodeSpec{Role: swarm.NodeRoleManager}}
	docker := newFakeDocker(t, g3NodeMux(node, http.StatusOK))
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/nodes/n1/demote", nil), testAdmin)
	r.SetPathValue("id", "n1")
	w := httptest.NewRecorder()
	s.handleNodeDemote(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	entries, _ := s.store.ListAudit(0, 10)
	found := false
	for _, e := range entries {
		if e.Action == "node.role" && e.Detail == "worker" {
			found = true
		}
	}
	if !found {
		t.Errorf("no audit entry for node.role=worker, got: %+v", entries)
	}
}

func TestHandleNodePromote_NotFound(t *testing.T) {
	docker := newFakeDocker(t, g3NodeNotFoundMux())
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/nodes/n1/promote", nil), testAdmin)
	r.SetPathValue("id", "n1")
	w := httptest.NewRecorder()
	s.handleNodePromote(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleNodeDetail(t *testing.T) {
	node := swarm.Node{ID: "n1", Description: swarm.NodeDescription{Hostname: "node-a"}}
	docker := newFakeDocker(t, g3NodeMux(node, http.StatusOK))
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/nodes/n1", nil), testAdmin)
	r.SetPathValue("id", "n1")
	w := httptest.NewRecorder()
	s.handleNodeDetail(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "node-a") {
		t.Errorf("body missing node hostname, got: %s", w.Body.String())
	}
}

func TestHandleNodeDetail_NotFound(t *testing.T) {
	docker := newFakeDocker(t, g3NodeNotFoundMux())
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/nodes/missing", nil), testAdmin)
	r.SetPathValue("id", "missing")
	w := httptest.NewRecorder()
	s.handleNodeDetail(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleNodeLabelAdd(t *testing.T) {
	node := swarm.Node{ID: "n1", Meta: swarm.Meta{Version: swarm.Version{Index: 1}}, Description: swarm.NodeDescription{Hostname: "node-a"}}
	docker := newFakeDocker(t, g3NodeMux(node, http.StatusOK))
	s := newTestServer(t, docker)

	r := g3FormRequest(http.MethodPost, "/nodes/n1/labels", url.Values{"key": {"zone"}, "value": {"us-east-1a"}})
	r.SetPathValue("id", "n1")
	r = withUser(r, testAdmin)
	w := httptest.NewRecorder()
	s.handleNodeLabelAdd(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "/nodes/n1" {
		t.Errorf("Location = %q, want /nodes/n1", loc)
	}
	entries, _ := s.store.ListAudit(0, 10)
	found := false
	for _, e := range entries {
		if e.Action == "node.label.add" && e.Detail == "zone=us-east-1a" {
			found = true
		}
	}
	if !found {
		t.Errorf("no audit entry for node.label.add, got: %+v", entries)
	}
}

func TestHandleNodeLabelAdd_MissingKey(t *testing.T) {
	docker := newFakeDocker(t, http.NewServeMux())
	s := newTestServer(t, docker)

	r := g3FormRequest(http.MethodPost, "/nodes/n1/labels", url.Values{"value": {"us-east-1a"}})
	r.SetPathValue("id", "n1")
	r = withUser(r, testAdmin)
	w := httptest.NewRecorder()
	s.handleNodeLabelAdd(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleNodeLabelAdd_NotFound(t *testing.T) {
	docker := newFakeDocker(t, g3NodeNotFoundMux())
	s := newTestServer(t, docker)

	r := g3FormRequest(http.MethodPost, "/nodes/n1/labels", url.Values{"key": {"zone"}, "value": {"a"}})
	r.SetPathValue("id", "n1")
	r = withUser(r, testAdmin)
	w := httptest.NewRecorder()
	s.handleNodeLabelAdd(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleNodeLabelDelete(t *testing.T) {
	node := swarm.Node{
		ID: "n1", Meta: swarm.Meta{Version: swarm.Version{Index: 1}},
		Description: swarm.NodeDescription{Hostname: "node-a"},
		Spec:        swarm.NodeSpec{Annotations: swarm.Annotations{Labels: map[string]string{"zone": "a"}}},
	}
	docker := newFakeDocker(t, g3NodeMux(node, http.StatusOK))
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/nodes/n1/labels/zone/delete", nil), testAdmin)
	r.SetPathValue("id", "n1")
	r.SetPathValue("key", "zone")
	w := httptest.NewRecorder()
	s.handleNodeLabelDelete(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	entries, _ := s.store.ListAudit(0, 10)
	found := false
	for _, e := range entries {
		if e.Action == "node.label.delete" && e.Detail == "zone" {
			found = true
		}
	}
	if !found {
		t.Errorf("no audit entry for node.label.delete, got: %+v", entries)
	}
}

func TestHandleNodeLabelDelete_NotFound(t *testing.T) {
	docker := newFakeDocker(t, g3NodeNotFoundMux())
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/nodes/missing/labels/zone/delete", nil), testAdmin)
	r.SetPathValue("id", "missing")
	r.SetPathValue("key", "zone")
	w := httptest.NewRecorder()
	s.handleNodeLabelDelete(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}
