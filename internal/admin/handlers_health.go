package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// handleHealth is a liveness/readiness probe for orchestrators (a Docker
// HEALTHCHECK, a Swarm healthcheck on the admin service, an external
// uptime check, ...). Deliberately unauthenticated, like /status, but
// unlike /status it actually exercises the two things admin depends on
// (its store - SQLite or MongoDB, see store.Interface - and the local
// Docker daemon) rather than just confirming the HTTP server is accepting
// connections - a stuck store should fail the probe even though the
// process is technically still up.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	checks := map[string]string{}
	healthy := true

	if err := s.store.Ping(); err != nil {
		checks["store"] = err.Error()
		healthy = false
	} else {
		checks["store"] = "ok"
	}

	if _, err := s.docker.Ping(ctx); err != nil {
		checks["docker"] = err.Error()
		healthy = false
	} else {
		checks["docker"] = "ok"
	}

	w.Header().Set("Content-Type", "application/json")
	if !healthy {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	status := "ok"
	if !healthy {
		status = "unhealthy"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "checks": checks})
}
