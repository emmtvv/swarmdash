package admin

import (
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

// maxConcurrentExecSessions caps how many interactive /ws/exec sessions can
// be open at once (see Server.execSlots). Unlike logs/stats, an exec
// session runs arbitrary commands for as long as a browser tab stays open,
// so with no cap a handful of abandoned or malicious sessions can pin an
// unbounded number of agent-side shells and admin-side goroutines.
const maxConcurrentExecSessions = 50

const (
	wsPingInterval  = 30 * time.Second
	wsPongWait      = 60 * time.Second
	wsPingWriteWait = 10 * time.Second
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
	select {
	case s.execSlots <- struct{}{}:
		defer func() { <-s.execSlots }()
	default:
		http.Error(w, "too many concurrent exec sessions - close an existing console and try again", http.StatusServiceUnavailable)
		return
	}

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

	// Without this, a session that goes idle (a shell sitting at a prompt,
	// a log tail with nothing new to print) never has its ReadMessage
	// calls return, so a dead peer or an intermediate proxy/load balancer
	// that silently drops idle connections leaves both relay goroutines
	// blocked forever instead of the session getting torn down.
	done := make(chan struct{})
	defer close(done)
	go keepAlive(client, done)
	go keepAlive(upstream, done)

	errc := make(chan error, 2)
	go relayWS(client, upstream, errc)
	go relayWS(upstream, client, errc)
	<-errc
}

// keepAlive installs a read deadline on conn that's pushed out by every
// pong (refreshed in-line inside ReadMessage on whichever goroutine is
// already reading conn, so this touches no shared state from a second
// goroutine) and periodically sends a ping to elicit one. Pings are sent
// via WriteControl, which - unlike WriteMessage - gorilla/websocket
// documents as safe to call concurrently with the relay goroutine's own
// WriteMessage calls on the same conn.
func keepAlive(conn *websocket.Conn, done <-chan struct{}) {
	_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsPongWait))
	})

	ticker := time.NewTicker(wsPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsPingWriteWait)); err != nil {
				return
			}
		}
	}
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
