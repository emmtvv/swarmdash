package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"swarmdash/internal/store"
)

// g3UnreachableRepoURL points at a loopback port nothing listens on, so
// gitfetch.FetchComposeFile fails fast with "connection refused" instead of
// needing a real git server - handleGitOpsCreate/handleGitOpsSync only log
// that failure (see recordGitOpsResult), they don't surface it as an HTTP
// error, so the request/persistence behavior around it is still testable.
const g3UnreachableRepoURL = "http://127.0.0.1:1/repo.git"

func TestHandleGitOpsPage(t *testing.T) {
	s := newTestServer(t, nil)
	if err := s.store.PutGitStack(store.GitStack{ID: "g1", StackName: "zeta"}); err != nil {
		t.Fatalf("PutGitStack: %v", err)
	}
	if err := s.store.PutGitStack(store.GitStack{ID: "g2", StackName: "alpha"}); err != nil {
		t.Fatalf("PutGitStack: %v", err)
	}

	r := withUser(httptest.NewRequest(http.MethodGet, "/stacks/gitops", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleGitOpsPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "zeta") || !strings.Contains(body, "alpha") {
		t.Errorf("body missing stack names, got: %s", body)
	}
}

func TestHandleGitOpsCreate(t *testing.T) {
	s := newTestServer(t, nil)

	form := url.Values{
		"name":         {"my-app"},
		"repo_url":     {g3UnreachableRepoURL},
		"ref":          {"prod"},
		"compose_path": {"deploy/compose.yml"},
		"token":        {"secret-token"},
		"poll_seconds": {"300"},
	}
	r := g3FormRequest(http.MethodPost, "/stacks/gitops", form)
	r = withUser(r, testAdmin)
	w := httptest.NewRecorder()
	s.handleGitOpsCreate(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "/stacks/gitops" {
		t.Errorf("Location = %q, want /stacks/gitops", loc)
	}

	stacks, err := s.store.ListGitStacks()
	if err != nil {
		t.Fatalf("ListGitStacks: %v", err)
	}
	if len(stacks) != 1 {
		t.Fatalf("ListGitStacks() = %d stacks, want 1", len(stacks))
	}
	gs := stacks[0]
	if gs.StackName != "my-app" || gs.RepoURL != g3UnreachableRepoURL || gs.Ref != "prod" || gs.ComposePath != "deploy/compose.yml" || gs.PollSeconds != 300 {
		t.Errorf("unexpected persisted stack: %+v", gs)
	}
	if len(gs.AuthTokenEnc) == 0 {
		t.Fatalf("AuthTokenEnc not populated")
	}
	plain, err := s.decryptSecret(gs.AuthTokenEnc)
	if err != nil {
		t.Fatalf("decryptSecret: %v", err)
	}
	if plain != "secret-token" {
		t.Errorf("decrypted token = %q, want secret-token", plain)
	}

	entries, _ := s.store.ListAudit(0, 10)
	found := false
	for _, e := range entries {
		if e.Action == "gitops.create" && e.Target == "my-app" {
			found = true
		}
	}
	if !found {
		t.Errorf("no audit entry for gitops.create, got: %+v", entries)
	}
}

func TestHandleGitOpsCreate_Defaults(t *testing.T) {
	s := newTestServer(t, nil)

	form := url.Values{
		"name":     {"my-app"},
		"repo_url": {g3UnreachableRepoURL},
	}
	r := withUser(g3FormRequest(http.MethodPost, "/stacks/gitops", form), testAdmin)
	w := httptest.NewRecorder()
	s.handleGitOpsCreate(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	stacks, _ := s.store.ListGitStacks()
	if len(stacks) != 1 {
		t.Fatalf("ListGitStacks() = %d stacks, want 1", len(stacks))
	}
	if stacks[0].Ref != "main" {
		t.Errorf("Ref = %q, want main", stacks[0].Ref)
	}
	if stacks[0].ComposePath != "docker-compose.yml" {
		t.Errorf("ComposePath = %q, want docker-compose.yml", stacks[0].ComposePath)
	}
	if len(stacks[0].AuthTokenEnc) != 0 {
		t.Errorf("AuthTokenEnc = %v, want empty (no token supplied)", stacks[0].AuthTokenEnc)
	}
}

func TestHandleGitOpsCreate_InvalidName(t *testing.T) {
	s := newTestServer(t, nil)

	form := url.Values{"name": {"bad name!"}, "repo_url": {g3UnreachableRepoURL}}
	r := withUser(g3FormRequest(http.MethodPost, "/stacks/gitops", form), testAdmin)
	w := httptest.NewRecorder()
	s.handleGitOpsCreate(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
	stacks, _ := s.store.ListGitStacks()
	if len(stacks) != 0 {
		t.Errorf("expected no stack persisted, got %d", len(stacks))
	}
}

func TestHandleGitOpsCreate_MissingRepoURL(t *testing.T) {
	s := newTestServer(t, nil)

	form := url.Values{"name": {"my-app"}}
	r := withUser(g3FormRequest(http.MethodPost, "/stacks/gitops", form), testAdmin)
	w := httptest.NewRecorder()
	s.handleGitOpsCreate(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleGitOpsSync(t *testing.T) {
	s := newTestServer(t, nil)
	gs := store.GitStack{ID: "g1", StackName: "my-app", RepoURL: g3UnreachableRepoURL, Ref: "main", ComposePath: "docker-compose.yml"}
	if err := s.store.PutGitStack(gs); err != nil {
		t.Fatalf("PutGitStack: %v", err)
	}

	r := withUser(httptest.NewRequest(http.MethodPost, "/stacks/gitops/g1/sync", nil), testAdmin)
	r.SetPathValue("id", "g1")
	w := httptest.NewRecorder()
	s.handleGitOpsSync(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}

	// The unreachable repo makes the fetch fail; syncGitStack records that
	// failure onto the stack and the audit log rather than surfacing it as
	// an HTTP error (see recordGitOpsResult).
	got, err := s.store.GetGitStack("g1")
	if err != nil {
		t.Fatalf("GetGitStack: %v", err)
	}
	if got.LastError == "" {
		t.Errorf("expected LastError to be recorded after a failed sync")
	}

	entries, _ := s.store.ListAudit(0, 10)
	found := false
	for _, e := range entries {
		if e.Action == "gitops.sync" && e.Target == "my-app" && !e.Success {
			found = true
		}
	}
	if !found {
		t.Errorf("no failed audit entry for gitops.sync, got: %+v", entries)
	}
}

func TestHandleGitOpsSync_NotFound(t *testing.T) {
	s := newTestServer(t, nil)

	r := withUser(httptest.NewRequest(http.MethodPost, "/stacks/gitops/missing/sync", nil), testAdmin)
	r.SetPathValue("id", "missing")
	w := httptest.NewRecorder()
	s.handleGitOpsSync(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleGitOpsDelete(t *testing.T) {
	s := newTestServer(t, nil)
	if err := s.store.PutGitStack(store.GitStack{ID: "g1", StackName: "my-app"}); err != nil {
		t.Fatalf("PutGitStack: %v", err)
	}

	r := withUser(httptest.NewRequest(http.MethodPost, "/stacks/gitops/g1/delete", nil), testAdmin)
	r.SetPathValue("id", "g1")
	w := httptest.NewRecorder()
	s.handleGitOpsDelete(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	if _, err := s.store.GetGitStack("g1"); err != store.ErrNotFound {
		t.Errorf("GetGitStack after delete: err = %v, want ErrNotFound", err)
	}
	entries, _ := s.store.ListAudit(0, 10)
	found := false
	for _, e := range entries {
		if e.Action == "gitops.delete" && e.Target == "my-app" {
			found = true
		}
	}
	if !found {
		t.Errorf("no audit entry for gitops.delete, got: %+v", entries)
	}
}

func TestHandleGitOpsDelete_NotFound(t *testing.T) {
	s := newTestServer(t, nil)

	r := withUser(httptest.NewRequest(http.MethodPost, "/stacks/gitops/missing/delete", nil), testAdmin)
	r.SetPathValue("id", "missing")
	w := httptest.NewRecorder()
	s.handleGitOpsDelete(w, r)

	// DeleteGitStack is a no-op DELETE on an unknown ID, so the handler
	// still redirects; GetGitStack failing just means no audit is written.
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	entries, _ := s.store.ListAudit(0, 10)
	for _, e := range entries {
		if e.Action == "gitops.delete" {
			t.Errorf("unexpected audit entry for delete of missing stack: %+v", e)
		}
	}
}
