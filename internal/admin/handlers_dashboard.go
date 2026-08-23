package admin

import (
	"encoding/json"
	"html/template"
	"net/http"
	"time"

	"github.com/docker/docker/api/types/swarm"

	"swarmdash/internal/store"
	"swarmdash/internal/updatecheck"
)

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	nodes, err := s.listNodes(ctx)
	if err != nil {
		http.Error(w, "list nodes: "+err.Error(), http.StatusBadGateway)
		return
	}
	services, err := s.listServices(ctx)
	if err != nil {
		http.Error(w, "list services: "+err.Error(), http.StatusBadGateway)
		return
	}
	tasks, err := s.docker.TaskList(ctx, swarm.TaskListOptions{})
	if err != nil {
		http.Error(w, "list tasks: "+err.Error(), http.StatusBadGateway)
		return
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

	degraded := 0
	for _, svc := range services {
		if svc.ServiceStatus != nil && svc.ServiceStatus.RunningTasks < svc.ServiceStatus.DesiredTasks {
			degraded++
		}
	}

	cpuCapacity, memCapacity := clusterCapacity(nodes)
	cpuReserved := reservedResources(services)
	s.memUsageMu.RLock()
	memUsed := s.memUsageBytes
	s.memUsageMu.RUnlock()

	history, err := s.store.ListClusterSamples(time.Now().Add(-24 * time.Hour))
	if err != nil {
		s.log.Warn("list cluster samples", "err", err)
	}

	currentVersion := updatecheck.CurrentVersion
	if s.isUpdateCheckDisabled() {
		currentVersion = ""
	}

	s.render(w, r, "dashboard.html", map[string]any{
		"User":           userFromContext(r),
		"Nodes":          nodes,
		"Services":       services,
		"NodeCount":      len(nodes),
		"ServiceCount":   len(services),
		"StackCount":     len(groupByStack(services)),
		"DesiredTasks":   desired,
		"RunningTasks":   running,
		"FailedTasks":    failed,
		"DegradedCount":  degraded,
		"CPUCapacity":    cpuCapacity,
		"CPUReserved":    cpuReserved,
		"CPUPct":         pct(cpuReserved, cpuCapacity),
		"MemCapacity":    memCapacity,
		"MemUsed":        memUsed,
		"MemPct":         pct(memUsed, memCapacity),
		"HistoryJSON":    clusterHistoryJSON(history),
		"CurrentVersion": currentVersion,
	})
}

// clusterHistorySample is the trimmed-down shape sent to the dashboard's
// history charts - just what the canvas drawing code needs, keyed by
// millisecond timestamp so JS can use it directly.
type clusterHistorySample struct {
	T   int64   `json:"t"`
	Run int     `json:"run"`
	Des int     `json:"des"`
	CPU float64 `json:"cpu"`
	Mem float64 `json:"mem"`
}

func clusterHistoryJSON(samples []store.ClusterSample) template.JS {
	out := make([]clusterHistorySample, 0, len(samples))
	for _, s := range samples {
		out = append(out, clusterHistorySample{
			T:   s.Time.UnixMilli(),
			Run: s.RunningTasks,
			Des: s.DesiredTasks,
			CPU: s.CPUReservedPct,
			Mem: s.MemUsedPct,
		})
	}
	buf, err := json.Marshal(out)
	if err != nil {
		return "[]"
	}
	return template.JS(buf)
}

// clusterCapacity sums the advertised CPU/memory resources of every node,
// i.e. what the swarm scheduler itself treats as schedulable capacity.
func clusterCapacity(nodes []swarm.Node) (nanoCPUs, memoryBytes int64) {
	for _, n := range nodes {
		nanoCPUs += n.Description.Resources.NanoCPUs
		memoryBytes += n.Description.Resources.MemoryBytes
	}
	return
}

// reservedResources sums each service's per-task CPU reservation across
// its desired task count. Services with no reservation set (the common
// case) contribute zero - this mirrors what the scheduler actually
// accounts against node capacity, not real runtime usage. Memory used to
// be computed here too, but the dashboard now shows real usage instead
// (see clusterMemoryUsed in agentclient.go) - CPU still uses the
// reservation spec since Docker has no cheap way to report aggregate CPU
// usage.
func reservedResources(services []swarm.Service) (nanoCPUs int64) {
	for _, svc := range services {
		if svc.ServiceStatus == nil || svc.ServiceStatus.DesiredTasks == 0 {
			continue
		}
		res := svc.Spec.TaskTemplate.Resources
		if res == nil || res.Reservations == nil {
			continue
		}
		nanoCPUs += res.Reservations.NanoCPUs * int64(svc.ServiceStatus.DesiredTasks)
	}
	return
}

// sumDesiredTasks totals each service's current desired replica count, per
// Docker's own ServiceStatus - unlike len(tasks) from a raw TaskList, this
// doesn't include the shut-down/historical task records Docker retains
// alongside currently-running ones, so "running/desired" actually means
// something (see poller.go's sampleCluster, which uses the same number for
// the dashboard history chart).
func sumDesiredTasks(services []swarm.Service) int {
	total := 0
	for _, svc := range services {
		if svc.ServiceStatus != nil {
			total += int(svc.ServiceStatus.DesiredTasks)
		}
	}
	return total
}

func pct(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}
