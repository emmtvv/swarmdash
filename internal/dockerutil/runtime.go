package dockerutil

import (
	"strings"

	"github.com/docker/docker/api/types/system"
)

// Runtime describes the parts of a Docker daemon's setup that change what
// swarmdash can do on that node. See docs/rootless.md.
type Runtime struct {
	// Rootless is true when the daemon runs in rootless mode
	// (dockerd-rootless.sh / RootlessKit): no overlay networks, so no
	// multi-node Swarm or ingress routing mesh, and "host" networking is
	// RootlessKit's namespace rather than the real host's.
	Rootless bool
	// CgroupVersion is "1" or "2" as reported by the daemon ("" if it
	// doesn't say).
	CgroupVersion string
}

// RuntimeFromInfo extracts Runtime from a daemon's `docker info`.
func RuntimeFromInfo(info system.Info) Runtime {
	rt := Runtime{CgroupVersion: info.CgroupVersion}
	for _, opt := range info.SecurityOptions {
		// Entries look like "name=seccomp,profile=builtin" or
		// "name=rootless".
		for _, kv := range strings.Split(opt, ",") {
			if kv == "name=rootless" {
				rt.Rootless = true
			}
		}
	}
	return rt
}

// StatsUnavailable reports whether container CPU/memory stats are likely
// empty: a rootless daemon only gets resource stats on cgroup v2 (with the
// controllers delegated to the user via systemd).
func (rt Runtime) StatsUnavailable() bool {
	return rt.Rootless && rt.CgroupVersion != "2"
}
