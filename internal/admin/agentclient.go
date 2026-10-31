package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/docker/docker/api/types/swarm"
	"github.com/gorilla/websocket"

	"swarmdash/internal/web"
)

// nodeAgentAddr resolves the reachable IP for a node's agent. It reuses the
// node's swarm advertise address (Status.Addr) - the same address the
// manager already uses to reach that node for cluster control traffic - so
// no extra overlay network or service discovery is required. Falls back to
// ManagerStatus.Addr when Status.Addr is "0.0.0.0" (see web.NodeAddr).
func (s *Server) nodeAgentAddr(ctx context.Context, nodeID string) (string, error) {
	node, _, err := s.docker.NodeInspectWithRaw(ctx, nodeID)
	if err != nil {
		return "", fmt.Errorf("inspect node %s: %w", nodeID, err)
	}
	addr := web.NodeAddr(node)
	if addr == "" || addr == "0.0.0.0" {
		return "", fmt.Errorf("node %s has no advertised address", nodeID)
	}
	return addr, nil
}

func (s *Server) agentBaseURL(ctx context.Context, nodeID string) (string, error) {
	addr, err := s.nodeAgentAddr(ctx, nodeID)
	if err != nil {
		return "", err
	}
	scheme := "http"
	if s.agentTLS != nil {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s:%s", scheme, addr, s.cfg.AgentPort), nil
}

func (s *Server) agentHTTPClient() *http.Client {
	client := &http.Client{Timeout: 15 * time.Second}
	if s.agentTLS != nil {
		client.Transport = &http.Transport{TLSClientConfig: s.agentTLS}
	}
	return client
}

func (s *Server) agentGet(ctx context.Context, nodeID, path string) (*http.Response, error) {
	return s.agentDo(ctx, http.MethodGet, nodeID, path, nil)
}

func (s *Server) agentPost(ctx context.Context, nodeID, path string, body io.Reader) (*http.Response, error) {
	return s.agentDo(ctx, http.MethodPost, nodeID, path, body)
}

func (s *Server) agentDo(ctx context.Context, method, nodeID, path string, body io.Reader) (*http.Response, error) {
	req, err := s.agentRequest(ctx, method, nodeID, path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return s.agentHTTPClient().Do(req)
}

// agentRequest builds an authenticated request to a node's agent, for
// callers that need their own client (see agentStreamClient).
func (s *Server) agentRequest(ctx context.Context, method, nodeID, path string, body io.Reader) (*http.Request, error) {
	base, err := s.agentBaseURL(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.ClusterSecret)
	return req, nil
}

// dialAgentWS opens a websocket to the given node's agent, authenticated
// with the cluster secret via the Authorization header (safe here because
// admin, not the browser, is the one dialing the agent directly).
func (s *Server) dialAgentWS(ctx context.Context, nodeID, path string) (*websocket.Conn, error) {
	base, err := s.agentBaseURL(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(base + path)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}
	header := http.Header{"Authorization": []string{"Bearer " + s.cfg.ClusterSecret}}
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second, TLSClientConfig: s.agentTLS}
	conn, _, err := dialer.DialContext(ctx, u.String(), header)
	return conn, err
}

// taskContainer resolves a task ID to the node it's running on and the
// underlying container ID, needed to build agent API paths.
func (s *Server) taskContainer(ctx context.Context, taskID string) (nodeID, containerID string, err error) {
	task, _, err := s.docker.TaskInspectWithRaw(ctx, taskID)
	if err != nil {
		return "", "", fmt.Errorf("inspect task %s: %w", taskID, err)
	}
	if task.Status.ContainerStatus == nil || task.Status.ContainerStatus.ContainerID == "" {
		return "", "", fmt.Errorf("task %s has no running container (state: %s)", taskID, task.Status.State)
	}
	return task.NodeID, task.Status.ContainerStatus.ContainerID, nil
}

// nodeResourceStats is a node's live resource usage as reported by its
// agent's /v1/stats/summary: real, non-cache memory usage and CPU usage in
// fractional cores summed across every container currently running on that
// node, plus disk usage of the filesystem backing Docker's data directory -
// unlike reservedResources (which only reflects what services declare via
// deploy.resources.reservations.*), this is what's actually being used.
type nodeResourceStats struct {
	MemUsedBytes   int64
	CPUUsedCores   float64
	DiskTotalBytes int64
	DiskUsedBytes  int64
	Available      bool // false if the node's agent couldn't be reached
	// Tasks is per-task-container usage on this node; empty from agents
	// older than this field.
	Tasks []taskResourceUsage
}

type taskResourceUsage struct {
	TaskID      string  `json:"task_id"`
	ServiceName string  `json:"service_name"`
	MemBytes    int64   `json:"mem_bytes"`
	MemLimit    int64   `json:"mem_limit"`
	CPUCores    float64 `json:"cpu_cores"`
}

// nodeStats fetches one node's live resource usage from its agent. It
// returns a zero-value, unavailable result (rather than an error) on any
// failure, since a single unreachable agent shouldn't block the rest of a
// page from rendering.
func (s *Server) nodeStats(ctx context.Context, nodeID, hostname string) nodeResourceStats {
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := s.agentGet(reqCtx, nodeID, "/v1/stats/summary")
	if err != nil {
		s.log.Warn("node stats: agent request failed", "node", hostname, "err", err)
		return nodeResourceStats{}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		s.log.Warn("node stats: agent error", "node", hostname, "status", resp.StatusCode)
		return nodeResourceStats{}
	}
	var v struct {
		MemUsedBytes   int64               `json:"mem_used_bytes"`
		CPUUsedCores   float64             `json:"cpu_used_cores"`
		DiskTotalBytes int64               `json:"disk_total_bytes"`
		DiskUsedBytes  int64               `json:"disk_used_bytes"`
		Tasks          []taskResourceUsage `json:"tasks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		s.log.Warn("node stats: decode failed", "node", hostname, "err", err)
		return nodeResourceStats{}
	}
	return nodeResourceStats{
		MemUsedBytes:   v.MemUsedBytes,
		CPUUsedCores:   v.CPUUsedCores,
		DiskTotalBytes: v.DiskTotalBytes,
		DiskUsedBytes:  v.DiskUsedBytes,
		Available:      true,
		Tasks:          v.Tasks,
	}
}

// nodesStats fans out nodeStats across every ready node concurrently,
// keyed by node ID, for display on the nodes list page.
func (s *Server) nodesStats(ctx context.Context, nodes []swarm.Node) map[string]nodeResourceStats {
	out := make(map[string]nodeResourceStats, len(nodes))
	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	for _, n := range nodes {
		if n.Status.State != swarm.NodeStateReady {
			continue
		}
		wg.Add(1)
		go func(n swarm.Node) {
			defer wg.Done()
			st := s.nodeStats(ctx, n.ID, n.Description.Hostname)
			mu.Lock()
			out[n.ID] = st
			mu.Unlock()
		}(n)
	}
	wg.Wait()
	return out
}
