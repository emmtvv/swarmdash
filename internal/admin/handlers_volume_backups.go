package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types/volume"
)

// Volume backups (Volumes -> Backups): the work happens on the agent of the
// node that holds the volume (see internal/agent/volume_backups.go); this
// side lists and starts jobs and streams archives through for download
// and upload. Volumes are node-local, so the page works on one node at a
// time, like the Volumes page itself.

type volumeBackup struct {
	ID         string    `json:"id"`
	Volume     string    `json:"volume"`
	Source     string    `json:"source"`
	Status     string    `json:"status"`
	Error      string    `json:"error"`
	SizeBytes  int64     `json:"size_bytes"`
	CreatedAt  time.Time `json:"created_at"`
	FinishedAt time.Time `json:"finished_at"`
}

type volumeRestore struct {
	ID         string    `json:"id"`
	BackupID   string    `json:"backup_id"`
	Volume     string    `json:"volume"`
	Status     string    `json:"status"`
	Error      string    `json:"error"`
	CreatedAt  time.Time `json:"created_at"`
	FinishedAt time.Time `json:"finished_at"`
}

// Active reports whether a job is still in flight, for the page's polling.
func (b volumeBackup) Active() bool  { return b.Status == "queued" || b.Status == "running" }
func (r volumeRestore) Active() bool { return r.Status == "queued" || r.Status == "running" }

type volumeBackupList struct {
	Enabled  bool            `json:"enabled"`
	Dir      string          `json:"dir"`
	Backups  []volumeBackup  `json:"backups"`
	Restores []volumeRestore `json:"restores"`
}

// agentStreamClient is agentHTTPClient without its overall timeout: a
// multi-gigabyte archive streaming through is supposed to take a while.
// Connection setup is still bounded by the transport's dial timeouts.
func (s *Server) agentStreamClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 60 * time.Second
	if s.agentTLS != nil {
		transport.TLSClientConfig = s.agentTLS
	}
	return &http.Client{Transport: transport}
}

func (s *Server) loadVolumeBackups(ctx context.Context, nodeID string) (volumeBackupList, error) {
	var out volumeBackupList
	resp, err := s.agentGet(ctx, nodeID, "/v1/volume-backups")
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return out, fmt.Errorf("this node's agent is too old for volume backups - upgrade it")
	}
	if resp.StatusCode != http.StatusOK {
		return out, agentError(resp)
	}
	err = json.NewDecoder(resp.Body).Decode(&out)
	return out, err
}

// agentError turns a non-2xx agent response into an error carrying the
// agent's own message.
func agentError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := strings.TrimSpace(string(b))
	if msg == "" {
		msg = resp.Status
	}
	return fmt.Errorf("%s", msg)
}

func (s *Server) volumeBackupsData(r *http.Request) (map[string]any, error) {
	ctx := r.Context()
	nodes, err := s.listNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no nodes found")
	}
	nodeID := r.URL.Query().Get("node")
	if nodeID == "" {
		nodeID = nodes[0].ID
	}

	data := map[string]any{
		"User":       userFromContext(r),
		"Nodes":      nodes,
		"SelectedID": nodeID,
		"Error":      r.URL.Query().Get("error"),
		"Flash":      r.URL.Query().Get("flash"),
	}
	list, err := s.loadVolumeBackups(ctx, nodeID)
	if err != nil {
		data["AgentError"] = err.Error()
		return data, nil
	}
	active := false
	for _, b := range list.Backups {
		active = active || b.Active()
	}
	for _, rj := range list.Restores {
		active = active || rj.Active()
	}
	data["List"] = list
	data["Active"] = active

	var volumes []*volume.Volume
	if resp, err := s.agentGet(ctx, nodeID, "/v1/volumes"); err == nil {
		if resp.StatusCode == http.StatusOK {
			_ = json.NewDecoder(resp.Body).Decode(&volumes)
		}
		resp.Body.Close()
	}
	sort.Slice(volumes, func(i, j int) bool { return volumes[i].Name < volumes[j].Name })
	data["Volumes"] = volumes
	data["Preselect"] = r.URL.Query().Get("volume")
	return data, nil
}

func (s *Server) handleVolumeBackupsPage(w http.ResponseWriter, r *http.Request) {
	data, err := s.volumeBackupsData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.render(w, r, "volume_backups.html", data)
}

// handleVolumeBackupsJobs re-renders just the jobs tables, polled by the
// page every few seconds while anything is still running.
func (s *Server) handleVolumeBackupsJobs(w http.ResponseWriter, r *http.Request) {
	data, err := s.volumeBackupsData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.renderer.RenderFragment(w, "volume_backup_jobs", data, s.requestLanguage(r))
}

