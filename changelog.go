// Package swarmdash exposes files from the repository root that other
// packages need at runtime. go:embed can't reach parent directories, so the
// embedding has to happen here.
package swarmdash

import _ "embed"

// Changelog is the contents of CHANGELOG.md, used by internal/updatecheck to
// derive the version this build corresponds to.
//
//go:embed CHANGELOG.md
var Changelog string
