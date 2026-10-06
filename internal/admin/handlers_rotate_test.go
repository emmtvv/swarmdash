package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/swarm"
)

func serviceWithSecret(name string, sec *swarm.Secret, target string) swarm.ServiceSpec {
	return swarm.ServiceSpec{
		Annotations: swarm.Annotations{Name: name},
		TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{
			Image:   "app",
			Secrets: []*swarm.SecretReference{{SecretID: sec.ID, SecretName: sec.Spec.Name, File: &swarm.SecretReferenceFileTarget{Name: target}}},
		}},
	}
}

func TestHandleSecretRotate(t *testing.T) {
	fs := newFakeSwarm()
	old := fs.addSecret("db_pw")
	unrelated := fs.addSecret("other")
	fs.addService(serviceWithSecret("api", old, "db_pw"))
	fs.addService(serviceWithSecret("worker", old, "password"))
	fs.addService(serviceWithSecret("cron", unrelated, "other"))
	s := newTestServer(t, fs.client(t))

	w := httptest.NewRecorder()
	s.handleSecretRotate(w, postForm("/secrets/"+old.ID+"/rotate", url.Values{"data": {"s3cret"}, "delete_old": {"on"}}, "id", old.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}

	for svc, target := range map[string]string{"api": "db_pw", "worker": "password"} {
		ref := fs.service(svc).Spec.TaskTemplate.ContainerSpec.Secrets[0]
		if ref.SecretName != "db_pw_v2" || ref.File.Name != target {
			t.Errorf("%s secret ref = %s -> %s, want db_pw_v2 -> %s (target path kept)", svc, ref.SecretName, ref.File.Name, target)
		}
	}
	if ref := fs.service("cron").Spec.TaskTemplate.ContainerSpec.Secrets[0]; ref.SecretName != "other" {
		t.Errorf("unrelated service was touched: %+v", ref)
	}
	if _, ok := fs.secrets[old.ID]; ok {
		t.Error("old secret should be deleted")
	}
	body := w.Body.String()
	if !strings.Contains(body, "db_pw_v2") || !strings.Contains(body, "Switched 2 services") || !strings.Contains(body, "Deleted") {
		t.Errorf("result page missing summary: %s", body)
	}
}

func TestHandleSecretRotate_KeepOldAndCustomName(t *testing.T) {
	fs := newFakeSwarm()
	old := fs.addSecret("token_v3")
	fs.addService(serviceWithSecret("api", old, "token"))
	s := newTestServer(t, fs.client(t))

	w := httptest.NewRecorder()
	s.handleSecretRotate(w, postForm("/x", url.Values{"data": {"x"}, "new_name": {"token-2026"}}, "id", old.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if got := fs.service("api").Spec.TaskTemplate.ContainerSpec.Secrets[0].SecretName; got != "token-2026" {
		t.Errorf("secret = %q, want token-2026", got)
	}
	if _, ok := fs.secrets[old.ID]; !ok {
		t.Error("old secret should be kept without delete_old")
	}
}

func TestHandleConfigRotate(t *testing.T) {
	fs := newFakeSwarm()
	old := fs.addConfig("nginx_conf", "server {}")
	fs.addService(swarm.ServiceSpec{
		Annotations: swarm.Annotations{Name: "web"},
		TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{
			Image:   "nginx",
			Configs: []*swarm.ConfigReference{{ConfigID: old.ID, ConfigName: "nginx_conf", File: &swarm.ConfigReferenceFileTarget{Name: "/etc/nginx/nginx.conf"}}},
		}},
	})
	s := newTestServer(t, fs.client(t))

	// Detail page shows the content.
	w := httptest.NewRecorder()
	r := withUser(httptest.NewRequest(http.MethodGet, "/configs/"+old.ID, nil), testAdmin)
	r.SetPathValue("id", old.ID)
	s.handleConfigDetail(w, r)
	if !strings.Contains(w.Body.String(), "server {}") || !strings.Contains(w.Body.String(), "/etc/nginx/nginx.conf") {
		t.Errorf("detail page should show content and mount path: %s", w.Body.String())
	}

	// Unchanged content is rejected.
	w = httptest.NewRecorder()
	s.handleConfigRotate(w, postForm("/x", url.Values{"data": {"server {}"}}, "id", old.ID))
	if w.Code != http.StatusBadRequest {
		t.Errorf("unchanged content: status = %d, want 400", w.Code)
	}

	w = httptest.NewRecorder()
	s.handleConfigRotate(w, postForm("/x", url.Values{"data": {"server { listen 81; }"}, "delete_old": {"on"}}, "id", old.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	ref := fs.service("web").Spec.TaskTemplate.ContainerSpec.Configs[0]
	if ref.ConfigName != "nginx_conf_v2" || ref.File.Name != "/etc/nginx/nginx.conf" {
		t.Errorf("config ref = %+v", ref)
	}
	if cfg := fs.configs[ref.ConfigID]; cfg == nil || string(cfg.Spec.Data) != "server { listen 81; }" {
		t.Errorf("new config = %+v", cfg)
	}
}

func TestSecretsPage_ShowsUsage(t *testing.T) {
	fs := newFakeSwarm()
	sec := fs.addSecret("db_pw")
	fs.addService(serviceWithSecret("api", sec, "db_pw"))
	s := newTestServer(t, fs.client(t))

	w := httptest.NewRecorder()
	s.handleSecretsPage(w, withUser(httptest.NewRequest(http.MethodGet, "/secrets", nil), testAdmin))
	if !strings.Contains(w.Body.String(), `<a href="/services/api">api</a>`) {
		t.Errorf("secrets page should link the service using it: %s", w.Body.String())
	}
}

func TestNextVersionName(t *testing.T) {
	taken := map[string]bool{"pw_v2": true}
	exists := func(n string) bool { return taken[n] }
	for in, want := range map[string]string{"pw": "pw_v3", "key": "key_v2", "key_v7": "key_v8", "a_v1x": "a_v1x_v2"} {
		if got := nextVersionName(in, exists); got != want {
			t.Errorf("nextVersionName(%q) = %q, want %q", in, got, want)
		}
	}
}
