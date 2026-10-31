package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/swarm"
	"golang.org/x/crypto/bcrypt"

	"swarmdash/internal/store"
	"swarmdash/internal/totp"
)

// browser drives the full admin Handler() the way a real browser would:
// cookie jar, CSRF token scraped from the page's <meta> tag, no redirect
// following (so each test step can assert where it was sent).
type browser struct {
	t    *testing.T
	base string
	c    *http.Client
	csrf string
	lang string
}

var csrfMetaRE = regexp.MustCompile(`<meta name="csrf-token" content="([^"]+)">`)

func newBrowser(t *testing.T, s *Server, lang string) *browser {
	t.Helper()
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, base: ts.URL, lang: lang, c: &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (b *browser) do(method, path string, body io.Reader, hdr map[string]string) (*http.Response, string) {
	b.t.Helper()
	req, err := http.NewRequest(method, b.base+path, body)
	if err != nil {
		b.t.Fatal(err)
	}
	req.Header.Set("Accept-Language", b.lang)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if m := csrfMetaRE.FindSubmatch(raw); m != nil {
		b.csrf = string(m[1])
	}
	return resp, string(raw)
}

func (b *browser) get(path string) (*http.Response, string) {
	return b.do(http.MethodGet, path, nil, nil)
}

func (b *browser) post(path string, form url.Values) (*http.Response, string) {
	b.t.Helper()
	if b.csrf == "" {
		b.get("/login")
	}
	form.Set("csrf_token", b.csrf)
	return b.do(http.MethodPost, path, strings.NewReader(form.Encode()), map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
}

func wantRedirect(t *testing.T, resp *http.Response, prefix string) {
	t.Helper()
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), prefix) {
		t.Fatalf("got %d -> %q, want redirect to %s", resp.StatusCode, resp.Header.Get("Location"), prefix)
	}
}

func wantBody(t *testing.T, resp *http.Response, body string, contains ...string) {
	t.Helper()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, body: %.500s", resp.StatusCode, body)
	}
	for _, c := range contains {
		if !strings.Contains(body, c) {
			t.Errorf("body missing %q", c)
		}
	}
}

// fakeVolumeAgent is just enough of an agent for the usage sampler and the
// volume backup pages.
type fakeVolumeAgent struct {
	mu       sync.Mutex
	uploaded []byte
	started  []string
}

