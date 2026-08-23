package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/swarm"
)

func g5ServiceSpecService() swarm.Service {
	return swarm.Service{
		ID:   "svc1",
		Meta: swarm.Meta{Version: swarm.Version{Index: 3}},
		Spec: swarm.ServiceSpec{
			Annotations:  swarm.Annotations{Name: "web"},
			TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{Image: "nginx:latest"}},
		},
	}
}

// g5ServiceSpecMux registers GET/POST /services/svc1(/update) plus (only
// when the caller needs them) /networks, /secrets, /configs lookups.
func g5ServiceSpecMux(svc swarm.Service, gotSpec *swarm.ServiceSpec) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services/svc1", jsonHandler(svc))
	mux.HandleFunc("POST /services/svc1/update", func(w http.ResponseWriter, r *http.Request) {
		if gotSpec != nil {
			_ = json.NewDecoder(r.Body).Decode(gotSpec)
		}
		jsonHandler(swarm.ServiceUpdateResponse{})(w, r)
	})
	return mux
}

// g5ServiceSpecForm returns a minimal valid form for POST /services/{name}/spec
// that doesn't touch networks/secrets/configs, so callers testing those
// specifically only need to override one field.
func g5ServiceSpecForm() url.Values {
	return url.Values{
		"env":                   {"FOO=bar"},
		"labels":                {"team=infra"},
		"entrypoint":            {"/bin/sh"},
		"command":               {"-c\necho hi"},
		"mounts":                {"data:/var/data"},
		"ports":                 {"8080:80"},
		"restart_condition":     {"any"},
		"update_parallelism":    {"1"},
		"update_delay":          {"10s"},
		"update_order":          {"stop-first"},
		"update_failure_action": {"pause"},
	}
}

