package admin

import (
	"context"

	"swarmdash/internal/dockerutil"
)

// detectRuntime records whether admin's own Docker daemon is rootless (see
// docs/rootless.md), so the dashboard can warn about what that limits.
// Best effort: if `docker info` fails, admin just assumes a regular daemon.
func (s *Server) detectRuntime(ctx context.Context) {
	info, err := s.docker.Info(ctx)
	if err != nil {
		s.log.Warn("docker info failed, skipping rootless detection", "err", err)
		return
	}
	s.runtime = dockerutil.RuntimeFromInfo(info)
	if s.runtime.Rootless {
		s.log.Warn("docker daemon runs in rootless mode: overlay networks and the ingress routing mesh are unavailable (see docs/rootless.md)",
			"cgroup_version", s.runtime.CgroupVersion)
	}
}
