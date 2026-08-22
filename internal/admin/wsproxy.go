package admin

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/gorilla/websocket"
)

// browserUpgrader upgrades the browser-facing side of a proxied websocket.
// Origin is checked against Host to guard against cross-site websocket
// hijacking, since these endpoints act on behalf of the logged-in session.
var browserUpgrader = websocket.Upgrader{
	ReadBufferSize:  32 * 1024,
	WriteBufferSize: 32 * 1024,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		u, err := url.Parse(origin)
		return err == nil && u.Host == r.Host
	},
}

func (s *Server) handleExecPage(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskID")
	s.render(w, r, "exec.html", map[string]any{
		"TaskID": taskID,
		"User":   userFromContext(r),
	})
}

// handleTaskLogsPage renders a plain, non-interactive log viewer for a
// single task - for looking at what a container printed without opening a
// full exec/console session (and without the tty/shell dependency that
// requires).
func (s *Server) handleTaskLogsPage(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskID")
	s.render(w, r, "task_logs.html", map[string]any{
		"TaskID": taskID,
		"User":   userFromContext(r),
	})
}

func (s *Server) handleExecProxy(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskID")
	nodeID, containerID, err := s.taskContainer(r.Context(), taskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	q := r.URL.Query()
	shell := q.Get("cmd")
	if shell == "" {
		shell = "/bin/sh"
	}
	path := fmt.Sprintf("/v1/containers/%s/exec?cmd=%s&cols=%s&rows=%s",
		containerID, url.QueryEscape(shell), q.Get("cols"), q.Get("rows"))

	s.proxyWS(w, r, nodeID, path)
}

func (s *Server) handleLogsProxy(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskID")
	nodeID, containerID, err := s.taskContainer(r.Context(), taskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	tail := q.Get("tail")
	if tail == "" {
		tail = "200"
	}
	path := fmt.Sprintf("/v1/containers/%s/logs?tail=%s&follow=%s", containerID, url.QueryEscape(tail), q.Get("follow"))
	s.proxyWS(w, r, nodeID, path)
}

func (s *Server) handleStatsProxy(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskID")
	nodeID, containerID, err := s.taskContainer(r.Context(), taskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	path := fmt.Sprintf("/v1/containers/%s/stats", containerID)
	s.proxyWS(w, r, nodeID, path)
}

// proxyWS upgrades the incoming browser connection, dials the same path on
// the target node's agent, and relays messages in both directions until
// either side closes.
func (s *Server) proxyWS(w http.ResponseWriter, r *http.Request, nodeID, agentPath string) {
	upstream, err := s.dialAgentWS(r.Context(), nodeID, agentPath)
	if err != nil {
		http.Error(w, "agent connection failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer upstream.Close()

	client, err := browserUpgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Warn("browser ws upgrade failed", "err", err)
		return
	}
	defer client.Close()

	errc := make(chan error, 2)
	go relayWS(client, upstream, errc)
	go relayWS(upstream, client, errc)
	<-errc
}

// wsTextWriter adapts an io.Writer to forward each Write as a text
// websocket message; used as the stdout/stderr sink for stdcopy.StdCopy
// when demuxing a docker log stream straight to the browser.
type wsTextWriter struct {
	conn *websocket.Conn
}

func (w *wsTextWriter) Write(p []byte) (int, error) {
	if err := w.conn.WriteMessage(websocket.TextMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func relayWS(dst, src *websocket.Conn, errc chan<- error) {
	for {
		mt, data, err := src.ReadMessage()
		if err != nil {
			errc <- err
			return
		}
		if err := dst.WriteMessage(mt, data); err != nil {
			errc <- err
			return
		}
	}
}
