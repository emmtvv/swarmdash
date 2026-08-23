package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/swarm"
)

// g5SwarmMux wires up the swarm-wide Docker Engine API endpoints
// handlers_swarm.go touches: swarm inspect/update/unlock-key, node/service/
// task list.
func g5SwarmMux(t *testing.T, sw swarm.Swarm, nodes []swarm.Node, services []swarm.Service, tasks []swarm.Task) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /swarm", jsonHandler(sw))
	mux.HandleFunc("POST /swarm/update", jsonHandler(swarm.Swarm{}))
	mux.HandleFunc("GET /swarm/unlockkey", jsonHandler(swarm.UnlockKeyResponse{UnlockKey: "SWMKEY-1-abc"}))
	mux.HandleFunc("GET /nodes", jsonHandler(nodes))
	mux.HandleFunc("GET /services", jsonHandler(services))
	mux.HandleFunc("GET /tasks", jsonHandler(tasks))
	return mux
}

// g5SwarmSpecForm returns a complete, valid form body for
// POST /settings/swarm/spec - every field handleSwarmUpdateSpec parses.
func g5SwarmSpecForm() url.Values {
	return url.Values{
		"name":                            {"prod"},
		"labels":                          {"env=prod"},
		"cert_expiry":                     {"2160h"},
		"dispatcher_heartbeat":            {"5s"},
		"raft_snapshot_interval":          {"10000"},
		"raft_log_entries_slow_followers": {"500"},
		"raft_election_tick":              {"3"},
		"raft_heartbeat_tick":             {"1"},
		"task_history_retention":          {"5"},
		"raft_keep_old_snapshots":         {"0"},
		"log_driver_name":                 {"json-file"},
		"log_driver_options":              {"max-size=10m"},
	}
}

func TestHandleSwarmPage(t *testing.T) {
	sw := swarm.Swarm{ClusterInfo: swarm.ClusterInfo{ID: "swarm1"}}
	nodes := []swarm.Node{
		{ID: "n1", Description: swarm.NodeDescription{Hostname: "mgr1"}, ManagerStatus: &swarm.ManagerStatus{Leader: true}, Status: swarm.NodeStatus{Addr: "10.0.0.1"}},
	}
	services := []swarm.Service{
		{Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "web"}, Mode: swarm.ServiceMode{Replicated: &swarm.ReplicatedService{}}}},
	}
	tasks := []swarm.Task{
		{Status: swarm.TaskStatus{State: swarm.TaskStateFailed}},
		{Status: swarm.TaskStatus{State: swarm.TaskStateRunning}},
	}
	docker := newFakeDocker(t, g5SwarmMux(t, sw, nodes, services, tasks))
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/settings/swarm", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleSwarmPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "swarm1") {
		t.Errorf("body missing swarm ID, got: %s", body)
	}
	if !strings.Contains(body, "10.0.0.1") {
		t.Errorf("body missing leader manager addr, got: %s", body)
	}
}

