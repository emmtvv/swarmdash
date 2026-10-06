package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"swarmdash/internal/httpx"
	"swarmdash/internal/store"
)

// backupDocument is the JSON shape downloaded/uploaded by Settings ->
// Backup. Scope is deliberately "users and cluster configuration", not
// history: audit log, task events and cluster metrics samples are left
// out on purpose, since restoring those would just create confusing
// duplicate/out-of-order entries rather than anything worth recovering.
// Sessions are excluded too - restoring stale session tokens would be
// actively wrong.
//
// PasswordEnc/ClientSecretEnc/AuthTokenEnc fields inside these records are
// opaque ciphertext encrypted with a key derived from the cluster secret
// (see crypto.go). They travel through a backup untouched (no
// decrypt/re-encrypt round trip) and only decrypt successfully again on an
// admin instance configured with the *same* --cluster-secret - restoring
// onto a different cluster secret leaves those fields present but unusable
// until re-entered by hand. handleBackupRestore best-effort-checks this at
// restore time and reports which records failed to decrypt.
type backupDocument struct {
	Version     int                        `json:"version"`
	CreatedAt   time.Time                  `json:"created_at"`
	Users       []store.User               `json:"users,omitempty"`
	APITokens   []store.APIToken           `json:"api_tokens,omitempty"`
	Webhooks    []store.Webhook            `json:"webhooks,omitempty"`
	Registries  []store.RegistryCredential `json:"registry_credentials,omitempty"`
	GitStacks   []store.GitStack           `json:"gitops_stacks,omitempty"`
	DeployHooks []store.DeployHook         `json:"deploy_hooks,omitempty"`
	SSOConfig   *store.SSOConfig           `json:"sso_config,omitempty"`
	// Stack compose history and saved templates: configuration in the
	// sense that matters here - what each stack is supposed to be - even
	// though they're stored as a log. Absent from backups taken before
	// they existed, which restore treats as "nothing to merge".
	StackVersions  []store.StackVersion  `json:"stack_versions,omitempty"`
	StackTemplates []store.StackTemplate `json:"stack_templates,omitempty"`
}

const backupVersion = 1

func (s *Server) buildBackup() (backupDocument, error) {
	doc := backupDocument{Version: backupVersion, CreatedAt: time.Now()}
	var err error
	if doc.Users, err = s.store.ListUsers(); err != nil {
		return doc, fmt.Errorf("list users: %w", err)
	}
	if doc.APITokens, err = s.store.ListAPITokens(); err != nil {
		return doc, fmt.Errorf("list api tokens: %w", err)
	}
	if doc.Webhooks, err = s.store.ListWebhooks(); err != nil {
		return doc, fmt.Errorf("list webhooks: %w", err)
	}
	if doc.Registries, err = s.store.ListRegistryCredentials(); err != nil {
		return doc, fmt.Errorf("list registry credentials: %w", err)
	}
	if doc.GitStacks, err = s.store.ListGitStacks(); err != nil {
		return doc, fmt.Errorf("list gitops stacks: %w", err)
	}
	if doc.DeployHooks, err = s.store.ListDeployHooks(); err != nil {
		return doc, fmt.Errorf("list deploy hooks: %w", err)
	}
	if doc.StackVersions, err = s.store.ListStackVersions(""); err != nil {
		return doc, fmt.Errorf("list stack versions: %w", err)
	}
	if doc.StackTemplates, err = s.store.ListStackTemplates(); err != nil {
		return doc, fmt.Errorf("list stack templates: %w", err)
	}
	sso, err := s.store.GetSSOConfig()
	if err == nil {
		doc.SSOConfig = &sso
	} else if !errors.Is(err, store.ErrNotFound) {
		return doc, fmt.Errorf("load sso config: %w", err)
	}
	return doc, nil
}

