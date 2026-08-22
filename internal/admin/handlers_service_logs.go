package admin

import (
	"net/http"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
)

// handleServiceLogsPage renders the aggregated log viewer for a service -
// unlike the per-task log page, this merges every task/replica's output
// into one stream, so you don't have to know which task to look at (or
// re-check it after a rolling update replaces the tasks).
func (s *Server) handleServiceLogsPage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.render(w, r, "service_logs.html", map[string]any{
		"ServiceName": name,
		"User":        userFromContext(r),
	})
}

// handleServiceLogsProxy streams a service's merged logs over a websocket.
// Unlike task-level exec/logs, this doesn't need to go through a per-node
// agent - ServiceLogs is a swarm-manager API answered directly by s.docker.
func (s *Server) handleServiceLogsProxy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	svc, err := s.getService(r.Context(), name)
	if err != nil {
		http.Error(w, "get service: "+err.Error(), http.StatusNotFound)
		return
	}

	q := r.URL.Query()
	tail := q.Get("tail")
	if tail == "" {
		tail = "200"
	}
	follow := q.Get("follow") != "false"

	rc, err := s.docker.ServiceLogs(r.Context(), svc.ID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Tail:       tail,
		Details:    true,
	})
	if err != nil {
		http.Error(w, "logs: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer rc.Close()

	conn, err := browserUpgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Warn("ws upgrade failed", "err", err)
		return
	}
	defer conn.Close()

	pw := &wsTextWriter{conn: conn}
	_, _ = stdcopy.StdCopy(pw, pw, rc)
}

// handleServiceLogsDownload returns a service's merged logs as a plain HTTP
// attachment (never follows), same shape as the per-task download.
func (s *Server) handleServiceLogsDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	svc, err := s.getService(r.Context(), name)
	if err != nil {
		http.Error(w, "get service: "+err.Error(), http.StatusNotFound)
		return
	}

	rc, err := s.docker.ServiceLogs(r.Context(), svc.ID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     false,
		Tail:       "all",
		Timestamps: true,
		Details:    true,
	})
	if err != nil {
		http.Error(w, "logs: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Disposition", `attachment; filename="`+svc.Spec.Name+`.log"`)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = stdcopy.StdCopy(w, w, rc)
}
