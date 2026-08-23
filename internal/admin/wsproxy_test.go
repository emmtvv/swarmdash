package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/swarm"
	"github.com/gorilla/websocket"
)

// TestHandleExecProxy_RejectsOverCapacity guards the concurrent-exec-session
// cap: with execSlots full, a new session must be turned away with 503
// before it ever tries to resolve the task or dial an agent, rather than an
// unbounded number of interactive shells being allowed to pile up.
func TestHandleExecProxy_RejectsOverCapacity(t *testing.T) {
	s := newTestServer(t, nil)
	s.execSlots = make(chan struct{}, 1)
	s.execSlots <- struct{}{} // fill the only slot

	r := httptest.NewRequest(http.MethodGet, "/ws/exec/task1", nil)
	r.SetPathValue("taskID", "task1")
	w := httptest.NewRecorder()

	s.handleExecProxy(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

func TestHandleExecProxy_AcquiresAndReleasesSlotOnFailure(t *testing.T) {
	// A docker mux with no /tasks/missing route, so taskContainer fails
	// cleanly with a 404 instead of panicking on a nil client.
	docker := newFakeDocker(t, http.NewServeMux())
	s := newTestServer(t, docker)
	s.execSlots = make(chan struct{}, 1)

	r := httptest.NewRequest(http.MethodGet, "/ws/exec/missing", nil)
	r.SetPathValue("taskID", "missing")
	w := httptest.NewRecorder()

	s.handleExecProxy(w, r)

	if w.Code == http.StatusServiceUnavailable {
		t.Fatalf("first call should not see the capacity error")
	}
	select {
	case s.execSlots <- struct{}{}:
	default:
		t.Error("slot was not released after the handler returned")
	}
}

// TestHandleExecProxy_AuditsOpenAndClose is the regression test for the
// audit-log gap called out in the security review: an interactive exec
// session was previously invisible in the audit trail entirely (only the
// mutating actions elsewhere in admin were recorded). Drives a real
// websocket round trip - fake agent, fake admin server, real client dial -
// so the open/close audit calls in handleExecProxy actually fire the way
// they would for a browser console session.
func TestHandleExecProxy_AuditsOpenAndClose(t *testing.T) {
	agentUpgrader := websocket.Upgrader{}
	agentMux := http.NewServeMux()
	agentMux.HandleFunc("/v1/containers/c1/exec", func(w http.ResponseWriter, r *http.Request) {
		conn, err := agentUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("agent upgrade: %v", err)
			return
		}
		defer conn.Close()
		// Block until the client disconnects, mirroring a real exec session
		// that stays open until the browser tab/console is closed.
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	agentAddr, agentPort := newFakeAgent(t, agentMux)
	node := swarm.Node{ID: "n1", Status: swarm.NodeStatus{Addr: agentAddr}}

	docker := newFakeDocker(t, g5FilesDockerMux(g5RunningTask(), node))
	s := newTestServer(t, docker)
	s.cfg.AgentPort = agentPort

	adminMux := http.NewServeMux()
	adminMux.HandleFunc("GET /ws/exec/{taskID}", func(w http.ResponseWriter, r *http.Request) {
		s.handleExecProxy(w, withUser(r, testAdmin))
	})
	adminServer := httptest.NewServer(adminMux)
	defer adminServer.Close()

	wsURL := "ws" + strings.TrimPrefix(adminServer.URL, "http") + "/ws/exec/t1?cmd=%2Fbin%2Fsh"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial exec websocket: %v", err)
	}
	conn.Close() // ends the session; handleExecProxy should log the close audit shortly after

	deadline := time.Now().Add(5 * time.Second)
	var entries []storeAuditEntrySnapshot
	for time.Now().Before(deadline) {
		got, err := s.store.ListAudit(0, 10)
		if err != nil {
			t.Fatalf("ListAudit: %v", err)
		}
		if len(got) >= 2 {
			for _, e := range got {
				entries = append(entries, storeAuditEntrySnapshot{Action: e.Action, Target: e.Target, Detail: e.Detail})
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 audit entries (open+close), got: %+v", entries)
	}
	want := []storeAuditEntrySnapshot{
		{Action: "container.exec.close", Target: "t1", Detail: "/bin/sh"},
		{Action: "container.exec.open", Target: "t1", Detail: "/bin/sh"},
	}
	if entries[0] != want[0] || entries[1] != want[1] {
		t.Fatalf("audit entries = %+v, want %+v (ListAudit returns newest first)", entries, want)
	}
}

// storeAuditEntrySnapshot pares a store.AuditEntry down to the fields
// TestHandleExecProxy_AuditsOpenAndClose asserts on, so the comparison
// above doesn't have to deal with timestamps/IPs.
type storeAuditEntrySnapshot struct {
	Action, Target, Detail string
}
