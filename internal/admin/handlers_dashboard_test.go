package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/swarm"

	"swarmdash/internal/store"
	"swarmdash/internal/updatecheck"
)

func dashboardDockerMux(nodes []swarm.Node, services []swarm.Service, tasks []swarm.Task) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/nodes", jsonHandler(nodes))
	mux.HandleFunc("/services", jsonHandler(services))
	mux.HandleFunc("/tasks", jsonHandler(tasks))
	return mux
}

func TestHandleDashboard(t *testing.T) {
	nodes := []swarm.Node{
		{ID: "n1", Description: swarm.NodeDescription{Hostname: "node-a", Resources: swarm.Resources{NanoCPUs: 4_000_000_000, MemoryBytes: 8 << 30}}},
	}
	services := []swarm.Service{
		{
			ID: "s1",
			Spec: swarm.ServiceSpec{
				Annotations:  swarm.Annotations{Name: "web"},
				TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{Image: "nginx:latest"}},
			},
			ServiceStatus: &swarm.ServiceStatus{DesiredTasks: 2, RunningTasks: 2},
		},
	}
	tasks := []swarm.Task{
		{ID: "t1", Status: swarm.TaskStatus{State: swarm.TaskStateRunning}},
		{ID: "t2", Status: swarm.TaskStatus{State: swarm.TaskStateFailed}},
	}

	docker := newFakeDocker(t, dashboardDockerMux(nodes, services, tasks))
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleDashboard(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "node-a") {
		t.Errorf("body missing node hostname, got: %s", body)
	}
	if !strings.Contains(body, updatecheck.CurrentVersion) {
		t.Errorf("body missing CurrentVersion %q", updatecheck.CurrentVersion)
	}
}

func TestHandleDashboard_DockerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/nodes", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleDashboard(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestSumDesiredTasks(t *testing.T) {
	services := []swarm.Service{
		{ServiceStatus: &swarm.ServiceStatus{DesiredTasks: 3}},
		{ServiceStatus: &swarm.ServiceStatus{DesiredTasks: 2}},
		{ServiceStatus: nil},
	}
	if got := sumDesiredTasks(services); got != 5 {
		t.Errorf("sumDesiredTasks() = %d, want 5", got)
	}
}

func TestClusterCapacity(t *testing.T) {
	nodes := []swarm.Node{
		{Description: swarm.NodeDescription{Resources: swarm.Resources{NanoCPUs: 2_000_000_000, MemoryBytes: 1024}}},
		{Description: swarm.NodeDescription{Resources: swarm.Resources{NanoCPUs: 1_000_000_000, MemoryBytes: 2048}}},
	}
	cpu, mem := clusterCapacity(nodes)
	if cpu != 3_000_000_000 || mem != 3072 {
		t.Errorf("clusterCapacity() = (%d, %d), want (3000000000, 3072)", cpu, mem)
	}
}

func TestReservedResources(t *testing.T) {
	svc := func(desired uint64, reservedNanoCPUs int64) swarm.Service {
		return swarm.Service{
			ServiceStatus: &swarm.ServiceStatus{DesiredTasks: desired},
			Spec: swarm.ServiceSpec{
				TaskTemplate: swarm.TaskSpec{
					Resources: &swarm.ResourceRequirements{
						Reservations: &swarm.Resources{NanoCPUs: reservedNanoCPUs},
					},
				},
			},
		}
	}
	services := []swarm.Service{
		svc(2, 500_000_000),
		{ServiceStatus: &swarm.ServiceStatus{DesiredTasks: 0}}, // desired 0: skipped
		{ServiceStatus: &swarm.ServiceStatus{DesiredTasks: 1}}, // no resources: skipped
	}
	if got := reservedResources(services); got != 1_000_000_000 {
		t.Errorf("reservedResources() = %d, want 1000000000", got)
	}
}

func TestPct(t *testing.T) {
	tests := []struct {
		part, total int64
		want        float64
	}{
		{0, 0, 0},
		{50, 100, 50},
		{1, 3, 33.33333333333333},
	}
	for _, tt := range tests {
		if got := pct(tt.part, tt.total); got != tt.want {
			t.Errorf("pct(%d, %d) = %v, want %v", tt.part, tt.total, got, tt.want)
		}
	}
}

func TestClusterHistoryJSON(t *testing.T) {
	samples := []store.ClusterSample{
		{RunningTasks: 3, DesiredTasks: 4, CPUReservedPct: 12.5, MemUsedPct: 40},
	}
	got := string(clusterHistoryJSON(samples))
	if !strings.Contains(got, `"run":3`) || !strings.Contains(got, `"des":4`) {
		t.Errorf("clusterHistoryJSON() = %s, missing expected fields", got)
	}
}
