package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/swarm"
)

func TestHandleServiceCreate(t *testing.T) {
	fs := newFakeSwarm()
	fs.addSecret("db_pw")
	s := newTestServer(t, fs.client(t))

	w := httptest.NewRecorder()
	s.handleServiceCreate(w, postForm("/services", url.Values{
		"name":              {"api"},
		"stack":             {"shop"},
		"image":             {"me/api:1.2"},
		"mode":              {"replicated"},
		"replicas":          {"3"},
		"env":               {"A=1\nB=2"},
		"command":           {"serve\n--port=80"},
		"ports":             {"8080:80"},
		"secrets":           {"db_pw:password"},
		"health_test":       {"curl -f localhost"},
		"health_interval":   {"10s"},
		"restart_condition": {"on-failure"},
		"update_order":      {"start-first"},
	}))

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "/services/shop_api" {
		t.Errorf("Location = %q, want /services/shop_api", loc)
	}
	svc := fs.service("shop_api")
	if svc == nil {
		t.Fatal("service shop_api was not created")
	}
	cs := svc.Spec.TaskTemplate.ContainerSpec
	if cs.Image != "me/api:1.2" || svc.Spec.Labels[stackLabel] != "shop" {
		t.Errorf("image/labels = %q / %v", cs.Image, svc.Spec.Labels)
	}
	if svc.Spec.Mode.Replicated == nil || *svc.Spec.Mode.Replicated.Replicas != 3 {
		t.Errorf("mode = %+v", svc.Spec.Mode)
	}
	if !reflect.DeepEqual(cs.Env, []string{"A=1", "B=2"}) || !reflect.DeepEqual(cs.Args, []string{"serve", "--port=80"}) {
		t.Errorf("env/args = %v / %v", cs.Env, cs.Args)
	}
	if len(cs.Secrets) != 1 || cs.Secrets[0].SecretName != "db_pw" || cs.Secrets[0].File.Name != "password" {
		t.Errorf("secrets = %+v", cs.Secrets)
	}
	if cs.Healthcheck == nil || cs.Healthcheck.Test[1] != "curl -f localhost" {
		t.Errorf("healthcheck = %+v", cs.Healthcheck)
	}
	if svc.Spec.EndpointSpec == nil || svc.Spec.EndpointSpec.Ports[0].PublishedPort != 8080 {
		t.Errorf("endpoint = %+v", svc.Spec.EndpointSpec)
	}
	if svc.Spec.UpdateConfig.Order != "start-first" || svc.Spec.TaskTemplate.RestartPolicy.Condition != "on-failure" {
		t.Errorf("update/restart = %+v / %+v", svc.Spec.UpdateConfig, svc.Spec.TaskTemplate.RestartPolicy)
	}

	entries, _ := s.store.ListAudit(0, 10)
	if len(entries) != 1 || entries[0].Action != "service.create" || entries[0].Target != "shop_api" {
		t.Errorf("audit = %+v", entries)
	}
}

func TestHandleServiceCreate_ErrorKeepsInput(t *testing.T) {
	fs := newFakeSwarm()
	s := newTestServer(t, fs.client(t))

	w := httptest.NewRecorder()
	s.handleServiceCreate(w, postForm("/services", url.Values{
		"name":    {"api"},
		"image":   {"me/api:1"},
		"env":     {"KEEP_ME=1"},
		"secrets": {"does-not-exist"},
	}))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (form re-rendered)", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "does-not-exist") || !strings.Contains(body, "not found") || !strings.Contains(body, "KEEP_ME=1") {
		t.Errorf("re-rendered form should show the error and keep input, got: %s", body)
	}
	if len(fs.services) != 0 {
		t.Errorf("no service should have been created")
	}
}

func TestHandleServiceCreate_Validation(t *testing.T) {
	s := newTestServer(t, newFakeSwarm().client(t))
	for name, form := range map[string]url.Values{
		"bad name":      {"name": {"-x"}, "image": {"nginx"}},
		"missing image": {"name": {"x"}},
		"bad replicas":  {"name": {"x"}, "image": {"nginx"}, "replicas": {"many"}},
	} {
		w := httptest.NewRecorder()
		s.handleServiceCreate(w, postForm("/services", form))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "error-box") {
			t.Errorf("%s: status = %d, want re-rendered form with error", name, w.Code)
		}
	}
}

func TestHandleServiceNewPage_Clone(t *testing.T) {
	fs := newFakeSwarm()
	replicas := uint64(4)
	fs.addService(swarm.ServiceSpec{
		Annotations: swarm.Annotations{Name: "shop_web", Labels: map[string]string{stackLabel: "shop"}},
		TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{
			Image: "nginx:1.27@sha256:abc",
			Env:   []string{"MODE=prod"},
		}},
		Mode:         swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &replicas}},
		EndpointSpec: &swarm.EndpointSpec{Ports: []swarm.PortConfig{{PublishedPort: 80, TargetPort: 80}}},
	})
	s := newTestServer(t, fs.client(t))

	w := httptest.NewRecorder()
	s.handleServiceNewPage(w, withUser(httptest.NewRequest(http.MethodGet, "/services/new?from=shop_web", nil), testAdmin))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{`value="web-copy"`, `value="shop"`, `value="nginx:1.27"`, `value="4"`, "MODE=prod"} {
		if !strings.Contains(body, want) {
			t.Errorf("clone form missing %s", want)
		}
	}
	if strings.Contains(body, "80:80") {
		t.Error("clone form should not carry over published ports")
	}
}
