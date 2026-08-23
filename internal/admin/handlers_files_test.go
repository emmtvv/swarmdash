package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/swarm"
)

// g5FilesDockerMux registers the swarm-manager endpoints handlers_files.go
// needs: task inspect (to resolve node+container) and node inspect (to
// resolve the agent address).
func g5FilesDockerMux(task swarm.Task, node swarm.Node) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tasks/t1", jsonHandler(task))
	mux.HandleFunc("GET /nodes/"+node.ID, jsonHandler(node))
	return mux
}

func g5RunningTask() swarm.Task {
	return swarm.Task{
		ID:     "t1",
		NodeID: "n1",
		Status: swarm.TaskStatus{State: swarm.TaskStateRunning, ContainerStatus: &swarm.ContainerStatus{ContainerID: "c1"}},
	}
}

func TestHandleLogsDownload(t *testing.T) {
	agentMux := http.NewServeMux()
	agentMux.HandleFunc("GET /v1/containers/c1/logs/download", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="c1.log"`)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("log line\n"))
	})
	agentAddr, agentPort := newFakeAgent(t, agentMux)
	node := swarm.Node{ID: "n1", Status: swarm.NodeStatus{Addr: agentAddr}}

	docker := newFakeDocker(t, g5FilesDockerMux(g5RunningTask(), node))
	s := newTestServer(t, docker)
	s.cfg.AgentPort = agentPort

	r := withUser(httptest.NewRequest(http.MethodGet, "/logs/t1/download", nil), testAdmin)
	r.SetPathValue("taskID", "t1")
	w := httptest.NewRecorder()
	s.handleLogsDownload(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if w.Body.String() != "log line\n" {
		t.Errorf("body = %q, want %q", w.Body.String(), "log line\n")
	}
	if !strings.Contains(w.Header().Get("Content-Disposition"), "c1.log") {
		t.Errorf("Content-Disposition = %q, want c1.log", w.Header().Get("Content-Disposition"))
	}
}

func TestHandleLogsDownload_TaskNotFound(t *testing.T) {
	docker := newFakeDocker(t, http.NewServeMux())
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/logs/missing/download", nil), testAdmin)
	r.SetPathValue("taskID", "missing")
	w := httptest.NewRecorder()
	s.handleLogsDownload(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleLogsDownload_NoRunningContainer(t *testing.T) {
	task := swarm.Task{ID: "t1", Status: swarm.TaskStatus{State: swarm.TaskStateShutdown}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tasks/t1", jsonHandler(task))
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/logs/t1/download", nil), testAdmin)
	r.SetPathValue("taskID", "t1")
	w := httptest.NewRecorder()
	s.handleLogsDownload(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleFilesList(t *testing.T) {
	agentMux := http.NewServeMux()
	agentMux.HandleFunc("GET /v1/containers/c1/files", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("path") != "/data" {
			t.Errorf("path = %q, want /data", r.URL.Query().Get("path"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"a.txt"}]`))
	})
	agentAddr, agentPort := newFakeAgent(t, agentMux)
	node := swarm.Node{ID: "n1", Status: swarm.NodeStatus{Addr: agentAddr}}

	docker := newFakeDocker(t, g5FilesDockerMux(g5RunningTask(), node))
	s := newTestServer(t, docker)
	s.cfg.AgentPort = agentPort

	r := withUser(httptest.NewRequest(http.MethodGet, "/files/t1?path=/data", nil), testAdmin)
	r.SetPathValue("taskID", "t1")
	w := httptest.NewRecorder()
	s.handleFilesList(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "a.txt") {
		t.Errorf("body missing file entry, got: %s", w.Body.String())
	}

	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "container.files.list" || entries[0].Target != "t1" || entries[0].Detail != "/data" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleFilesList_TaskNotFound(t *testing.T) {
	docker := newFakeDocker(t, http.NewServeMux())
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/files/missing", nil), testAdmin)
	r.SetPathValue("taskID", "missing")
	w := httptest.NewRecorder()
	s.handleFilesList(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleFileDownload(t *testing.T) {
	agentMux := http.NewServeMux()
	agentMux.HandleFunc("GET /v1/containers/c1/files/download", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("path") != "/data/a.txt" {
			t.Errorf("path = %q, want /data/a.txt", r.URL.Query().Get("path"))
		}
		w.Header().Set("Content-Disposition", `attachment; filename="a.txt"`)
		_, _ = w.Write([]byte("file contents"))
	})
	agentAddr, agentPort := newFakeAgent(t, agentMux)
	node := swarm.Node{ID: "n1", Status: swarm.NodeStatus{Addr: agentAddr}}

	docker := newFakeDocker(t, g5FilesDockerMux(g5RunningTask(), node))
	s := newTestServer(t, docker)
	s.cfg.AgentPort = agentPort

	r := withUser(httptest.NewRequest(http.MethodGet, "/files/t1/download?path=/data/a.txt", nil), testAdmin)
	r.SetPathValue("taskID", "t1")
	w := httptest.NewRecorder()
	s.handleFileDownload(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if w.Body.String() != "file contents" {
		t.Errorf("body = %q, want %q", w.Body.String(), "file contents")
	}

	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "container.files.download" || entries[0].Target != "t1" || entries[0].Detail != "/data/a.txt" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleFileDownload_MissingPath(t *testing.T) {
	docker := newFakeDocker(t, g5FilesDockerMux(g5RunningTask(), swarm.Node{ID: "n1"}))
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/files/t1/download", nil), testAdmin)
	r.SetPathValue("taskID", "t1")
	w := httptest.NewRecorder()
	s.handleFileDownload(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleFileDownload_TaskNotFound(t *testing.T) {
	docker := newFakeDocker(t, http.NewServeMux())
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/files/missing/download?path=/x", nil), testAdmin)
	r.SetPathValue("taskID", "missing")
	w := httptest.NewRecorder()
	s.handleFileDownload(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}
