package admin

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarmdash/internal/store"
)

// g3SeedBackupData populates s's store with one record of every kind
// buildBackup collects, encrypting secret fields the same way the real
// handlers would (via s's own cluster-secret-derived key), and returns the
// GitStack/RegistryCredential IDs used so callers can assert against them.
func g3SeedBackupData(t *testing.T, s *Server) {
	t.Helper()
	if err := s.store.PutUser(store.User{Username: "alice", PasswordHash: "hash", Role: "admin"}); err != nil {
		t.Fatalf("PutUser: %v", err)
	}
	if err := s.store.PutAPIToken(store.APIToken{ID: "tok1", Name: "ci", Hash: "h1", Role: "admin", CreatedBy: "alice"}); err != nil {
		t.Fatalf("PutAPIToken: %v", err)
	}
	if err := s.store.PutWebhook(store.Webhook{ID: "wh1", Name: "notify", URL: "https://example.com/hook"}); err != nil {
		t.Fatalf("PutWebhook: %v", err)
	}
	regEnc, err := s.encryptSecret("regpass")
	if err != nil {
		t.Fatalf("encryptSecret: %v", err)
	}
	if err := s.store.PutRegistryCredential(store.RegistryCredential{Server: "registry.example.com", Username: "reguser", PasswordEnc: regEnc}); err != nil {
		t.Fatalf("PutRegistryCredential: %v", err)
	}
	gitEnc, err := s.encryptSecret("gittoken")
	if err != nil {
		t.Fatalf("encryptSecret: %v", err)
	}
	if err := s.store.PutGitStack(store.GitStack{ID: "g1", StackName: "app", RepoURL: "https://example.com/repo.git", AuthTokenEnc: gitEnc}); err != nil {
		t.Fatalf("PutGitStack: %v", err)
	}
	if err := s.store.PutDeployHook(store.DeployHook{ID: "d1", Hash: "h2", ServiceName: "web", CreatedBy: "alice"}); err != nil {
		t.Fatalf("PutDeployHook: %v", err)
	}
	ssoEnc, err := s.encryptSecret("clientsecret")
	if err != nil {
		t.Fatalf("encryptSecret: %v", err)
	}
	if err := s.store.PutSSOConfig(store.SSOConfig{ID: "sso", Enabled: true, Label: "Okta", Issuer: "https://okta.example.com", ClientID: "cid", ClientSecretEnc: ssoEnc}); err != nil {
		t.Fatalf("PutSSOConfig: %v", err)
	}
}

