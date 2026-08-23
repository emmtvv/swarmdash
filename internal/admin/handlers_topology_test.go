package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/swarm"
)

func g3TopologyMux(nets []network.Summary, services []swarm.Service) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/networks", jsonHandler(nets))
	mux.HandleFunc("/services", jsonHandler(services))
	return mux
}

func TestHandleTopologyPage(t *testing.T) {
	nets := []network.Summary{
		{ID: "net1", Name: "backend", Driver: "overlay"},
	}
	services := []swarm.Service{
		{
			ID: "s1",
			Spec: swarm.ServiceSpec{
				Annotations:  swarm.Annotations{Name: "web"},
				TaskTemplate: swarm.TaskSpec{Networks: []swarm.NetworkAttachmentConfig{{Target: "net1"}}},
			},
			ServiceStatus: &swarm.ServiceStatus{RunningTasks: 1, DesiredTasks: 1},
		},
		{
			ID:   "s2",
			Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "standalone"}},
		},
	}

	docker := newFakeDocker(t, g3TopologyMux(nets, services))
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/topology", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleTopologyPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "backend") {
		t.Errorf("body missing network name, got: %s", body)
	}
	if !strings.Contains(body, "standalone") {
		t.Errorf("body missing isolated service name, got: %s", body)
	}
}

func TestHandleTopologyPage_NoServices(t *testing.T) {
	docker := newFakeDocker(t, g3TopologyMux(nil, nil))
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/topology", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleTopologyPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "No services are attached") {
		t.Errorf("body missing empty-graph message, got: %s", w.Body.String())
	}
}

func TestHandleTopologyPage_NetworkListError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/networks", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/topology", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleTopologyPage(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleTopologyPage_ServiceListError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/networks", jsonHandler([]network.Summary{}))
	mux.HandleFunc("/services", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	r := withUser(httptest.NewRequest(http.MethodGet, "/topology", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleTopologyPage(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestBuildTopologyGraph(t *testing.T) {
	nets := []network.Summary{
		{ID: "net1", Name: "backend", Driver: "overlay"},
		{ID: "net2", Name: "unused-overlay", Driver: "overlay"},
		{ID: "net3", Name: "bridge0", Driver: "bridge"},
	}
	services := []swarm.Service{
		{
			ID:   "s1",
			Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "web"}, TaskTemplate: swarm.TaskSpec{Networks: []swarm.NetworkAttachmentConfig{{Target: "net1"}}}},
		},
		{
			ID:   "s2",
			Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "isolated-b"}},
		},
		{
			ID:   "s3",
			Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "isolated-a"}, TaskTemplate: swarm.TaskSpec{Networks: []swarm.NetworkAttachmentConfig{{Target: "net3"}}}},
		},
	}

	graph, isolated := buildTopologyGraph(nets, services)

	// graph.Nodes carries every service (whether or not it attaches to an
	// overlay network) plus only the overlay networks actually used: 3
	// services + net1 (net2 is an unused overlay, net3 isn't overlay at all).
	if len(graph.Nodes) != 4 {
		t.Fatalf("graph.Nodes = %d, want 4: %+v", len(graph.Nodes), graph.Nodes)
	}
	if len(graph.Edges) != 1 {
		t.Fatalf("graph.Edges = %d, want 1: %+v", len(graph.Edges), graph.Edges)
	}
	if graph.Edges[0].Source != "svc:s1" || graph.Edges[0].Target != "net:net1" {
		t.Errorf("unexpected edge: %+v", graph.Edges[0])
	}

	if len(isolated) != 2 {
		t.Fatalf("isolated = %d, want 2: %+v", len(isolated), isolated)
	}
	// buildTopologyGraph sorts isolated services by name.
	if isolated[0].Spec.Name != "isolated-a" || isolated[1].Spec.Name != "isolated-b" {
		t.Errorf("isolated not sorted by name: %+v", isolated)
	}
}

func TestServiceOverlayNetworkIDs(t *testing.T) {
	svc := swarm.Service{
		Spec: swarm.ServiceSpec{
			TaskTemplate: swarm.TaskSpec{Networks: []swarm.NetworkAttachmentConfig{{Target: "net1"}}},
			//nolint:staticcheck // exercising the deprecated fallback on purpose
			Networks: []swarm.NetworkAttachmentConfig{{Target: "net1"}, {Target: "net2"}},
		},
	}
	got := serviceOverlayNetworkIDs(svc)
	want := []string{"net1", "net2"}
	if len(got) != len(want) {
		t.Fatalf("serviceOverlayNetworkIDs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("serviceOverlayNetworkIDs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
