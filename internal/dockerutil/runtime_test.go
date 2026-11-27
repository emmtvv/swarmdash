package dockerutil

import (
	"testing"

	"github.com/docker/docker/api/types/system"
)

func TestRuntimeFromInfo(t *testing.T) {
	for _, tc := range []struct {
		name     string
		info     system.Info
		rootless bool
		noStats  bool
	}{
		{"rootful", system.Info{SecurityOptions: []string{"name=seccomp,profile=builtin", "name=cgroupns"}, CgroupVersion: "2"}, false, false},
		{"rootless cgroup v2", system.Info{SecurityOptions: []string{"name=seccomp,profile=builtin", "name=rootless", "name=cgroupns"}, CgroupVersion: "2"}, true, false},
		{"rootless cgroup v1", system.Info{SecurityOptions: []string{"name=rootless"}, CgroupVersion: "1"}, true, true},
		{"rootless combined entry", system.Info{SecurityOptions: []string{"name=seccomp,name=rootless"}}, true, true},
		{"empty", system.Info{}, false, false},
	} {
		rt := RuntimeFromInfo(tc.info)
		if rt.Rootless != tc.rootless || rt.StatsUnavailable() != tc.noStats {
			t.Errorf("%s: got rootless=%v statsUnavailable=%v, want %v/%v", tc.name, rt.Rootless, rt.StatsUnavailable(), tc.rootless, tc.noStats)
		}
	}
}
