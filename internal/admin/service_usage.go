package admin

import (
	"encoding/json"
	"html/template"
	"time"

	"swarmdash/internal/store"
)

// Real per-service resource usage. The poller already asks every node's
// agent for its stats summary once a minute (sampleCluster); agents also
// break that down per swarm task container, which is aggregated here into
// one figure per service. Pages read the latest snapshot from memory - a
// minute stale at worst - instead of fanning out to every agent on each
// load, and the service page charts the persisted samples
// (store.ServiceSample) for the last 24h.

type serviceUsage struct {
	CPUCores float64
	MemBytes int64
	// MemLimit sums the tasks' memory limits as the kernel sees them (the
	// node's total memory for a task without a limit), so MemPct is "of
	// what these tasks may use", not of the cluster.
	MemLimit int64
	Tasks    int
}

// MemPct is memory used as a percentage of MemLimit.
func (u serviceUsage) MemPct() float64 {
	return pct(u.MemBytes, u.MemLimit)
}

type serviceUsageSnapshot struct {
	at       time.Time
	services map[string]serviceUsage
	tasks    map[string]taskResourceUsage
}

func aggregateServiceUsage(perNode map[string]nodeResourceStats) serviceUsageSnapshot {
	snap := serviceUsageSnapshot{
		at:       time.Now(),
		services: map[string]serviceUsage{},
		tasks:    map[string]taskResourceUsage{},
	}
	for _, st := range perNode {
		for _, t := range st.Tasks {
			snap.tasks[t.TaskID] = t
			if t.ServiceName == "" {
				continue
			}
			u := snap.services[t.ServiceName]
			u.CPUCores += t.CPUCores
			u.MemBytes += t.MemBytes
			u.MemLimit += t.MemLimit
			u.Tasks++
			snap.services[t.ServiceName] = u
		}
	}
	return snap
}

// currentServiceUsage returns the latest per-service and per-task usage,
// and when it was sampled (zero before the first sample after startup).
func (s *Server) currentServiceUsage() (services map[string]serviceUsage, tasks map[string]taskResourceUsage, at time.Time) {
	s.memUsageMu.RLock()
	defer s.memUsageMu.RUnlock()
	return s.serviceUsage.services, s.serviceUsage.tasks, s.serviceUsage.at
}

type serviceHistorySample struct {
	T   int64   `json:"t"`
	CPU float64 `json:"cpu"`
	Mem int64   `json:"mem"`
}

func serviceHistoryJSON(samples []store.ServiceSample) template.JS {
	out := make([]serviceHistorySample, 0, len(samples))
	for _, s := range samples {
		out = append(out, serviceHistorySample{T: s.Time.UnixMilli(), CPU: s.CPUCores, Mem: s.MemBytes})
	}
	buf, err := json.Marshal(out)
	if err != nil {
		return "[]"
	}
	return template.JS(buf)
}
