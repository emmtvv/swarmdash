// Package gitfetch reads a single file out of a git repository at a given
// ref, for GitOps stack deploy (Stacks -> GitOps deploy). It uses a pure-Go
// git implementation (go-git) rather than shelling out to a system git
// binary, keeping swarmdash a single Go binary with no external runtime
// dependencies - the same property the rest of the project relies on.
package gitfetch

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"

	"github.com/go-git/go-billy/v5/memfs"
)

// FetchComposeFile clones repoURL at ref (a branch name) into memory and
// returns the HEAD commit hash and the contents of path. token, if
// non-empty, is sent as HTTP basic auth (the conventional way to
// authenticate a personal access token over HTTPS on GitHub/GitLab/etc).
func FetchComposeFile(ctx context.Context, repoURL, ref, path, token string) (commit string, content []byte, err error) {
	if repoURL == "" {
		return "", nil, fmt.Errorf("repo URL is required")
	}
	if ref == "" {
		ref = "main"
	}
	if path == "" {
		path = "docker-compose.yml"
	}

	opts := &git.CloneOptions{
		URL:           repoURL,
		ReferenceName: plumbing.NewBranchReferenceName(ref),
		SingleBranch:  true,
		Depth:         1,
		Tags:          git.NoTags,
	}
	if token != "" {
		opts.Auth = &githttp.BasicAuth{Username: "x-access-token", Password: token}
	}

	fs := memfs.New()
	repo, err := git.CloneContext(ctx, memory.NewStorage(), fs, opts)
	if err != nil {
		if err == transport.ErrAuthenticationRequired {
			return "", nil, fmt.Errorf("clone %s: authentication required (set a token)", repoURL)
		}
		return "", nil, fmt.Errorf("clone %s (ref %s): %w", repoURL, ref, err)
	}

	head, err := repo.Head()
	if err != nil {
		return "", nil, fmt.Errorf("resolve HEAD: %w", err)
	}

	f, err := fs.Open(path)
	if err != nil {
		return "", nil, fmt.Errorf("open %s in repo: %w", path, err)
	}
	defer f.Close()
	buf, err := io.ReadAll(f)
	if err != nil {
		return "", nil, fmt.Errorf("read %s: %w", path, err)
	}

	return head.Hash().String(), buf, nil
}

// ShortCommit trims a full commit hash to the 12-character form used
// elsewhere in the UI (see the shortID template func).
func ShortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return strings.TrimSpace(commit)
}
