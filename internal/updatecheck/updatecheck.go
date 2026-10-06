// Package updatecheck holds the single source of truth for the version this
// build of swarmdash corresponds to. The actual check against the latest
// GitHub release (github.com/emmtvv/swarmdash) happens client-side - the
// browser calls the GitHub API directly and compares versions in JS (see
// internal/web/static/app.js) - so nothing here talks to the network or
// keeps any state.
package updatecheck

// CurrentVersion is the version this build of swarmdash corresponds to.
// Deployments aren't always built via `make build`/`make docker` (which
// would inject internal/version.Version via -ldflags) - many are deployed
// by hand with a manually assembled image/config - so rather than depend on
// that, this is the single source of truth for the update check. Bump it
// by hand alongside the latest released entry in CHANGELOG.md.
const CurrentVersion = "1.2.3"