func TestHandleServiceUpdateSpec(t *testing.T) {
	svc := g5ServiceSpecService()
	var gotSpec swarm.ServiceSpec
	docker := newFakeDocker(t, g5ServiceSpecMux(svc, &gotSpec))
	s := newTestServer(t, docker)

	form := g5ServiceSpecForm()
	r := withUser(httptest.NewRequest(http.MethodPost, "/services/svc1/spec", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("name", "svc1")
	w := httptest.NewRecorder()
	s.handleServiceUpdateSpec(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "/services/svc1" {
		t.Errorf("Location = %q, want /services/svc1", loc)
	}
	if len(gotSpec.TaskTemplate.ContainerSpec.Env) != 1 || gotSpec.TaskTemplate.ContainerSpec.Env[0] != "FOO=bar" {
		t.Errorf("posted env = %v, want [FOO=bar]", gotSpec.TaskTemplate.ContainerSpec.Env)
	}
	if gotSpec.EndpointSpec == nil || len(gotSpec.EndpointSpec.Ports) != 1 || gotSpec.EndpointSpec.Ports[0].PublishedPort != 8080 {
		t.Errorf("posted ports = %+v, want 8080:80", gotSpec.EndpointSpec)
	}
	if gotSpec.UpdateConfig == nil || gotSpec.UpdateConfig.Parallelism != 1 {
		t.Errorf("posted update config = %+v", gotSpec.UpdateConfig)
	}

	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "service.update_spec" || entries[0].Target != "web" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleServiceUpdateSpec_ServiceNotFound(t *testing.T) {
	docker := newFakeDocker(t, http.NewServeMux())
	s := newTestServer(t, docker)

	form := g5ServiceSpecForm()
	r := withUser(httptest.NewRequest(http.MethodPost, "/services/missing/spec", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("name", "missing")
	w := httptest.NewRecorder()
	s.handleServiceUpdateSpec(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleServiceUpdateSpec_InvalidMounts(t *testing.T) {
	svc := g5ServiceSpecService()
	docker := newFakeDocker(t, g5ServiceSpecMux(svc, nil))
	s := newTestServer(t, docker)

	form := g5ServiceSpecForm()
	form.Set("mounts", "a:b:c:d:e")
	r := withUser(httptest.NewRequest(http.MethodPost, "/services/svc1/spec", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("name", "svc1")
	w := httptest.NewRecorder()
	s.handleServiceUpdateSpec(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleServiceUpdateSpec_NetworkNotFound(t *testing.T) {
	svc := g5ServiceSpecService()
	mux := g5ServiceSpecMux(svc, nil)
	mux.HandleFunc("GET /networks", jsonHandler([]any{}))
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	form := g5ServiceSpecForm()
	form.Set("networks", "ghost-net")
	r := withUser(httptest.NewRequest(http.MethodPost, "/services/svc1/spec", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("name", "svc1")
	w := httptest.NewRecorder()
	s.handleServiceUpdateSpec(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "ghost-net") {
		t.Errorf("body missing network name, got: %s", w.Body.String())
	}
}

func TestHandleServiceUpdateSpec_SecretNotFound(t *testing.T) {
	svc := g5ServiceSpecService()
	mux := g5ServiceSpecMux(svc, nil)
	mux.HandleFunc("GET /secrets", jsonHandler([]any{}))
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	form := g5ServiceSpecForm()
	form.Set("secrets", "ghost-secret")
	r := withUser(httptest.NewRequest(http.MethodPost, "/services/svc1/spec", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("name", "svc1")
	w := httptest.NewRecorder()
	s.handleServiceUpdateSpec(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleServiceUpdateSpec_ConfigNotFound(t *testing.T) {
	svc := g5ServiceSpecService()
	mux := g5ServiceSpecMux(svc, nil)
	mux.HandleFunc("GET /configs", jsonHandler([]any{}))
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	form := g5ServiceSpecForm()
	form.Set("configs", "ghost-config")
	r := withUser(httptest.NewRequest(http.MethodPost, "/services/svc1/spec", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("name", "svc1")
	w := httptest.NewRecorder()
	s.handleServiceUpdateSpec(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleServiceUpdateSpec_InvalidHealthcheck(t *testing.T) {
	svc := g5ServiceSpecService()
	docker := newFakeDocker(t, g5ServiceSpecMux(svc, nil))
	s := newTestServer(t, docker)

	form := g5ServiceSpecForm()
	form.Set("health_interval", "not-a-duration")
	r := withUser(httptest.NewRequest(http.MethodPost, "/services/svc1/spec", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("name", "svc1")
	w := httptest.NewRecorder()
	s.handleServiceUpdateSpec(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleServiceUpdateSpec_InvalidPorts(t *testing.T) {
	svc := g5ServiceSpecService()
	docker := newFakeDocker(t, g5ServiceSpecMux(svc, nil))
	s := newTestServer(t, docker)

	form := g5ServiceSpecForm()
	form.Set("ports", "not-a-port")
	r := withUser(httptest.NewRequest(http.MethodPost, "/services/svc1/spec", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("name", "svc1")
	w := httptest.NewRecorder()
	s.handleServiceUpdateSpec(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleServiceUpdateSpec_InvalidResources(t *testing.T) {
	svc := g5ServiceSpecService()
	docker := newFakeDocker(t, g5ServiceSpecMux(svc, nil))
	s := newTestServer(t, docker)

	form := g5ServiceSpecForm()
	form.Set("cpu_limit", "not-a-float")
	r := withUser(httptest.NewRequest(http.MethodPost, "/services/svc1/spec", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("name", "svc1")
	w := httptest.NewRecorder()
	s.handleServiceUpdateSpec(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleServiceUpdateSpec_DockerUpdateError(t *testing.T) {
	svc := g5ServiceSpecService()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services/svc1", jsonHandler(svc))
	mux.HandleFunc("POST /services/svc1/update", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	docker := newFakeDocker(t, mux)
	s := newTestServer(t, docker)

	form := g5ServiceSpecForm()
	r := withUser(httptest.NewRequest(http.MethodPost, "/services/svc1/spec", strings.NewReader(form.Encode())), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("name", "svc1")
	w := httptest.NewRecorder()
	s.handleServiceUpdateSpec(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", w.Code, w.Body.String())
	}
}

func TestParseHealthcheckForm(t *testing.T) {
	form := url.Values{
		"health_test":         {"curl -f http://localhost/"},
		"health_interval":     {"30s"},
		"health_timeout":      {"5s"},
		"health_start_period": {"10s"},
		"health_retries":      {"3"},
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatalf("ParseForm: %v", err)
	}
	hc, err := parseHealthcheckForm(r)
	if err != nil {
		t.Fatalf("parseHealthcheckForm: %v", err)
	}
	if len(hc.Test) != 2 || hc.Test[0] != "CMD-SHELL" || hc.Retries != 3 {
		t.Errorf("parseHealthcheckForm() = %+v, unexpected", hc)
	}
}

func TestParseHealthcheckForm_Disabled(t *testing.T) {
	form := url.Values{"health_disabled": {"on"}}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = r.ParseForm()
	hc, err := parseHealthcheckForm(r)
	if err != nil {
		t.Fatalf("parseHealthcheckForm: %v", err)
	}
	if len(hc.Test) != 1 || hc.Test[0] != "NONE" {
		t.Errorf("parseHealthcheckForm() disabled = %+v, want [NONE]", hc.Test)
	}
}

func TestParseHealthcheckForm_Blank(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	_ = r.ParseForm()
	hc, err := parseHealthcheckForm(r)
	if err != nil || hc != nil {
		t.Fatalf("parseHealthcheckForm() blank = %+v, %v, want nil, nil", hc, err)
	}
}

func TestParseServiceResourceForm(t *testing.T) {
	form := url.Values{"cpu_limit": {"1.5"}, "mem_limit": {"512m"}, "cpu_reservation": {"0.5"}, "mem_reservation": {"256m"}}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = r.ParseForm()
	res, err := parseServiceResourceForm(r)
	if err != nil {
		t.Fatalf("parseServiceResourceForm: %v", err)
	}
	if res.Limits.NanoCPUs != 1_500_000_000 || res.Reservations.NanoCPUs != 500_000_000 {
		t.Errorf("parseServiceResourceForm() = %+v, unexpected", res)
	}
}

func TestParseServiceResourceForm_Blank(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	_ = r.ParseForm()
	res, err := parseServiceResourceForm(r)
	if err != nil || res != nil {
		t.Fatalf("parseServiceResourceForm() blank = %+v, %v, want nil, nil", res, err)
	}
}

func TestJoinKVAndParseKVLines(t *testing.T) {
	m := map[string]string{"a": "1"}
	joined := joinKV(m)
	if joined != "a=1" {
		t.Fatalf("joinKV() = %q, want a=1", joined)
	}
	back := parseKVLines("a=1\nb=2\nbare")
	if back["a"] != "1" || back["b"] != "2" || back["bare"] != "" {
		t.Errorf("parseKVLines() = %v, unexpected", back)
	}
}

func TestFormatPorts(t *testing.T) {
	ports := []swarm.PortConfig{
		{PublishedPort: 8080, TargetPort: 80, Protocol: swarm.PortConfigProtocolTCP},
		{PublishedPort: 53, TargetPort: 53, Protocol: swarm.PortConfigProtocolUDP},
	}
	got := formatPorts(ports)
	want := "8080:80\n53:53/udp"
	if got != want {
		t.Errorf("formatPorts() = %q, want %q", got, want)
	}
}

func TestServiceEditorForm(t *testing.T) {
	svc := swarm.Service{
		Spec: swarm.ServiceSpec{
			TaskTemplate: swarm.TaskSpec{
				ContainerSpec: &swarm.ContainerSpec{
					Env:     []string{"FOO=bar"},
					Command: []string{"/bin/sh"},
					Args:    []string{"-c", "echo hi"},
					Mounts:  []mount.Mount{{Source: "data", Target: "/var/data", ReadOnly: true}},
				},
				Networks: []swarm.NetworkAttachmentConfig{{Target: "net1"}},
			},
			EndpointSpec: &swarm.EndpointSpec{Ports: []swarm.PortConfig{{PublishedPort: 80, TargetPort: 80}}},
		},
	}
	form := serviceEditorForm(svc, map[string]string{"net1": "overlay-net"})
	if form.Env != "FOO=bar" || form.Entrypoint != "/bin/sh" {
		t.Errorf("serviceEditorForm() = %+v, unexpected", form)
	}
	if !strings.Contains(form.Mounts, "ro") {
		t.Errorf("serviceEditorForm() mounts = %q, want ro suffix", form.Mounts)
	}
	if form.Networks != "overlay-net" {
		t.Errorf("serviceEditorForm() networks = %q, want overlay-net (resolved name)", form.Networks)
	}
	if form.RestartCondition != "any" || form.UpdateOrder != "stop-first" || form.UpdateFailure != "pause" {
		t.Errorf("serviceEditorForm() defaults = %+v, unexpected", form)
	}
}
