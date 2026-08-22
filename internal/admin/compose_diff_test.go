package admin

import (
	"testing"

	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/swarm"
)

func TestImageTag(t *testing.T) {
	tests := []struct {
		name  string
		image string
		want  string
	}{
		{"digest suffix stripped", "nginx:alpine@sha256:abcdef1234567890", "nginx:alpine"},
		{"no digest is a no-op", "nginx:alpine", "nginx:alpine"},
		{"bare image without tag", "nginx", "nginx"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := imageTag(tt.image); got != tt.want {
				t.Errorf("imageTag(%q) = %q, want %q", tt.image, got, tt.want)
			}
		})
	}
}

func TestSortedEnv(t *testing.T) {
	t.Run("nil container spec", func(t *testing.T) {
		spec := swarm.ServiceSpec{}
		if got := sortedEnv(spec); got != "" {
			t.Errorf("sortedEnv() = %q, want empty", got)
		}
	})

	t.Run("sorts regardless of input order", func(t *testing.T) {
		a := specWithEnv([]string{"B=2", "A=1"})
		b := specWithEnv([]string{"A=1", "B=2"})
		if sortedEnv(a) != sortedEnv(b) {
			t.Errorf("sortedEnv differs for reordered but equal env: %q vs %q", sortedEnv(a), sortedEnv(b))
		}
		if want := "A=1,B=2"; sortedEnv(a) != want {
			t.Errorf("sortedEnv() = %q, want %q", sortedEnv(a), want)
		}
	})
}

func TestReplicasOf(t *testing.T) {
	t.Run("no mode set", func(t *testing.T) {
		if got := replicasOf(swarm.ServiceSpec{}); got != 0 {
			t.Errorf("replicasOf() = %d, want 0", got)
		}
	})

	t.Run("replicated mode", func(t *testing.T) {
		n := uint64(5)
		spec := swarm.ServiceSpec{Mode: swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &n}}}
		if got := replicasOf(spec); got != 5 {
			t.Errorf("replicasOf() = %d, want 5", got)
		}
	})

	t.Run("replicated mode with nil replicas pointer", func(t *testing.T) {
		spec := swarm.ServiceSpec{Mode: swarm.ServiceMode{Replicated: &swarm.ReplicatedService{}}}
		if got := replicasOf(spec); got != 0 {
			t.Errorf("replicasOf() = %d, want 0", got)
		}
	})
}

func TestMountsOf(t *testing.T) {
	t.Run("nil container spec", func(t *testing.T) {
		if got := mountsOf(swarm.ServiceSpec{}); got != nil {
			t.Errorf("mountsOf() = %v, want nil", got)
		}
	})

	t.Run("formats source:target", func(t *testing.T) {
		spec := swarm.ServiceSpec{
			TaskTemplate: swarm.TaskSpec{
				ContainerSpec: &swarm.ContainerSpec{
					Mounts: []mount.Mount{
						{Source: "data", Target: "/var/data"},
						{Source: "/host/logs", Target: "/logs"},
					},
				},
			},
		}
		got := mountsOf(spec)
		want := []string{"data:/var/data", "/host/logs:/logs"}
		if len(got) != len(want) {
			t.Fatalf("mountsOf() = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("mountsOf()[%d] = %q, want %q", i, got[i], want[i])
			}
		}
	})
}

func TestResourceSummary(t *testing.T) {
	t.Run("no resources", func(t *testing.T) {
		if got := resourceSummary(swarm.ServiceSpec{}); got != "" {
			t.Errorf("resourceSummary() = %q, want empty", got)
		}
	})

	t.Run("resources without limits", func(t *testing.T) {
		spec := swarm.ServiceSpec{TaskTemplate: swarm.TaskSpec{Resources: &swarm.ResourceRequirements{}}}
		if got := resourceSummary(spec); got != "" {
			t.Errorf("resourceSummary() = %q, want empty", got)
		}
	})

	t.Run("formats cpu and memory", func(t *testing.T) {
		spec := swarm.ServiceSpec{
			TaskTemplate: swarm.TaskSpec{
				Resources: &swarm.ResourceRequirements{
					Limits: &swarm.Limit{NanoCPUs: 1_500_000_000, MemoryBytes: 536870912},
				},
			},
		}
		want := "cpu=" + cpusToString(1_500_000_000) + " mem=" + bytesToString(536870912)
		if got := resourceSummary(spec); got != want {
			t.Errorf("resourceSummary() = %q, want %q", got, want)
		}
	})
}

