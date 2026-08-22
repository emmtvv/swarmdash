package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/docker/docker/api/types/swarm"

	"swarmdash/internal/store"
)

const (
	pollInterval      = 10 * time.Second
	degradedThreshold = 2 * time.Minute

	// clusterSampleEvery samples cluster-wide history (dashboard "Last 24h"
	// charts) once every this many poll ticks, i.e. every ~60s - frequent
	// enough for a useful trend line, infrequent enough to keep 14 days of
	// retention cheap (~1440 samples/day).
	clusterSampleEvery     = 6
	clusterSampleRetention = 14 * 24 * time.Hour
)

// pollerState is in-memory, deliberately not persisted: it only needs to
// survive between ticks of a single running admin process, and rebuilding
// it from scratch (with a "seed" tick that never fires events) after a
// restart is simpler and safer than trying to resume mid-episode.
type pollerState struct {
	seeded        bool
	taskStates    map[string]swarm.TaskState
	degradedSince map[string]time.Time
	nodeStates    map[string]swarm.NodeState
	tick          int
}

// runPoller watches for task state transitions, services stuck below
// their desired replica count, and nodes going down - durable history for
// the former (task_events), webhook notifications for the latter two.
// Docker doesn't push any of this, so polling is the only option (same
// reasoning as the service detail SSE view, just cluster-wide and
// persisted instead of per-service and live-only).
func (s *Server) runPoller(ctx context.Context) {
	st := &pollerState{
		taskStates:    map[string]swarm.TaskState{},
		degradedSince: map[string]time.Time{},
		nodeStates:    map[string]swarm.NodeState{},
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pollOnce(ctx, st)
		}
	}
}

func (s *Server) pollOnce(ctx context.Context, st *pollerState) {
	tasks, err := s.docker.TaskList(ctx, swarm.TaskListOptions{})
	if err != nil {
		s.log.Warn("poller: list tasks", "err", err)
		return
	}
	services, err := s.listServices(ctx)
	if err != nil {
		s.log.Warn("poller: list services", "err", err)
		return
	}
	nodes, err := s.listNodes(ctx)
	if err != nil {
		s.log.Warn("poller: list nodes", "err", err)
		return
	}

	hostnames := make(map[string]string, len(nodes))
	for _, n := range nodes {
		hostnames[n.ID] = n.Description.Hostname
	}
	serviceNames := make(map[string]string, len(services))
	for _, svc := range services {
		serviceNames[svc.ID] = svc.Spec.Name
	}

	if st.seeded {
		for _, t := range tasks {
			prev, existed := st.taskStates[t.ID]
			if existed && prev != t.Status.State {
				if err := s.store.AppendTaskEvent(store.TaskEvent{
					ServiceName: serviceNames[t.ServiceID],
					TaskID:      t.ID,
					Node:        hostnames[t.NodeID],
					State:       string(t.Status.State),
					Message:     t.Status.Err,
				}); err != nil {
					s.log.Warn("poller: append task event", "err", err)
				}
			}
		}
	}
	newTaskStates := make(map[string]swarm.TaskState, len(tasks))
	for _, t := range tasks {
		newTaskStates[t.ID] = t.Status.State
	}
	st.taskStates = newTaskStates

	now := time.Now()
	for _, svc := range services {
		if svc.ServiceStatus == nil {
			continue
		}
		degraded := svc.ServiceStatus.RunningTasks < svc.ServiceStatus.DesiredTasks
		since, tracked := st.degradedSince[svc.ID]
		switch {
		case degraded && !tracked:
			st.degradedSince[svc.ID] = now
		case degraded && tracked && st.seeded && now.Sub(since) >= degradedThreshold && now.Sub(since) < degradedThreshold+pollInterval:
			s.fireWebhooks(ctx, "service_degraded", svc.Spec.Name,
				fmt.Sprintf("%d/%d tasks running for over %s", svc.ServiceStatus.RunningTasks, svc.ServiceStatus.DesiredTasks, degradedThreshold))
		case !degraded && tracked:
			delete(st.degradedSince, svc.ID)
			if st.seeded && now.Sub(since) >= degradedThreshold {
				s.fireWebhooks(ctx, "service_recovered", svc.Spec.Name, "back to desired replica count")
			}
		}
	}

	for _, n := range nodes {
		prev, existed := st.nodeStates[n.ID]
		st.nodeStates[n.ID] = n.Status.State
		if !existed || !st.seeded {
			continue
		}
		if prev == swarm.NodeStateReady && (n.Status.State == swarm.NodeStateDown || n.Status.State == swarm.NodeStateDisconnected) {
			s.fireWebhooks(ctx, "node_down", n.Description.Hostname, "node state: "+string(n.Status.State))
		} else if prev != swarm.NodeStateReady && n.Status.State == swarm.NodeStateReady {
			s.fireWebhooks(ctx, "node_recovered", n.Description.Hostname, "node is ready again")
		}
	}

	st.tick++
	if st.tick%clusterSampleEvery == 0 {
		s.sampleCluster(ctx, nodes, services, tasks)
	}

	st.seeded = true
}

