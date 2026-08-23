// Package updatecheck periodically compares the running swarmdash version
// against the latest GitHub release, so the admin UI can point operators at
// an available update (https://github.com/emmtvv/swarmdash/tags) instead of
// leaving them to notice on their own.
package updatecheck

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// repoLatestReleaseAPI is GitHub's "latest release" endpoint for the
// project. Releases are created by .github/workflows/release.yml whenever a
// v*.*.* tag is pushed, so this always matches the newest tag at
// https://github.com/emmtvv/swarmdash/tags.
const repoLatestReleaseAPI = "https://api.github.com/repos/emmtvv/swarmdash/releases/latest"

// CurrentVersion is the version this build of swarmdash corresponds to.
// Deployments aren't always built via `make build`/`make docker` (which
// would inject internal/version.Version via -ldflags) - many are deployed
// by hand with a manually assembled image/config - so rather than depend on
// that, this is the single source of truth for the update check. Bump it
// by hand alongside the latest released entry in CHANGELOG.md.
const CurrentVersion = "1.1.0"

const checkInterval = 15 * time.Minute

// Status is the checker's current view of whether an update is available.
type Status struct {
	// Enabled is false for unversioned (dev/local) builds, which have
	// nothing meaningful to compare against - no network calls are made
	// and the rest of the fields stay zero.
	Enabled         bool
	CurrentVersion  string
	LatestVersion   string
	UpdateAvailable bool
	ReleaseURL      string
	CheckedAt       time.Time
	// Err holds the last fetch error, if any. A failed check leaves the
	// previous successful Status (LatestVersion/UpdateAvailable/URL) in
	// place rather than clearing it.
	Err string
}

// Checker holds the last-known Status and refreshes it on a timer.
type Checker struct {
	current string
	enabled bool

	client *http.Client
	apiURL string // overridable in tests

	mu     sync.RWMutex
	status Status
}

// NewChecker builds a Checker for the given build version (typically
// version.Version). A "dev" or empty version disables checking entirely.
func NewChecker(currentVersion string) *Checker {
	current := strings.TrimPrefix(strings.TrimSpace(currentVersion), "v")
	enabled := current != "" && current != "dev"
	return &Checker{
		current: current,
		enabled: enabled,
		client:  &http.Client{Timeout: 10 * time.Second},
		apiURL:  repoLatestReleaseAPI,
		status:  Status{Enabled: enabled, CurrentVersion: current},
	}
}

// Status returns the most recently computed Status.
func (c *Checker) Status() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status
}

// Run checks immediately, then every checkInterval, until ctx is done.
func (c *Checker) Run(ctx context.Context) {
	if !c.enabled {
		return
	}
	c.checkOnce(ctx)
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.checkOnce(ctx)
		}
	}
}

func (c *Checker) checkOnce(ctx context.Context) {
	latest, url, err := c.fetchLatest(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.CheckedAt = time.Now()
	if err != nil {
		c.status.Err = err.Error()
		return
	}
	c.status.Err = ""
	c.status.LatestVersion = latest
	c.status.ReleaseURL = url
	c.status.UpdateAvailable = isNewer(latest, c.current)
}

func (c *Checker) fetchLatest(ctx context.Context) (latestVersion, releaseURL string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("github releases/latest: unexpected status %d", resp.StatusCode)
	}

	var body struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", "", fmt.Errorf("decode github response: %w", err)
	}
	if body.TagName == "" {
		return "", "", fmt.Errorf("github response had no tag_name")
	}
	return strings.TrimPrefix(body.TagName, "v"), body.HTMLURL, nil
}

// isNewer reports whether latest is a newer version than current, comparing
// dotted numeric segments (1.2.3) and treating a missing segment as 0.
// Anything that doesn't parse as such is treated as "not newer" - an
// unrecognized format shouldn't wrongly flag every build as outdated.
func isNewer(latest, current string) bool {
	lp, lok := parseVersion(latest)
	cp, cok := parseVersion(current)
	if !lok || !cok {
		return false
	}
	for i := 0; i < len(lp) || i < len(cp); i++ {
		var l, c int
		if i < len(lp) {
			l = lp[i]
		}
		if i < len(cp) {
			c = cp[i]
		}
		if l != c {
			return l > c
		}
	}
	return false
}

// parseVersion splits a version string like "1.2.3" into numeric segments.
// Pre-release/build metadata after "-" or "+" is dropped first.
func parseVersion(v string) ([]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" {
		return nil, false
	}
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, false
		}
		out[i] = n
	}
	return out, true
}
