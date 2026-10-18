package updatecheck

import "testing"

func TestLatestVersion(t *testing.T) {
	tests := []struct {
		name, changelog, want string
	}{
		{"first release wins", "# Changelog\n\n## [1.2.5] - 2026-10-18\n\n## [1.2.4] - 2026-10-14\n", "1.2.5"},
		{"skips unreleased", "## [Unreleased]\n\n- wip\n\n## [2.0.0] - 2026-11-01\n", "2.0.0"},
		{"ignores non-headings", "see [1.0.0] for details\n### [9.9.9]\n## [1.1.0]\n", "1.1.0"},
		{"no releases", "# Changelog\n", "dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := latestVersion(tt.changelog); got != tt.want {
				t.Errorf("latestVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCurrentVersionFromEmbeddedChangelog(t *testing.T) {
	if CurrentVersion == "dev" {
		t.Fatal("CurrentVersion is \"dev\": no \"## [x.y.z]\" heading found in CHANGELOG.md")
	}
}
