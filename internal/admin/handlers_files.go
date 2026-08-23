package admin

import (
	"io"
	"net/http"
	"net/url"
)

func (s *Server) handleLogsDownload(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskID")
	nodeID, containerID, err := s.taskContainer(r.Context(), taskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	s.proxyDownload(w, r, nodeID, "/v1/containers/"+containerID+"/logs/download")
}

func (s *Server) handleFilesList(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskID")
	nodeID, containerID, err := s.taskContainer(r.Context(), taskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	path := r.URL.Query().Get("path")
	// Audited same as file download below - the /files listing already
	// reveals filenames (and so plenty about what's mounted into the
	// container) even without downloading anything.
	s.audit(r, "container.files.list", taskID, path, nil)
	resp, err := s.agentGet(r.Context(), nodeID, "/v1/containers/"+containerID+"/files?path="+url.QueryEscape(path))
	if err != nil {
		http.Error(w, "agent request: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskID")
	nodeID, containerID, err := s.taskContainer(r.Context(), taskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	// Same secrets-exfiltration concern as the RBAC comment on this route
	// in routes.go: containers routinely bind-mount Docker secrets, so
	// every file actually pulled out of one is worth a durable record of
	// who did it and which path they took.
	s.audit(r, "container.files.download", taskID, path, nil)
	s.proxyDownload(w, r, nodeID, "/v1/containers/"+containerID+"/files/download?path="+url.QueryEscape(path))
}

// proxyDownload forwards a plain (non-websocket) GET to the node's agent
// and streams the response straight through, preserving headers like
// Content-Disposition that the agent set for a file/log download.
func (s *Server) proxyDownload(w http.ResponseWriter, r *http.Request, nodeID, agentPath string) {
	resp, err := s.agentGet(r.Context(), nodeID, agentPath)
	if err != nil {
		http.Error(w, "agent request: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for _, h := range []string{"Content-Disposition", "Content-Type"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