func (s *Server) handleBackupPage(w http.ResponseWriter, r *http.Request) {
	doc, err := s.buildBackup()
	if err != nil {
		http.Error(w, "build backup: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data := backupCounts(doc)
	data["User"] = userFromContext(r)
	s.render(w, r, "backup.html", data)
}

func (s *Server) handleBackupExport(w http.ResponseWriter, r *http.Request) {
	doc, err := s.buildBackup()
	if err != nil {
		http.Error(w, "build backup: "+err.Error(), http.StatusInternalServerError)
		return
	}
	filename := fmt.Sprintf("swarmdash-backup-%s.json", time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/json")
	httpx.SetAttachment(w, filename)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		s.log.Error("encode backup", "err", err)
		return
	}
	s.audit(r, "backup.export", "", fmt.Sprintf("users=%d tokens=%d webhooks=%d registries=%d gitops=%d deploy_hooks=%d",
		len(doc.Users), len(doc.APITokens), len(doc.Webhooks), len(doc.Registries), len(doc.GitStacks), len(doc.DeployHooks)), nil)
}

// handleBackupRestore merges a previously exported backup back into the
// store. It's a merge (upsert), never a wipe-and-replace: records not
// present in the uploaded file are left untouched, so a partial or stale
// backup can't accidentally delete users or settings created since it was
// taken - restoring can only add or overwrite by ID, never remove.
func (s *Server) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "no backup file uploaded", http.StatusBadRequest)
		return
	}
	defer file.Close()

	var doc backupDocument
	if err := json.NewDecoder(io.LimitReader(file, 32<<20)).Decode(&doc); err != nil {
		http.Error(w, "invalid backup file: "+err.Error(), http.StatusBadRequest)
		return
	}
	if doc.Version > backupVersion {
		http.Error(w, fmt.Sprintf("backup file is version %d, this swarmdash only understands up to %d", doc.Version, backupVersion), http.StatusBadRequest)
		return
	}

	var warnings []string

	for _, u := range doc.Users {
		if err := s.store.PutUser(u); err != nil {
			http.Error(w, "restore user "+u.Username+": "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	for _, t := range doc.APITokens {
		if err := s.store.PutAPIToken(t); err != nil {
			http.Error(w, "restore api token "+t.Name+": "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	for _, wh := range doc.Webhooks {
		if err := s.store.PutWebhook(wh); err != nil {
			http.Error(w, "restore webhook "+wh.Name+": "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	for _, c := range doc.Registries {
		if _, err := s.decryptSecret(c.PasswordEnc); err != nil {
			warnings = append(warnings, fmt.Sprintf("registry credential %q: could not decrypt with this cluster's secret - re-enter its password", c.Server))
		}
		if err := s.store.PutRegistryCredential(c); err != nil {
			http.Error(w, "restore registry credential "+c.Server+": "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	for _, g := range doc.GitStacks {
		if len(g.AuthTokenEnc) > 0 {
			if _, err := s.decryptSecret(g.AuthTokenEnc); err != nil {
				warnings = append(warnings, fmt.Sprintf("gitops stack %q: could not decrypt its access token with this cluster's secret - re-enter it", g.StackName))
			}
		}
		if err := s.store.PutGitStack(g); err != nil {
			http.Error(w, "restore gitops stack "+g.StackName+": "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	for _, h := range doc.DeployHooks {
		if err := s.store.PutDeployHook(h); err != nil {
			http.Error(w, "restore deploy hook: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	for _, v := range doc.StackVersions {
		if len(v.VarsEnc) > 0 {
			if _, err := s.decryptSecret(v.VarsEnc); err != nil {
				warnings = append(warnings, fmt.Sprintf("stack %q v%d: could not decrypt its deploy variables with this cluster's secret - re-enter them when redeploying it", v.StackName, v.Version))
			}
		}
		if err := s.store.PutStackVersion(v); err != nil {
			http.Error(w, "restore stack version: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	for _, t := range doc.StackTemplates {
		if err := s.store.PutStackTemplate(t); err != nil {
			http.Error(w, "restore template "+t.Name+": "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if doc.SSOConfig != nil {
		if len(doc.SSOConfig.ClientSecretEnc) > 0 {
			if _, err := s.decryptSecret(doc.SSOConfig.ClientSecretEnc); err != nil {
				warnings = append(warnings, "sso config: could not decrypt the client secret with this cluster's secret - re-enter it")
			}
		}
		if err := s.store.PutSSOConfig(*doc.SSOConfig); err != nil {
			http.Error(w, "restore sso config: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	detail := fmt.Sprintf("users=%d tokens=%d webhooks=%d registries=%d gitops=%d deploy_hooks=%d sso=%v warnings=%d",
		len(doc.Users), len(doc.APITokens), len(doc.Webhooks), len(doc.Registries), len(doc.GitStacks), len(doc.DeployHooks), doc.SSOConfig != nil, len(warnings))
	s.audit(r, "backup.restore", "", detail, nil)

	data := backupCounts(doc)
	data["User"] = userFromContext(r)
	data["Restored"] = true
	data["RestoredUsers"] = len(doc.Users)
	data["RestoredTokens"] = len(doc.APITokens)
	data["Warnings"] = warnings
	s.render(w, r, "backup.html", data)
}

// backupCounts summarizes doc for the Backup page.
func backupCounts(doc backupDocument) map[string]any {
	return map[string]any{
		"UserCount":          len(doc.Users),
		"TokenCount":         len(doc.APITokens),
		"WebhookCount":       len(doc.Webhooks),
		"RegistryCount":      len(doc.Registries),
		"GitStackCount":      len(doc.GitStacks),
		"DeployHookCount":    len(doc.DeployHooks),
		"StackVersionCount":  len(doc.StackVersions),
		"StackTemplateCount": len(doc.StackTemplates),
		"HasSSO":             doc.SSOConfig != nil,
	}
}