func g3MultipartFileRequest(t *testing.T, method, target, field, filename string, content []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile(field, filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	r := httptest.NewRequest(method, target, &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return r
}

func TestHandleBackupPage(t *testing.T) {
	s := newTestServer(t, nil)
	g3SeedBackupData(t, s)

	r := withUser(httptest.NewRequest(http.MethodGet, "/settings/backup", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleBackupPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `<td class="mono">yes</td>`) {
		t.Errorf("body missing HasSSO=yes, got: %s", w.Body.String())
	}
}

func TestHandleBackupExport(t *testing.T) {
	s := newTestServer(t, nil)
	g3SeedBackupData(t, s)

	r := withUser(httptest.NewRequest(http.MethodGet, "/settings/backup/export", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleBackupExport(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "swarmdash-backup-") {
		t.Errorf("Content-Disposition = %q, missing filename", cd)
	}

	var doc backupDocument
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode backup document: %v", err)
	}
	if len(doc.Users) != 1 || doc.Users[0].Username != "alice" {
		t.Errorf("Users = %+v, want one user alice", doc.Users)
	}
	if len(doc.APITokens) != 1 || len(doc.Webhooks) != 1 || len(doc.Registries) != 1 || len(doc.DeployHooks) != 1 {
		t.Errorf("unexpected record counts: tokens=%d webhooks=%d registries=%d hooks=%d",
			len(doc.APITokens), len(doc.Webhooks), len(doc.Registries), len(doc.DeployHooks))
	}
	if len(doc.GitStacks) != 1 || doc.GitStacks[0].StackName != "app" {
		t.Errorf("GitStacks = %+v, want one stack named app", doc.GitStacks)
	}
	if doc.SSOConfig == nil || doc.SSOConfig.Label != "Okta" {
		t.Errorf("SSOConfig = %+v, want Okta config", doc.SSOConfig)
	}

	entries, _ := s.store.ListAudit(0, 10)
	found := false
	for _, e := range entries {
		if e.Action == "backup.export" {
			found = true
		}
	}
	if !found {
		t.Errorf("no audit entry for backup.export, got: %+v", entries)
	}
}

func TestHandleBackupRestore(t *testing.T) {
	src := newTestServer(t, nil)
	g3SeedBackupData(t, src)
	exportReq := withUser(httptest.NewRequest(http.MethodGet, "/settings/backup/export", nil), testAdmin)
	exportW := httptest.NewRecorder()
	src.handleBackupExport(exportW, exportReq)

	// Restore onto a fresh server sharing the same (empty) ClusterSecret as
	// src, so every encrypted field decrypts cleanly and no warnings fire.
	dst := newTestServer(t, nil)
	r := withUser(g3MultipartFileRequest(t, http.MethodPost, "/settings/backup/restore", "file", "backup.json", exportW.Body.Bytes()), testAdmin)
	w := httptest.NewRecorder()
	dst.handleBackupRestore(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Restore complete") {
		t.Errorf("body missing restore confirmation, got: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "could not be decrypted") {
		t.Errorf("unexpected decrypt warning, body: %s", w.Body.String())
	}

	u, err := dst.store.GetUser("alice")
	if err != nil {
		t.Fatalf("GetUser(alice): %v", err)
	}
	if u.Role != "admin" {
		t.Errorf("restored user role = %q, want admin", u.Role)
	}
	if _, err := dst.store.GetGitStack("g1"); err != nil {
		t.Errorf("GetGitStack(g1) after restore: %v", err)
	}
	sso, err := dst.store.GetSSOConfig()
	if err != nil || sso.Label != "Okta" {
		t.Errorf("restored SSO config = %+v, err = %v", sso, err)
	}

	entries, _ := dst.store.ListAudit(0, 10)
	found := false
	for _, e := range entries {
		if e.Action == "backup.restore" {
			found = true
		}
	}
	if !found {
		t.Errorf("no audit entry for backup.restore, got: %+v", entries)
	}
}

func TestHandleBackupRestore_DecryptWarning(t *testing.T) {
	s := newTestServer(t, nil)
	doc := backupDocument{
		Version: backupVersion,
		Registries: []store.RegistryCredential{
			{Server: "bad.example.com", Username: "u", PasswordEnc: []byte("not-a-valid-ciphertext!!")},
		},
	}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal doc: %v", err)
	}

	r := withUser(g3MultipartFileRequest(t, http.MethodPost, "/settings/backup/restore", "file", "backup.json", body), testAdmin)
	w := httptest.NewRecorder()
	s.handleBackupRestore(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "could not be decrypted") {
		t.Errorf("body missing decrypt warning, got: %s", w.Body.String())
	}
	// The record is still restored even though its secret can't be
	// decrypted - handleBackupRestore is best-effort, not all-or-nothing.
	if _, err := s.store.GetRegistryCredential("bad.example.com"); err != nil {
		t.Errorf("GetRegistryCredential after warning: %v", err)
	}
}

func TestHandleBackupRestore_NoFile(t *testing.T) {
	s := newTestServer(t, nil)

	r := withUser(httptest.NewRequest(http.MethodPost, "/settings/backup/restore", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleBackupRestore(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleBackupRestore_InvalidJSON(t *testing.T) {
	s := newTestServer(t, nil)

	r := withUser(g3MultipartFileRequest(t, http.MethodPost, "/settings/backup/restore", "file", "backup.json", []byte("not json")), testAdmin)
	w := httptest.NewRecorder()
	s.handleBackupRestore(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleBackupRestore_VersionTooNew(t *testing.T) {
	s := newTestServer(t, nil)
	doc := backupDocument{Version: backupVersion + 1}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal doc: %v", err)
	}

	r := withUser(g3MultipartFileRequest(t, http.MethodPost, "/settings/backup/restore", "file", "backup.json", body), testAdmin)
	w := httptest.NewRecorder()
	s.handleBackupRestore(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}
