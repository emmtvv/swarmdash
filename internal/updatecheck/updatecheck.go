// Package updatecheck holds the single source of truth for the version this
// build of swarmdash corresponds to. The actual check against the latest
// GitHub release (github.com/emmtvv/swarmdash) happens client-side - the
// browser calls the GitHub API directly and compares versions in JS (see
// internal/web/static/app.js) - so nothing here talks to the network or
// keeps any state.
package updatecheck

import (
	"regexp"

	"swarmdash"
)

// CurrentVersion is the version this build of swarmdash corresponds to.
// Deployments aren't always built via `make build`/`make docker` (which
// would inject internal/version.Version via -ldflags) - many are deployed
// by hand with a manually assembled image/config - so rather than depend on
// that, it's taken from the latest released entry in CHANGELOG.md, which is
// embedded into the binary. An "Unreleased" section is skipped.
var CurrentVersion = latestVersion(swarmdash.Changelog)

var releaseHeading = regexp.MustCompile(`(?m)^## \[(\d+\.\d+\.\d+)\]`)

// latestVersion returns the version from the first "## [x.y.z]" heading in
// changelog, or "dev" if there is none.
func latestVersion(changelog string) string {
	m := releaseHeading.FindStringSubmatch(changelog)
	if m == nil {
		return "dev"
	}
	return m[1]
}