func TestDiffServiceSpec(t *testing.T) {
	t.Run("no changes", func(t *testing.T) {
		old := baseSpec()
		new := baseSpec()
		got := diffServiceSpec(old, new)
		if len(got) != 0 {
			t.Fatalf("diffServiceSpec() = %v, want no changes", got)
		}
	})

	t.Run("image change", func(t *testing.T) {
		old := baseSpec()
		new := baseSpec()
		new.TaskTemplate.ContainerSpec.Image = "nginx:1.27"
		got := diffServiceSpec(old, new)
		want := "image: nginx:1.26 -> nginx:1.27"
		assertContains(t, got, want)
	})

	t.Run("digest-pinned image compares equal on tag", func(t *testing.T) {
		old := baseSpec()
		old.TaskTemplate.ContainerSpec.Image = "nginx:1.26@sha256:deadbeef"
		new := baseSpec()
		got := diffServiceSpec(old, new)
		for _, c := range got {
			if len(c) >= 5 && c[:5] == "image" {
				t.Fatalf("unexpected image change for digest-pinned equal tag: %v", got)
			}
		}
	})

	t.Run("environment change", func(t *testing.T) {
		old := baseSpec()
		new := baseSpec()
		new.TaskTemplate.ContainerSpec.Env = []string{"FOO=different"}
		got := diffServiceSpec(old, new)
		assertContains(t, got, "environment changed")
	})

	t.Run("replicas change", func(t *testing.T) {
		old := baseSpec()
		new := baseSpec()
		n := uint64(9)
		new.Mode.Replicated.Replicas = &n
		got := diffServiceSpec(old, new)
		assertContains(t, got, "replicas: 2 -> 9")
	})

	t.Run("mount count change", func(t *testing.T) {
		old := baseSpec()
		new := baseSpec()
		new.TaskTemplate.ContainerSpec.Mounts = append(new.TaskTemplate.ContainerSpec.Mounts,
			mount.Mount{Source: "extra", Target: "/extra"})
		got := diffServiceSpec(old, new)
		assertContains(t, got, "mounts: 1 -> 2")
	})

	t.Run("resources change", func(t *testing.T) {
		old := baseSpec()
		new := baseSpec()
		new.TaskTemplate.Resources.Limits = &swarm.Limit{NanoCPUs: 2_000_000_000, MemoryBytes: 1073741824}
		got := diffServiceSpec(old, new)
		wantOld := resourceSummary(old)
		wantNew := resourceSummary(new)
		assertContains(t, got, "resources: "+wantOld+" -> "+wantNew)
	})

	t.Run("resources added from none", func(t *testing.T) {
		old := swarm.ServiceSpec{TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{Image: "x"}}}
		new := old
		new.TaskTemplate.Resources = &swarm.ResourceRequirements{Limits: &swarm.Limit{NanoCPUs: 1_000_000_000}}
		got := diffServiceSpec(old, new)
		assertContains(t, got, "resources: none -> "+resourceSummary(new))
	})
}

func specWithEnv(env []string) swarm.ServiceSpec {
	return swarm.ServiceSpec{
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{Env: env},
		},
	}
}

func baseSpec() swarm.ServiceSpec {
	n := uint64(2)
	return swarm.ServiceSpec{
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{
				Image: "nginx:1.26",
				Env:   []string{"FOO=bar"},
				Mounts: []mount.Mount{
					{Source: "data", Target: "/data"},
				},
			},
			Resources: &swarm.ResourceRequirements{
				Limits: &swarm.Limit{NanoCPUs: 1_000_000_000, MemoryBytes: 268435456},
			},
		},
		Mode: swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &n}},
	}
}

func assertContains(t *testing.T, list []string, want string) {
	t.Helper()
	for _, v := range list {
		if v == want {
			return
		}
	}
	t.Fatalf("changes %v does not contain %q", list, want)
}
