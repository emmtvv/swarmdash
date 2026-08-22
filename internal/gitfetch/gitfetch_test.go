package gitfetch

import "testing"

func TestShortCommit(t *testing.T) {
	tests := []struct {
		name   string
		commit string
		want   string
	}{
		{"full 40-char SHA truncates to 12", "8f3b1c2d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b", "8f3b1c2d4e5f"},
		{"already short passes through", "abc123", "abc123"},
		{"exactly 12 chars passes through", "0123456789ab", "0123456789ab"},
		{"empty string", "", ""},
		{"whitespace trimmed when not truncated", "  abc123  ", "abc123"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShortCommit(tt.commit); got != tt.want {
				t.Errorf("ShortCommit(%q) = %q, want %q", tt.commit, got, tt.want)
			}
		})
	}
}
