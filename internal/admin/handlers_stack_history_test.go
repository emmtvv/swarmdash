package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/swarm"
)

const historyComposeV1 = `services:
  web:
    image: nginx:${TAG}
  worker:
    image: busybox
    command: sleep 1000
`

const historyComposeV2 = `services:
  web:
    image: nginx:${TAG}
    deploy:
      replicas: 2
`

func deploy(t *testing.T, s *Server, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleStackDeploySubmit(w, postForm("/stacks/deploy", form))
	return w
}

func TestStackDeploy_RecordsHistoryWithEncryptedVars(t *testing.T) {
	fs := newFakeSwarm()
	s := newTestServer(t, fs.client(t))
	s.cfg.ClusterSecret = "test-cluster-secret"

	form := url.Values{"name": {"app"}, "compose": {historyComposeV1}, "vars": {"TAG=1.27"}}
	if w := deploy(t, s, form); w.Code != http.StatusSeeOther {
		t.Fatalf("deploy: status %d: %s", w.Code, w.Body.String())
	}
	if img := fs.service("app_web").Spec.TaskTemplate.ContainerSpec.Image; img != "nginx:1.27" {
		t.Errorf("image = %q, want variable substituted", img)
	}
	if args := fs.service("app_worker").Spec.TaskTemplate.ContainerSpec.Args; strings.Join(args, " ") != "sleep 1000" {
		t.Errorf("worker args = %v (compose command: must map to Args)", args)
	}

	// Re-deploying the identical file + vars doesn't add a version.
	deploy(t, s, form)
	versions, _ := s.store.ListStackVersions("app")
	if len(versions) != 1 {
		t.Fatalf("versions = %d, want 1", len(versions))
	}
	v := versions[0]
	if v.Version != 1 || v.Compose != historyComposeV1 || v.Source != "ui" || v.CreatedBy != testAdmin.Username {
		t.Errorf("version = %+v", v)
	}
	if len(v.VarsEnc) == 0 || strings.Contains(string(v.VarsEnc), "1.27") {
		t.Errorf("vars must be stored encrypted, got %q", v.VarsEnc)
	}
	if vars, err := s.decryptSecret(v.VarsEnc); err != nil || vars != "TAG=1.27" {
		t.Errorf("decrypted vars = %q, %v", vars, err)
	}
}

func TestStackDeploy_PruneRemovesOrphans(t *testing.T) {
	fs := newFakeSwarm()
	s := newTestServer(t, fs.client(t))

	deploy(t, s, url.Values{"name": {"app"}, "compose": {historyComposeV1}, "vars": {"TAG=1"}})

	// Without prune, worker survives a file that no longer has it.
	deploy(t, s, url.Values{"name": {"app"}, "compose": {historyComposeV2}, "vars": {"TAG=1"}})
	if fs.service("app_worker") == nil {
		t.Fatal("worker removed without prune")
	}

	// Preview reports it as an orphan.
	w := httptest.NewRecorder()
	s.handleStackDeployPreview(w, postForm("/stacks/deploy/preview", url.Values{"name": {"app"}, "compose": {historyComposeV2}, "vars": {"TAG=1"}, "prune": {"on"}}))
	if !strings.Contains(w.Body.String(), "worker") || !strings.Contains(w.Body.String(), ">remove<") {
		t.Errorf("preview should list worker for removal, got: %s", w.Body.String())
	}

	deploy(t, s, url.Values{"name": {"app"}, "compose": {historyComposeV2}, "vars": {"TAG=1"}, "prune": {"on"}})
	if fs.service("app_worker") != nil {
		t.Fatal("worker not removed with prune")
	}
	if fs.service("app_web") == nil {
		t.Fatal("web removed by prune")
	}
}

func TestStackRollback(t *testing.T) {
	fs := newFakeSwarm()
	s := newTestServer(t, fs.client(t))
	s.cfg.ClusterSecret = "test-cluster-secret"

	deploy(t, s, url.Values{"name": {"app"}, "compose": {historyComposeV1}, "vars": {"TAG=1"}})
	deploy(t, s, url.Values{"name": {"app"}, "compose": {historyComposeV2}, "vars": {"TAG=2"}, "prune": {"on"}})
	if fs.service("app_worker") != nil {
		t.Fatal("setup: worker should be gone after v2")
	}

	versions, _ := s.store.ListStackVersions("app")
	v1 := versions[1]
	w := httptest.NewRecorder()
	s.handleStackRollback(w, postForm("/stacks/app/versions/"+v1.ID+"/rollback", nil, "name", "app", "id", v1.ID))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("rollback: status %d: %s", w.Code, w.Body.String())
	}
	if fs.service("app_worker") == nil {
		t.Error("rollback to v1 should recreate worker")
	}
	if img := fs.service("app_web").Spec.TaskTemplate.ContainerSpec.Image; img != "nginx:1" {
		t.Errorf("image = %q, want v1's variables (nginx:1)", img)
	}

	versions, _ = s.store.ListStackVersions("app")
	if len(versions) != 3 || versions[0].Source != "rollback" || versions[0].Note != "rollback to v1" {
		t.Errorf("history after rollback = %+v", versions)
	}

	// A version ID from another stack is rejected.
	w = httptest.NewRecorder()
	s.handleStackRollback(w, postForm("/x", nil, "name", "other", "id", v1.ID))
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-stack rollback status = %d, want 404", w.Code)
	}
}

