package admin

import "net/http"

// handleStatusPage is deliberately unauthenticated (for status-page /
// uptime-monitor use) and deliberately minimal - only aggregate counts,
// nothing that identifies specific nodes, images, or services by name.
func (s *Server) handleStatusPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	nodes, err := s.listNodes(ctx)
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	services, err := s.listServices(ctx)
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}

	readyNodes := 0
	for _, n := range nodes {
		if n.Status.State == "ready" {
			readyNodes++
		}
	}
	degraded := 0
	for _, svc := range services {
		if svc.ServiceStatus != nil && svc.ServiceStatus.RunningTasks < svc.ServiceStatus.DesiredTasks {
			degraded++
		}
	}
	healthy := degraded == 0 && readyNodes == len(nodes)

	s.render(w, r, "status.html", map[string]any{
		"Healthy":      healthy,
		"NodeCount":    len(nodes),
		"ReadyNodes":   readyNodes,
		"ServiceCount": len(services),
		"Degraded":     degraded,
	})
}
