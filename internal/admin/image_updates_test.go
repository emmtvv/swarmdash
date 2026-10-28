package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/swarm"

	"swarmdash/internal/imageupdate"
	"swarmdash/internal/store"
)

// fakeRegistry serves one repository's tag list over TLS, with no auth,
// and returns the host to put in image references.
func fakeRegistry(t *testing.T, s *Server, tags ...string) string {
	t.Helper()
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tags": tags})
	}))
	t.Cleanup(ts.Close)
	s.registryClient = &imageupdate.Client{HTTP: ts.Client()}
	return strings.TrimPrefix(ts.URL, "https://")
}

func serviceWithImage(name, image string, labels map[string]string) swarm.ServiceSpec {
	return swarm.ServiceSpec{
		Annotations:  swarm.Annotations{Name: name, Labels: labels},
		TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{Image: image}},
	}
}

func TestImageUpdateOnce_AppliesPolicies(t *testing.T) {
	f := newFakeSwarm()
	s := newTestServer(t, f.client(t))
	reg := fakeRegistry(t, s, "latest", "1.0.0", "1.0.3", "1.2.0", "2.0.0")
	if err := s.store.PutAppSettings(store.AppSettings{ImageAutoUpdate: "major"}); err != nil {
		t.Fatal(err)
	}

	f.addService(serviceWithImage("minor", reg+"/app:1.0.0@sha256:0123456789012345678901234567890123456789012345678901234567890123", map[string]string{autoUpdateLabel: "minor"}))
	f.addService(serviceWithImage("patch", reg+"/app:1.0.0", map[string]string{autoUpdateLabel: "patch"}))
	f.addService(serviceWithImage("off", reg+"/app:1.0.0", map[string]string{autoUpdateLabel: "off"}))
	f.addService(serviceWithImage("default", reg+"/app:1.0.0", nil))
	f.addService(serviceWithImage("latest", reg+"/app:latest", nil))

	s.imageUpdateOnce(context.Background())

	want := map[string]string{
		"patch":   reg + "/app:1.0.3",
		"minor":   reg + "/app:1.2.0",
		"off":     reg + "/app:1.0.0",
		"default": reg + "/app:2.0.0",
		"latest":  reg + "/app:latest",
	}
	for name, image := range want {
		if got := f.service(name).Spec.TaskTemplate.ContainerSpec.Image; got != image {
			t.Errorf("%s: image = %q, want %q", name, got, image)
		}
	}

	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	updated := 0
	for _, e := range entries {
		if e.Action == "service_image_updated" && e.Username == "system" && e.Success {
			updated++
		}
	}
	if updated != 3 {
		t.Errorf("got %d service_image_updated audit entries, want 3", updated)
	}
}

func TestImageUpdateOnce_SkipsVersionRolledBackFrom(t *testing.T) {
	f := newFakeSwarm()
	s := newTestServer(t, f.client(t))
	reg := fakeRegistry(t, s, "1.0.0", "1.1.0")

	svc := f.addService(serviceWithImage("web", reg+"/app:1.0.0", map[string]string{autoUpdateLabel: "minor"}))
	prev := serviceWithImage("web", reg+"/app:1.1.0", nil)
	svc.PreviousSpec = &prev
	svc.UpdateStatus = &swarm.UpdateStatus{State: swarm.UpdateStateRollbackCompleted}

	s.imageUpdateOnce(context.Background())

	if got := f.service("web").Spec.TaskTemplate.ContainerSpec.Image; got != reg+"/app:1.0.0" {
		t.Errorf("image = %q, want it left at 1.0.0", got)
	}
}

func TestHandleServiceAutoUpdate_SetsAndClearsLabel(t *testing.T) {
	f := newFakeSwarm()
	s := newTestServer(t, f.client(t))
	f.addService(serviceWithImage("web", "nginx:1.27.1", map[string]string{"keep": "me"}))

	w := httptest.NewRecorder()
	s.handleServiceAutoUpdate(w, postForm("/services/web/autoupdate", url.Values{"policy": {"major"}}, "name", "web"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d; body: %s", w.Code, w.Body.String())
	}
	labels := f.service("web").Spec.Labels
	if labels[autoUpdateLabel] != "major" || labels["keep"] != "me" {
		t.Errorf("labels = %v, want autoupdate=major and keep=me", labels)
	}

	w = httptest.NewRecorder()
	s.handleServiceAutoUpdate(w, postForm("/services/web/autoupdate", url.Values{"policy": {""}}, "name", "web"))
	if _, ok := f.service("web").Spec.Labels[autoUpdateLabel]; ok {
		t.Error("label should be removed when the policy is reset to the cluster default")
	}

	w = httptest.NewRecorder()
	s.handleServiceAutoUpdate(w, postForm("/services/web/autoupdate", url.Values{"policy": {"weekly"}}, "name", "web"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid policy: status = %d, want 400", w.Code)
	}
}

func TestHandleServiceImageUpdates_OffersMinorAndMajor(t *testing.T) {
	f := newFakeSwarm()
	s := newTestServer(t, f.client(t))
	reg := fakeRegistry(t, s, "1.0.0", "1.0.3", "1.2.0", "2.0.0")
	f.addService(serviceWithImage("web", reg+"/app:1.0.0", nil))

	r := withUser(httptest.NewRequest(http.MethodGet, "/services/web/image-updates", nil), testViewer)
	r.SetPathValue("name", "web")
	w := httptest.NewRecorder()
	s.handleServiceImageUpdates(w, r)

	body := w.Body.String()
	for _, want := range []string{reg + "/app:1.0.3", reg + "/app:1.2.0", reg + "/app:2.0.0", "Cluster default (off)"} {
		if !strings.Contains(body, want) {
			t.Errorf("fragment missing %q:\n%s", want, body)
		}
	}
}

// TestHandleServiceImageUpdates_PagesStillRenderAfterwards guards against
// rendering the fragment from the shared template set itself: html/template
// can't Clone a set once it has executed, so every later full page failed.
func TestHandleServiceImageUpdates_PagesStillRenderAfterwards(t *testing.T) {
	f := newFakeSwarm()
	s := newTestServer(t, f.client(t))
	f.addService(serviceWithImage("web", "nginx:latest", nil))

	r := withUser(httptest.NewRequest(http.MethodGet, "/services/web/image-updates", nil), testAdmin)
	r.SetPathValue("name", "web")
	s.handleServiceImageUpdates(httptest.NewRecorder(), r)

	w := httptest.NewRecorder()
	s.handleGeneralSettingsPage(w, withUser(httptest.NewRequest(http.MethodGet, "/settings/general", nil), testAdmin))
	if w.Code != http.StatusOK {
		t.Fatalf("page after fragment: status = %d; body: %s", w.Code, w.Body.String())
	}
}