func (a *fakeVolumeAgent) mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/stats/summary", jsonHandler(map[string]any{
		"mem_used_bytes": 300 << 20, "cpu_used_cores": 0.5,
		"tasks": []map[string]any{
			{"task_id": "t1", "service_name": "web", "mem_bytes": 100 << 20, "mem_limit": 1 << 30, "cpu_cores": 0.25},
			{"task_id": "t2", "service_name": "web", "mem_bytes": 200 << 20, "mem_limit": 1 << 30, "cpu_cores": 0.25},
		},
	}))
	mux.HandleFunc("GET /v1/volumes", jsonHandler([]map[string]any{{"Name": "data", "Driver": "local"}}))
	mux.HandleFunc("GET /v1/volume-backups", jsonHandler(map[string]any{
		"enabled": true,
		"backups": []map[string]any{{"id": "data-20261031-120000-abcdef", "volume": "data", "source": "backup", "status": "done", "size_bytes": 4096, "created_at": time.Now()}},
	}))
	mux.HandleFunc("POST /v1/volumes/{name}/backup", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.started = append(a.started, r.PathValue("name"))
		a.mu.Unlock()
		writeJSON(w, map[string]any{"id": "x", "status": "queued"})
	})
	mux.HandleFunc("POST /v1/volume-backups/upload", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		a.mu.Lock()
		a.uploaded = b
		a.mu.Unlock()
		writeJSON(w, map[string]any{"id": "y", "status": "done"})
	})
	mux.HandleFunc("GET /v1/volume-backups/{id}/download", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="`+r.PathValue("id")+`.tar.gz"`)
		_, _ = w.Write([]byte("ARCHIVE-BYTES"))
	})
	return mux
}

func TestFeaturesEndToEnd(t *testing.T) {
	agent := &fakeVolumeAgent{}
	agentAddr, agentPort := newFakeAgent(t, agent.mux())

	nodes := []swarm.Node{{
		ID:          "n1",
		Description: swarm.NodeDescription{Hostname: "node-a", Resources: swarm.Resources{NanoCPUs: 4e9, MemoryBytes: 8 << 30}},
		Status:      swarm.NodeStatus{State: swarm.NodeStateReady, Addr: agentAddr},
		Spec:        swarm.NodeSpec{Role: swarm.NodeRoleManager, Availability: swarm.NodeAvailabilityActive},
	}}
	replicas := uint64(2)
	services := []swarm.Service{{
		ID: "s1",
		Spec: swarm.ServiceSpec{
			Annotations:  swarm.Annotations{Name: "web", Labels: map[string]string{stackLabel: "shop"}},
			TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{Image: "nginx:1.27"}},
			Mode:         swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &replicas}},
		},
		ServiceStatus: &swarm.ServiceStatus{DesiredTasks: 2, RunningTasks: 2},
	}}
	tasks := []swarm.Task{
		{ID: "t1", ServiceID: "s1", NodeID: "n1", Status: swarm.TaskStatus{State: swarm.TaskStateRunning}, DesiredState: swarm.TaskStateRunning},
		{ID: "t2", ServiceID: "s1", NodeID: "n1", Status: swarm.TaskStatus{State: swarm.TaskStateRunning}, DesiredState: swarm.TaskStateRunning},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /nodes", jsonHandler(nodes))
	mux.HandleFunc("GET /nodes/{id}", jsonHandler(nodes[0]))
	mux.HandleFunc("GET /services", jsonHandler(services))
	mux.HandleFunc("GET /services/{id}", jsonHandler(services[0]))
	mux.HandleFunc("GET /tasks", jsonHandler(tasks))
	mux.HandleFunc("GET /networks", jsonHandler([]network.Summary{}))
	mux.HandleFunc("GET /secrets", jsonHandler([]swarm.Secret{{ID: "sec1", Spec: swarm.SecretSpec{Annotations: swarm.Annotations{Name: "web_db_password"}}}}))
	mux.HandleFunc("GET /configs", jsonHandler([]swarm.Config{}))

	s := newTestServer(t, newFakeDocker(t, mux))
	s.cfg.ClusterSecret = "e2e-cluster-secret"
	s.cfg.AgentPort = agentPort
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw-123456"), bcrypt.MinCost)
	if err := s.store.PutUser(store.User{Username: "alice", PasswordHash: string(hash), Role: "admin", AuthSource: "local", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	b := newBrowser(t, s, "ru-RU,ru;q=0.9")

	// --- The browser's Accept-Language is ignored: English until someone
	// picks a language in the UI, here on the login page.
	resp, body := b.get("/login")
	wantBody(t, resp, body, `<html lang="en">`, "Log in", `name="lang"`)
	resp, _ = b.post("/language", url.Values{"lang": {"ru"}, "return": {"/login"}})
	wantRedirect(t, resp, "/login")
	resp, body = b.get("/login")
	wantBody(t, resp, body, `<html lang="ru">`, "Войти")

	resp, _ = b.post("/login", url.Values{"username": {"alice"}, "password": {"pw-123456"}})
	wantRedirect(t, resp, "/")

	// --- Per-service usage: one sampling pass, then the pages show it.
	s.sampleCluster(t.Context(), nodes, services, tasks)
	resp, body = b.get("/services")
	wantBody(t, resp, body, "Сервисы", `data-sort="0.5"`, "300.0 MiB")
	resp, body = b.get("/services/web")
	wantBody(t, resp, body, "Потребление ресурсов", "0.50", "(15% от лимита)")
	if samples, _ := s.store.ListServiceSamples("web", time.Now().Add(-time.Hour)); len(samples) != 1 || samples[0].Tasks != 2 {
		t.Fatalf("service samples = %+v, want one sample of 2 tasks", samples)
	}

	// --- Favorites: star a service and a stack, see them on the dashboard
	// and as the palette's opening group.
	for _, f := range [][2]string{{"service", "web"}, {"stack", "shop"}} {
		resp, body = b.do(http.MethodPost, "/favorites", strings.NewReader(url.Values{"kind": {f[0]}, "name": {f[1]}}.Encode()),
			map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Accept": "application/json", "X-CSRF-Token": b.csrf})
		wantBody(t, resp, body, `"favorite":true`)
	}
	resp, body = b.get("/")
	wantBody(t, resp, body, "Избранное", `class="fav-card fav-ok" href="/services/web"`, `href="/stacks/shop"`)

	var res struct {
		Groups []struct {
			Kind string `json:"kind"`
			Hits []struct {
				Label, Href string
			} `json:"hits"`
		} `json:"groups"`
	}
	_, body = b.get("/search?q=")
	if err := json.Unmarshal([]byte(body), &res); err != nil || len(res.Groups) != 1 || res.Groups[0].Kind != "favorite" || len(res.Groups[0].Hits) != 2 {
		t.Fatalf("empty search = %s, want the favorites group", body)
	}

	// --- Global search across kinds.
	_, body = b.get("/search?q=web")
	res.Groups = nil
	_ = json.Unmarshal([]byte(body), &res)
	kinds := map[string]bool{}
	for _, g := range res.Groups {
		kinds[g.Kind] = true
	}
	if !kinds["service"] || !kinds["secret"] {
		t.Fatalf("search web = %s, want service and secret hits", body)
	}
	_, body = b.get("/search?q=node-a")
	if !strings.Contains(body, `"href":"/nodes/n1"`) {
		t.Fatalf("search node-a = %s, want the node", body)
	}

	// --- Two-factor: enroll, then sign in again with a code.
	resp, _ = b.post("/account/2fa/setup", url.Values{})
	wantRedirect(t, resp, "/account/security")
	resp, body = b.get("/account/security")
	wantBody(t, resp, body, `<div class="qr-box"><svg`, "Двухфакторная аутентификация")
	u, _ := s.store.GetUser("alice")
	secret, err := s.decryptSecret(u.TOTPSecretEnc)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := totp.Code(secret, totp.Step(time.Now()))
	resp, body = b.post("/account/2fa/enable", url.Values{"code": {code}})
	wantBody(t, resp, body, "Коды восстановления")

	resp, _ = b.post("/logout", url.Values{})
	wantRedirect(t, resp, "/login")
	resp, _ = b.post("/login", url.Values{"username": {"alice"}, "password": {"pw-123456"}})
	wantRedirect(t, resp, "/login/2fa")
	if resp, _ = b.get("/"); resp.StatusCode != http.StatusSeeOther {
		t.Fatal("password alone reached the dashboard")
	}
	resp, body = b.get("/login/2fa")
	wantBody(t, resp, body, "Код из приложения-аутентификатора")
	// The enrollment code's step is spent (replay guard), so sign in with
	// the next step's code, which is still inside the clock-skew window.
	next, _ := totp.Code(secret, totp.Step(time.Now())+1)
	resp, _ = b.post("/login/2fa", url.Values{"code": {next}})
	wantRedirect(t, resp, "/")

	// --- The sidebar's picker (no return field) goes back to the page it
	// was used on, and the choice is saved on the account.
	resp, body = b.get("/services")
	wantBody(t, resp, body, `class="lang-picker"`)
	form := url.Values{"lang": {"de"}, "csrf_token": {b.csrf}}
	resp, _ = b.do(http.MethodPost, "/language", strings.NewReader(form.Encode()), map[string]string{
		"Content-Type": "application/x-www-form-urlencoded", "Referer": b.base + "/services?x=1"})
	wantRedirect(t, resp, "/services?x=1")
	resp, body = b.get("/services")
	wantBody(t, resp, body, `<html lang="de">`, "Services filtern...")
	if u, _ := s.store.GetUser("alice"); u.Language != "de" {
		t.Fatalf("language not saved on the account: %q", u.Language)
	}
	resp, _ = b.post("/language", url.Values{"lang": {"de"}, "return": {"//evil.example"}})
	wantRedirect(t, resp, "/")
	resp, _ = b.do(http.MethodPost, "/language", strings.NewReader(form.Encode()), map[string]string{
		"Content-Type": "application/x-www-form-urlencoded", "Referer": "https://evil.example/x"})
	wantRedirect(t, resp, "/")
	if resp, _ = b.post("/language", url.Values{"lang": {""}}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("empty language: status %d, want 400", resp.StatusCode)
	}

	// --- Volume backups: list, start, upload (streamed), download.
	resp, body = b.get("/volumes/backups?node=n1")
	wantBody(t, resp, body, "Volume-Sicherungen", "data-20261031-120000-abcdef")
	if strings.Contains(body, "hx-trigger") {
		t.Error("backups page polls although no job is running")
	}
	resp, _ = b.post("/volumes/data/backup?node=n1", url.Values{})
	wantRedirect(t, resp, "/volumes/backups?node=n1&flash=")
	archive := bytes.Repeat([]byte("0123456789"), 50_000)
	resp, _ = b.do(http.MethodPost, "/volumes/backups/upload?node=n1&volume=data", bytes.NewReader(archive),
		map[string]string{"Content-Type": "application/gzip", "X-CSRF-Token": b.csrf})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status %d", resp.StatusCode)
	}
	resp, body = b.get("/volumes/backups/data-20261031-120000-abcdef/download?node=n1")
	if resp.StatusCode != http.StatusOK || body != "ARCHIVE-BYTES" || !strings.Contains(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("download: %d %q %q", resp.StatusCode, body, resp.Header.Get("Content-Disposition"))
	}
	agent.mu.Lock()
	defer agent.mu.Unlock()
	if len(agent.started) != 1 || agent.started[0] != "data" || !bytes.Equal(agent.uploaded, archive) {
		t.Fatalf("agent saw started=%v uploaded=%d bytes", agent.started, len(agent.uploaded))
	}
}