func backupsPageURL(nodeID, key, msg string) string {
	u := "/volumes/backups?node=" + url.QueryEscape(nodeID)
	if key != "" {
		u += "&" + key + "=" + url.QueryEscape(msg)
	}
	return u
}

func (s *Server) handleVolumeBackupCreate(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("node")
	name := r.PathValue("name")
	resp, err := s.agentPost(r.Context(), nodeID, "/v1/volumes/"+url.PathEscape(name)+"/backup", nil)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			err = agentError(resp)
		}
	}
	s.audit(r, "volume.backup", name, "node="+nodeID, err)
	if err != nil {
		redirect(w, r, backupsPageURL(nodeID, "error", err.Error()))
		return
	}
	redirect(w, r, backupsPageURL(nodeID, "flash", s.t(r, "volume_backups.backup_of_started", name)))
}

func (s *Server) handleVolumeBackupRestore(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	nodeID := r.URL.Query().Get("node")
	id := r.PathValue("id")
	target := strings.TrimSpace(r.FormValue("volume"))
	body, _ := json.Marshal(map[string]string{"volume": target})
	resp, err := s.agentPost(r.Context(), nodeID, "/v1/volume-backups/"+url.PathEscape(id)+"/restore", bytes.NewReader(body))
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			err = agentError(resp)
		}
	}
	s.audit(r, "volume.restore", target, "node="+nodeID+" backup="+id, err)
	if err != nil {
		redirect(w, r, backupsPageURL(nodeID, "error", err.Error()))
		return
	}
	redirect(w, r, backupsPageURL(nodeID, "flash", s.t(r, "volume_backups.restore_started")))
}

func (s *Server) handleVolumeBackupDelete(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("node")
	id := r.PathValue("id")
	resp, err := s.agentPost(r.Context(), nodeID, "/v1/volume-backups/"+url.PathEscape(id)+"/delete", nil)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			err = agentError(resp)
		}
	}
	s.audit(r, "volume.backup.delete", id, "node="+nodeID, err)
	if err != nil {
		redirect(w, r, backupsPageURL(nodeID, "error", err.Error()))
		return
	}
	redirect(w, r, backupsPageURL(nodeID, "", ""))
}

// handleVolumeBackupDownload streams an archive from the agent straight to
// the browser, never holding more than a buffer of it in admin.
func (s *Server) handleVolumeBackupDownload(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("node")
	id := r.PathValue("id")
	// Volumes routinely hold databases and their credentials - a durable
	// record of who pulled one out, like container file downloads.
	s.audit(r, "volume.backup.download", id, "node="+nodeID, nil)

	req, err := s.agentRequest(r.Context(), http.MethodGet, nodeID, "/v1/volume-backups/"+url.PathEscape(id)+"/download", nil)
	if err != nil {
		http.Error(w, "agent request: "+err.Error(), http.StatusBadGateway)
		return
	}
	resp, err := s.agentStreamClient().Do(req)
	if err != nil {
		http.Error(w, "agent request: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.Error(w, agentError(resp).Error(), resp.StatusCode)
		return
	}
	for _, h := range []string{"Content-Disposition", "Content-Type", "Content-Length"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, resp.Body)
}

// handleVolumeBackupUpload streams a .tar.gz from the browser to the
// node's agent as a new backup of ?volume=. The browser sends the file as
// the raw request body with the CSRF token in a header (see
// volume_backups.html) rather than as a multipart form, because the CSRF
// middleware parses multipart bodies - spooling a multi-gigabyte upload to
// admin's disk before this handler even runs.
func (s *Server) handleVolumeBackupUpload(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("node")
	vol := strings.TrimSpace(r.URL.Query().Get("volume"))
	if vol == "" {
		http.Error(w, s.t(r, "volume_backups.volume_is_required"), http.StatusBadRequest)
		return
	}
	req, err := s.agentRequest(r.Context(), http.MethodPost, nodeID, "/v1/volume-backups/upload?volume="+url.QueryEscape(vol), r.Body)
	if err != nil {
		http.Error(w, "agent request: "+err.Error(), http.StatusBadGateway)
		return
	}
	req.ContentLength = r.ContentLength
	req.Header.Set("Content-Type", "application/gzip")
	resp, err := s.agentStreamClient().Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			err = agentError(resp)
		}
	}
	s.audit(r, "volume.backup.upload", vol, "node="+nodeID, err)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusOK)
}