func TestStackDeployPage_EditLoadsStoredVersion(t *testing.T) {
	fs := newFakeSwarm()
	s := newTestServer(t, fs.client(t))
	s.cfg.ClusterSecret = "test-cluster-secret"
	deploy(t, s, url.Values{"name": {"app"}, "compose": {historyComposeV1}, "vars": {"TAG=edit-me"}})

	w := httptest.NewRecorder()
	s.handleStackDeployPage(w, withUser(httptest.NewRequest(http.MethodGet, "/stacks/deploy?stack=app", nil), testAdmin))
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "Edit stack app") || !strings.Contains(body, "TAG=edit-me") || !strings.Contains(body, "busybox") {
		t.Errorf("edit page should load v1 with its variables, got %d: %s", w.Code, body)
	}
}

func TestStackDeployPage_EditFallsBackToExport(t *testing.T) {
	fs := newFakeSwarm()
	fs.networks = append(fs.networks, swarmNetwork("net1", "legacy_front", "legacy"))
	fs.addSecret("api_key")
	fs.addService(swarm.ServiceSpec{
		Annotations: swarm.Annotations{Name: "legacy_api", Labels: map[string]string{stackLabel: "legacy"}},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{
				Image:   "me/api:3",
				Secrets: []*swarm.SecretReference{{SecretName: "api_key", File: &swarm.SecretReferenceFileTarget{Name: "api_key"}}},
			},
			Networks: []swarm.NetworkAttachmentConfig{{Target: "net1"}},
		},
		Mode: swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: uint64Ptr(1)}},
	})
	s := newTestServer(t, fs.client(t))

	w := httptest.NewRecorder()
	s.handleStackDeployPage(w, withUser(httptest.NewRequest(http.MethodGet, "/stacks/deploy?stack=legacy", nil), testAdmin))
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "generated from its running services") {
		t.Fatalf("expected export fallback notice, got %d: %s", w.Code, body)
	}

	// The generated file must deploy back unchanged: networks by key,
	// secrets declared at the top level.
	yml, err := s.exportStackYAML(withUser(httptest.NewRequest(http.MethodGet, "/", nil), testAdmin), "legacy")
	if err != nil {
		t.Fatal(err)
	}
	wd := httptest.NewRecorder()
	s.handleStackDeployPreview(wd, postForm("/stacks/deploy/preview", url.Values{"name": {"legacy"}, "compose": {string(yml)}}))
	if b := wd.Body.String(); strings.Contains(b, "error-box") || !strings.Contains(b, ">unchanged<") {
		t.Errorf("exported compose should preview as unchanged:\n%s\n---\n%s", yml, b)
	}
}

func TestStackDeploy_WarningsShownInPreview(t *testing.T) {
	s := newTestServer(t, newFakeSwarm().client(t))
	w := httptest.NewRecorder()
	s.handleStackDeployPreview(w, postForm("/stacks/deploy/preview", url.Values{
		"name":    {"app"},
		"compose": {"services:\n  web:\n    image: nginx\n    build: .\n    environment:\n      X: ${UNSET}\n"},
	}))
	body := w.Body.String()
	if !strings.Contains(body, "services.web.build") || !strings.Contains(body, "UNSET") {
		t.Errorf("preview should show warnings, got: %s", body)
	}
}

func TestTemplates_SaveUseDelete(t *testing.T) {
	s := newTestServer(t, nil)

	w := httptest.NewRecorder()
	s.handleTemplateSave(w, postForm("/stacks/templates", url.Values{
		"template_name": {"My App"}, "template_description": {"mine"}, "compose": {historyComposeV1}, "vars": {"TAG=x"},
	}))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("save: status %d: %s", w.Code, w.Body.String())
	}
	// Same name replaces rather than duplicates.
	s.handleTemplateSave(httptest.NewRecorder(), postForm("/stacks/templates", url.Values{
		"template_name": {"my app"}, "compose": {historyComposeV2},
	}))
	tpls, _ := s.store.ListStackTemplates()
	if len(tpls) != 1 || tpls[0].Compose != historyComposeV2 {
		t.Fatalf("templates = %+v, want one, replaced", tpls)
	}

	w = httptest.NewRecorder()
	s.handleTemplatesPage(w, withUser(httptest.NewRequest(http.MethodGet, "/stacks/templates", nil), testAdmin))
	if !strings.Contains(w.Body.String(), "Your templates") || !strings.Contains(w.Body.String(), "PostgreSQL") {
		t.Errorf("gallery should list custom and built-in templates")
	}

	w = httptest.NewRecorder()
	s.handleStackDeployPage(w, withUser(httptest.NewRequest(http.MethodGet, "/stacks/deploy?template="+tpls[0].ID, nil), testAdmin))
	if !strings.Contains(w.Body.String(), `value="my-app"`) || !strings.Contains(w.Body.String(), "replicas: 2") {
		t.Errorf("using a custom template should prefill the form, got: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	s.handleTemplateDelete(w, postForm("/x", nil, "id", tpls[0].ID))
	if tpls, _ := s.store.ListStackTemplates(); w.Code != http.StatusSeeOther || len(tpls) != 0 {
		t.Errorf("delete: status %d, remaining %d", w.Code, len(tpls))
	}
}

func TestStackNameSuggestion(t *testing.T) {
	for in, want := range map[string]string{"PostgreSQL": "postgresql", "My App 2": "my-app-2", "  ..x!!y ": "x-y"} {
		if got := stackNameSuggestion(in); got != want {
			t.Errorf("stackNameSuggestion(%q) = %q, want %q", in, got, want)
		}
	}
}