func TestHandleSwarmPage_InspectError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /swarm", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/settings/swarm", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleSwarmPage(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleSwarmUpdateSpec(t *testing.T) {
	sw := swarm.Swarm{ClusterInfo: swarm.ClusterInfo{ID: "swarm1", Meta: swarm.Meta{Version: swarm.Version{Index: 7}}}}
	var gotSpec swarm.Spec
	mux := http.NewServeMux()
	mux.HandleFunc("GET /swarm", jsonHandler(sw))
	mux.HandleFunc("POST /swarm/update", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotSpec)
		if got := r.URL.Query().Get("version"); got != "7" {
			t.Errorf("update version = %q, want 7", got)
		}
		jsonHandler(swarm.Swarm{})(w, r)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	form := g5SwarmSpecForm()
	r := withUser(httptest.NewRequest(http.MethodPost, "/settings/swarm/spec", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleSwarmUpdateSpec(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "flash=settings") {
		t.Errorf("Location = %q, want flash=settings", loc)
	}
	if gotSpec.Name != "prod" {
		t.Errorf("posted spec name = %q, want prod", gotSpec.Name)
	}
	if gotSpec.Labels["env"] != "prod" {
		t.Errorf("posted spec labels = %v, want env=prod", gotSpec.Labels)
	}

	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "swarm.update_spec" || !entries[0].Success {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleSwarmUpdateSpec_AutoLockEnabled(t *testing.T) {
	sw := swarm.Swarm{ClusterInfo: swarm.ClusterInfo{ID: "swarm1"}}
	docker := newFakeDocker(t, g5SwarmMux(t, sw, nil, nil, nil))
	s := newTestServer(t, docker)

	form := g5SwarmSpecForm()
	form.Set("autolock", "on")
	r := withUser(httptest.NewRequest(http.MethodPost, "/settings/swarm/spec", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleSwarmUpdateSpec(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "unlock_key=SWMKEY") {
		t.Errorf("Location = %q, want unlock_key", loc)
	}
}

func TestHandleSwarmUpdateSpec_InvalidCertExpiry(t *testing.T) {
	sw := swarm.Swarm{ClusterInfo: swarm.ClusterInfo{ID: "swarm1"}}
	docker := newFakeDocker(t, g5SwarmMux(t, sw, nil, nil, nil))
	s := newTestServer(t, docker)

	form := g5SwarmSpecForm()
	form.Set("cert_expiry", "not-a-duration")
	r := withUser(httptest.NewRequest(http.MethodPost, "/settings/swarm/spec", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleSwarmUpdateSpec(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleSwarmUpdateSpec_InvalidRaftValue(t *testing.T) {
	sw := swarm.Swarm{ClusterInfo: swarm.ClusterInfo{ID: "swarm1"}}
	docker := newFakeDocker(t, g5SwarmMux(t, sw, nil, nil, nil))
	s := newTestServer(t, docker)

	form := g5SwarmSpecForm()
	form.Set("raft_election_tick", "not-an-int")
	r := withUser(httptest.NewRequest(http.MethodPost, "/settings/swarm/spec", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleSwarmUpdateSpec(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleSwarmUpdateSpec_DockerError(t *testing.T) {
	sw := swarm.Swarm{ClusterInfo: swarm.ClusterInfo{ID: "swarm1"}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /swarm", jsonHandler(sw))
	mux.HandleFunc("POST /swarm/update", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	form := g5SwarmSpecForm()
	r := withUser(httptest.NewRequest(http.MethodPost, "/settings/swarm/spec", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleSwarmUpdateSpec(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleSwarmForceCertRotate(t *testing.T) {
	sw := swarm.Swarm{ClusterInfo: swarm.ClusterInfo{ID: "swarm1"}}
	var gotSpec swarm.Spec
	mux := http.NewServeMux()
	mux.HandleFunc("GET /swarm", jsonHandler(sw))
	mux.HandleFunc("POST /swarm/update", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotSpec)
		jsonHandler(swarm.Swarm{})(w, r)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/settings/swarm/rotate-ca", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleSwarmForceCertRotate(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	if gotSpec.CAConfig.ForceRotate != 1 {
		t.Errorf("ForceRotate = %d, want 1", gotSpec.CAConfig.ForceRotate)
	}
	entries, _ := s.store.ListAudit(0, 10)
	if len(entries) != 1 || entries[0].Action != "swarm.force_cert_rotate" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleSwarmForceCertRotate_DockerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /swarm", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/settings/swarm/rotate-ca", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleSwarmForceCertRotate(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleSwarmRebalance(t *testing.T) {
	replicated := swarm.Service{
		ID:   "s1",
		Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "web"}, Mode: swarm.ServiceMode{Replicated: &swarm.ReplicatedService{}}},
	}
	global := swarm.Service{
		ID:   "s2",
		Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "agent"}, Mode: swarm.ServiceMode{Global: &swarm.GlobalService{}}},
	}
	updateCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services", jsonHandler([]swarm.Service{replicated, global}))
	mux.HandleFunc("GET /services/s1", jsonHandler(replicated))
	mux.HandleFunc("POST /services/s1/update", func(w http.ResponseWriter, r *http.Request) {
		updateCalls++
		jsonHandler(swarm.ServiceUpdateResponse{})(w, r)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/settings/swarm/rebalance", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleSwarmRebalance(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	if updateCalls != 1 {
		t.Errorf("service update calls = %d, want 1 (global service must be skipped)", updateCalls)
	}
	entries, _ := s.store.ListAudit(0, 10)
	if len(entries) != 1 || entries[0].Action != "swarm.rebalance" || entries[0].Target != "web" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleSwarmRotateToken(t *testing.T) {
	sw := swarm.Swarm{ClusterInfo: swarm.ClusterInfo{ID: "swarm1"}}
	var gotFlags url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("GET /swarm", jsonHandler(sw))
	mux.HandleFunc("POST /swarm/update", func(w http.ResponseWriter, r *http.Request) {
		gotFlags = r.URL.Query()
		jsonHandler(swarm.Swarm{})(w, r)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/settings/swarm/rotate-token/worker", nil), testAdmin)
	r.SetPathValue("role", "worker")
	w := httptest.NewRecorder()
	s.handleSwarmRotateToken(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	if gotFlags.Get("rotateWorkerToken") != "true" {
		t.Errorf("rotateWorkerToken = %q, want true", gotFlags.Get("rotateWorkerToken"))
	}
	entries, _ := s.store.ListAudit(0, 10)
	if len(entries) != 1 || entries[0].Action != "swarm.rotate_token" || entries[0].Target != "worker" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleSwarmRotateToken_UnknownRole(t *testing.T) {
	sw := swarm.Swarm{ClusterInfo: swarm.ClusterInfo{ID: "swarm1"}}
	docker := newFakeDocker(t, g5SwarmMux(t, sw, nil, nil, nil))
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/settings/swarm/rotate-token/superuser", nil), testAdmin)
	r.SetPathValue("role", "superuser")
	w := httptest.NewRecorder()
	s.handleSwarmRotateToken(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleSwarmPruneFailedTasks(t *testing.T) {
	agentMux := http.NewServeMux()
	removeCalls := 0
	agentMux.HandleFunc("POST /v1/containers/c1/remove", func(w http.ResponseWriter, r *http.Request) {
		removeCalls++
		w.WriteHeader(http.StatusOK)
	})
	agentMux.HandleFunc("POST /v1/containers/c2/remove", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	agentAddr, agentPort := newFakeAgent(t, agentMux)

	tasks := []swarm.Task{
		{ID: "t1", NodeID: "n1", Status: swarm.TaskStatus{State: swarm.TaskStateFailed, ContainerStatus: &swarm.ContainerStatus{ContainerID: "c1"}}},
		{ID: "t2", NodeID: "n2", Status: swarm.TaskStatus{State: swarm.TaskStateRejected, ContainerStatus: &swarm.ContainerStatus{ContainerID: "c2"}}},
		{ID: "t3", Status: swarm.TaskStatus{State: swarm.TaskStateRunning}},
	}
	dockerMux := http.NewServeMux()
	dockerMux.HandleFunc("GET /tasks", jsonHandler(tasks))
	dockerMux.HandleFunc("GET /nodes/n1", jsonHandler(swarm.Node{ID: "n1", Status: swarm.NodeStatus{Addr: agentAddr}}))
	dockerMux.HandleFunc("GET /nodes/n2", jsonHandler(swarm.Node{ID: "n2", Status: swarm.NodeStatus{Addr: agentAddr}}))
	docker := newFakeDocker(t, dockerMux)
	s := newTestServer(t, docker)
	s.cfg.AgentPort = agentPort

	r := withUser(httptest.NewRequest(http.MethodPost, "/settings/swarm/prune-failed-tasks", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleSwarmPruneFailedTasks(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	if removeCalls != 1 {
		t.Errorf("remove calls = %d, want 1", removeCalls)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "removed=1") || !strings.Contains(loc, "failed=1") {
		t.Errorf("Location = %q, want removed=1&failed=1", loc)
	}
	entries, _ := s.store.ListAudit(0, 10)
	if len(entries) != 1 || entries[0].Action != "swarm.prune_failed_tasks" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleSwarmPruneFailedTasks_ListError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tasks", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/settings/swarm/prune-failed-tasks", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleSwarmPruneFailedTasks(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleSwarmPruneResources(t *testing.T) {
	var gotQuery url.Values
	agentMux := http.NewServeMux()
	agentMux.HandleFunc("POST /v1/system/prune", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		jsonHandler(systemPruneReport{
			ContainersDeleted: []string{"c1"},
			ImagesDeleted:     2,
			SpaceReclaimed:    1024,
		})(w, r)
	})
	agentAddr, agentPort := newFakeAgent(t, agentMux)

	nodes := []swarm.Node{{ID: "n1", Description: swarm.NodeDescription{Hostname: "node-a"}, Status: swarm.NodeStatus{Addr: agentAddr}}}
	dockerMux := http.NewServeMux()
	dockerMux.HandleFunc("GET /nodes", jsonHandler(nodes))
	dockerMux.HandleFunc("GET /nodes/n1", jsonHandler(nodes[0]))
	docker := newFakeDocker(t, dockerMux)
	s := newTestServer(t, docker)
	s.cfg.AgentPort = agentPort

	form := url.Values{"all": {"on"}, "volumes": {"on"}}
	r := withUser(httptest.NewRequest(http.MethodPost, "/settings/swarm/prune-resources", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleSwarmPruneResources(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	if gotQuery.Get("all") != "true" || gotQuery.Get("volumes") != "true" {
		t.Errorf("agent query = %v, want all=true&volumes=true", gotQuery)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "containers=1") || !strings.Contains(loc, "images=2") || !strings.Contains(loc, "reclaimed=1024") {
		t.Errorf("Location = %q, missing expected counts", loc)
	}
	entries, _ := s.store.ListAudit(0, 10)
	if len(entries) != 1 || entries[0].Action != "swarm.prune_resources" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleSwarmPruneResources_ListNodesError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /nodes", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodPost, "/settings/swarm/prune-resources", strings.NewReader("")), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleSwarmPruneResources(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestIsFailedTaskState(t *testing.T) {
	tests := []struct {
		state swarm.TaskState
		want  bool
	}{
		{swarm.TaskStateFailed, true},
		{swarm.TaskStateRejected, true},
		{swarm.TaskStateRunning, false},
		{swarm.TaskStateShutdown, false},
		{swarm.TaskStateComplete, false},
	}
	for _, tt := range tests {
		if got := isFailedTaskState(tt.state); got != tt.want {
			t.Errorf("isFailedTaskState(%v) = %v, want %v", tt.state, got, tt.want)
		}
	}
}

func TestParseSwarmFlash(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/settings/swarm?flash=resources&removed=3&failed=1&reclaimed=2048&unlock_key=KEY1", nil)
	got := parseSwarmFlash(r)
	if got == nil {
		t.Fatal("parseSwarmFlash() = nil, want non-nil")
	}
	if got.Kind != "resources" || got.Removed != 3 || got.Failed != 1 || got.Reclaimed != 2048 || got.UnlockKey != "KEY1" {
		t.Errorf("parseSwarmFlash() = %+v, unexpected", got)
	}

	if got := parseSwarmFlash(httptest.NewRequest(http.MethodGet, "/settings/swarm", nil)); got != nil {
		t.Errorf("parseSwarmFlash() with no flash = %+v, want nil", got)
	}
}

func TestSwarmEditorForm(t *testing.T) {
	limit := int64(7)
	keepOld := uint64(3)
	spec := swarm.Spec{
		Annotations:      swarm.Annotations{Name: "prod", Labels: map[string]string{"env": "prod"}},
		EncryptionConfig: swarm.EncryptionConfig{AutoLockManagers: true},
		CAConfig:         swarm.CAConfig{NodeCertExpiry: 2160 * time.Hour},
		Orchestration:    swarm.OrchestrationConfig{TaskHistoryRetentionLimit: &limit},
		Raft: swarm.RaftConfig{
			SnapshotInterval:           10000,
			KeepOldSnapshots:           &keepOld,
			LogEntriesForSlowFollowers: 500,
			ElectionTick:               3,
			HeartbeatTick:              1,
		},
		Dispatcher:   swarm.DispatcherConfig{HeartbeatPeriod: 5 * time.Second},
		TaskDefaults: swarm.TaskDefaults{LogDriver: &swarm.Driver{Name: "json-file", Options: map[string]string{"max-size": "10m"}}},
	}
	got := swarmEditorForm(spec)
	if got.Name != "prod" || !got.AutoLock || got.TaskHistoryRetentionLimit != "7" || got.RaftKeepOldSnapshots != "3" || got.LogDriverName != "json-file" {
		t.Errorf("swarmEditorForm() = %+v, unexpected", got)
	}
	if got.RaftElectionTick != "3" || got.RaftHeartbeatTick != "1" {
		t.Errorf("swarmEditorForm() raft ticks = %+v", got)
	}
	if !strings.Contains(got.Labels, "env=prod") {
		t.Errorf("swarmEditorForm() labels = %q", got.Labels)
	}
}

func TestSwarmEditorForm_NilOptionals(t *testing.T) {
	got := swarmEditorForm(swarm.Spec{})
	if got.TaskHistoryRetentionLimit != "" || got.RaftKeepOldSnapshots != "" || got.LogDriverName != "" {
		t.Errorf("swarmEditorForm() with nil optionals = %+v, want blank strings", got)
	}
}