// sampleCluster records one ClusterSample for the dashboard's "Last 24h"
// history charts, and refreshes the live memory-usage cache the dashboard
// reads on every page load. CPU still uses reservation math
// (clusterCapacity/reservedResources in handlers_dashboard.go) - Docker
// gives no aggregate cgroup CPU usage cheaply - but real memory usage is
// available per-node via each agent's /v1/stats/summary, so we fan out for
// that instead of reporting the reservation spec (which is 0 for any
// service that never set deploy.resources.reservations.memory).
func (s *Server) sampleCluster(ctx context.Context, nodes []swarm.Node, services []swarm.Service, tasks []swarm.Task) {
	readyNodes := 0
	for _, n := range nodes {
		if n.Status.State == swarm.NodeStateReady {
			readyNodes++
		}
	}
	degraded := 0
	for _, svc := range services {
		if svc.ServiceStatus != nil && svc.ServiceStatus.RunningTasks < svc.ServiceStatus.DesiredTasks {
			degraded++
		}
	}
	running, failed := 0, 0
	for _, t := range tasks {
		switch t.Status.State {
		case swarm.TaskStateRunning:
			running++
		case swarm.TaskStateFailed, swarm.TaskStateRejected:
			failed++
		}
	}
	desired := sumDesiredTasks(services)
	cpuCapacity, memCapacity := clusterCapacity(nodes)
	cpuReserved := reservedResources(services)
	memUsed := s.clusterMemoryUsed(ctx, nodes)

	s.memUsageMu.Lock()
	s.memUsageBytes = memUsed
	s.memUsageMu.Unlock()

	if err := s.store.AppendClusterSample(store.ClusterSample{
		NodeCount:        len(nodes),
		ReadyNodes:       readyNodes,
		ServiceCount:     len(services),
		DegradedServices: degraded,
		RunningTasks:     running,
		DesiredTasks:     desired,
		FailedTasks:      failed,
		CPUReservedPct:   pct(cpuReserved, cpuCapacity),
		MemUsedPct:       pct(memUsed, memCapacity),
	}); err != nil {
		s.log.Warn("poller: append cluster sample", "err", err)
	}
	if err := s.store.PruneClusterSamples(time.Now().Add(-clusterSampleRetention)); err != nil {
		s.log.Warn("poller: prune cluster samples", "err", err)
	}
}

func (s *Server) fireWebhooks(ctx context.Context, event, target, message string) {
	s.log.Info("cluster event", "event", event, "target", target, "message", message)
	if err := s.store.AppendAudit(store.AuditEntry{
		Username: "system",
		Action:   event,
		Target:   target,
		Detail:   message,
		Success:  true,
	}); err != nil {
		s.log.Warn("append system audit entry", "err", err)
	}

	hooks, err := s.store.ListWebhooks()
	if err != nil {
		s.log.Warn("list webhooks", "err", err)
		return
	}
	if len(hooks) == 0 {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"event":   event,
		"target":  target,
		"message": message,
		"time":    time.Now().Format(time.RFC3339),
	})
	for _, h := range hooks {
		go func(name, url string) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
			if err != nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
			if err != nil {
				s.log.Warn("webhook delivery failed", "webhook", name, "err", err)
				return
			}
			resp.Body.Close()
		}(h.Name, h.URL)
	}
}
