package admin

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"swarmdash/internal/compose"
	"swarmdash/internal/gitfetch"
	"swarmdash/internal/store"
)

func (s *Server) handleGitOpsPage(w http.ResponseWriter, r *http.Request) {
	stacks, err := s.store.ListGitStacks()
	if err != nil {
		http.Error(w, "list gitops stacks: "+err.Error(), http.StatusInternalServerError)
		return
	}
	sort.Slice(stacks, func(i, j int) bool { return stacks[i].StackName < stacks[j].StackName })
	s.render(w, r, "gitops.html", map[string]any{
		"User":   userFromContext(r),
		"Stacks": stacks,
	})
}

func (s *Server) handleGitOpsCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	repoURL := r.FormValue("repo_url")
	ref := r.FormValue("ref")
	composePath := r.FormValue("compose_path")
	token := r.FormValue("token")
	pollSeconds, _ := strconv.Atoi(r.FormValue("poll_seconds"))

	if !stackNamePattern.MatchString(name) {
		http.Error(w, "invalid stack name", http.StatusBadRequest)
		return
	}
	if repoURL == "" {
		http.Error(w, "repo URL is required", http.StatusBadRequest)
		return
	}
	if ref == "" {
		ref = "main"
	}
	if composePath == "" {
		composePath = "docker-compose.yml"
	}

	gs := store.GitStack{
		ID:          randomToken(8),
		StackName:   name,
		RepoURL:     repoURL,
		Ref:         ref,
		ComposePath: composePath,
		PollSeconds: pollSeconds,
		CreatedBy:   userFromContext(r).Username,
		CreatedAt:   time.Now(),
	}
	if token != "" {
		enc, err := s.encryptSecret(token)
		if err != nil {
			http.Error(w, "encrypt token: "+err.Error(), http.StatusInternalServerError)
			return
		}
		gs.AuthTokenEnc = enc
	}

	if err := s.store.PutGitStack(gs); err != nil {
		http.Error(w, "save gitops stack: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "gitops.create", name, repoURL, nil)

	// Kick an immediate first sync so the user gets instant feedback instead
	// of waiting for the next poll tick.
	if err := s.syncGitStack(r.Context(), gs); err != nil {
		s.log.Warn("gitops: initial sync failed", "stack", name, "err", err)
	}
	redirect(w, r, "/stacks/gitops")
}

func (s *Server) handleGitOpsSync(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	gs, err := s.store.GetGitStack(id)
	if err != nil {
		http.Error(w, "gitops stack not found", http.StatusNotFound)
		return
	}
	if err := s.syncGitStack(r.Context(), gs); err != nil {
		s.audit(r, "gitops.sync", gs.StackName, "", err)
	} else {
		s.audit(r, "gitops.sync", gs.StackName, "", nil)
	}
	redirect(w, r, "/stacks/gitops")
}

func (s *Server) handleGitOpsDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	gs, err := s.store.GetGitStack(id)
	if err == nil {
		s.audit(r, "gitops.delete", gs.StackName, "", nil)
	}
	if err := s.store.DeleteGitStack(id); err != nil {
		http.Error(w, "delete gitops stack: "+err.Error(), http.StatusInternalServerError)
		return
	}
	redirect(w, r, "/stacks/gitops")
}

// syncGitStack fetches the compose file at gs's configured ref and, if the
// commit has moved on since the last sync, applies it - the same
// applyCompose path the manual "Deploy stack" form uses. Shared by the
// "Sync now" button and the auto-poller (gitops_poller.go).
func (s *Server) syncGitStack(ctx context.Context, gs store.GitStack) error {
	token := ""
	if len(gs.AuthTokenEnc) > 0 {
		t, err := s.decryptSecret(gs.AuthTokenEnc)
		if err != nil {
			return s.recordGitOpsResult(gs, "", fmt.Errorf("decrypt token: %w", err))
		}
		token = t
	}

	commit, content, err := gitfetch.FetchComposeFile(ctx, gs.RepoURL, gs.Ref, gs.ComposePath, token)
	if err != nil {
		return s.recordGitOpsResult(gs, "", err)
	}
	if commit == gs.LastCommit {
		return nil // already up to date
	}

	file, err := compose.Parse(content)
	if err != nil {
		return s.recordGitOpsResult(gs, commit, fmt.Errorf("parse compose file: %w", err))
	}
	if _, err := s.applyCompose(ctx, gs.StackName, file, applyOptions{}); err != nil {
		return s.recordGitOpsResult(gs, commit, fmt.Errorf("apply: %w", err))
	}
	s.recordStackVersion(gs.StackName, string(content), "", "gitops", "commit "+shortCommit(commit), "gitops")

	return s.recordGitOpsResult(gs, commit, nil)
}

func shortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

// recordGitOpsResult persists the outcome of a sync attempt back onto the
// GitStack record so the UI can show last-deployed/last-error state.
func (s *Server) recordGitOpsResult(gs store.GitStack, commit string, syncErr error) error {
	if commit != "" {
		gs.LastCommit = commit
	}
	if syncErr != nil {
		gs.LastError = syncErr.Error()
	} else {
		gs.LastError = ""
		gs.LastDeployedAt = time.Now()
	}
	if err := s.store.PutGitStack(gs); err != nil {
		s.log.Error("gitops: persist sync result", "stack", gs.StackName, "err", err)
	}
	return syncErr
}
