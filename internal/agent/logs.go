package agent

import (
	"net/http"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/gorilla/websocket"
)

// handleLogs streams a container's stdout/stderr to the caller over a
// websocket, demultiplexing Docker's stdcopy framing along the way. Query
// params: follow (default true), tail (default "200").
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tail := r.URL.Query().Get("tail")
	if tail == "" {
		tail = "200"
	}
	follow := r.URL.Query().Get("follow") != "false"

	rc, err := s.docker.ContainerLogs(r.Context(), id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Tail:       tail,
		Timestamps: false,
	})
	if err != nil {
		http.Error(w, "logs: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer rc.Close()

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Warn("ws upgrade failed", "err", err)
		return
	}
	defer conn.Close()

	pw := &wsWriter{conn: conn}
	_, _ = stdcopy.StdCopy(pw, pw, rc)
}

// handleLogsDownload returns a container's logs as a plain HTTP response
// (never follows) so they can be saved as a file - the websocket handler
// above is for live tailing in the browser, this one is for `Content-
// Disposition: attachment`.
func (s *Server) handleLogsDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tail := r.URL.Query().Get("tail")
	if tail == "" {
		tail = "all"
	}

	rc, err := s.docker.ContainerLogs(r.Context(), id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     false,
		Tail:       tail,
		Timestamps: true,
	})
	if err != nil {
		http.Error(w, "logs: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Disposition", `attachment; filename="`+id+`.log"`)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = stdcopy.StdCopy(w, w, rc)
}

// wsWriter adapts an io.Writer to forward each Write as a binary websocket
// message; used as both the stdout and stderr sink for stdcopy.StdCopy so
// log lines from either stream reach the browser as they arrive.
type wsWriter struct {
	conn *websocket.Conn
}

func (w *wsWriter) Write(p []byte) (int, error) {
	if err := w.conn.WriteMessage(websocket.TextMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}
