package updatecheck

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIsNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"1.0.1", "1.0.0", true},
		{"1.0.0", "1.0.1", false},
		{"1.0.0", "1.0.0", false},
		{"2.0.0", "1.9.9", true},
		{"1.2", "1.2.0", false},
		{"1.2.1", "1.2", true},
		{"v1.0.1", "1.0.0", true},
		{"1.0.1-rc1", "1.0.0", true},
		{"garbage", "1.0.0", false},
		{"1.0.1", "dev", false},
	}
	for _, tc := range cases {
		if got := isNewer(tc.latest, tc.current); got != tc.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", tc.latest, tc.current, got, tc.want)
		}
	}
}

func TestNewCheckerDisabledForDevBuild(t *testing.T) {
	for _, v := range []string{"", "dev"} {
		c := NewChecker(v)
		if c.Status().Enabled {
			t.Errorf("NewChecker(%q).Status().Enabled = true, want false", v)
		}
	}
}

func TestRunSkipsNetworkWhenDisabled(t *testing.T) {
	c := NewChecker("dev")
	c.apiURL = "http://127.0.0.1:0/unreachable"

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	c.Run(ctx) // should return immediately, not block on the unreachable URL

	if c.Status().CheckedAt.After(time.Time{}) && !c.Status().CheckedAt.IsZero() {
		t.Errorf("expected no check to have run, got CheckedAt = %v", c.Status().CheckedAt)
	}
}

func TestCheckOnceDetectsUpdate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"tag_name": "v9.9.9",
			"html_url": "https://github.com/emmtvv/swarmdash/releases/tag/v9.9.9",
		})
	}))
	defer srv.Close()

	c := NewChecker("1.0.0")
	c.apiURL = srv.URL

	c.checkOnce(context.Background())

	st := c.Status()
	if !st.UpdateAvailable {
		t.Fatal("expected UpdateAvailable = true")
	}
	if st.LatestVersion != "9.9.9" {
		t.Errorf("LatestVersion = %q, want 9.9.9", st.LatestVersion)
	}
	if st.ReleaseURL != "https://github.com/emmtvv/swarmdash/releases/tag/v9.9.9" {
		t.Errorf("unexpected ReleaseURL: %q", st.ReleaseURL)
	}
	if st.Err != "" {
		t.Errorf("unexpected Err: %q", st.Err)
	}
}

func TestCheckOnceKeepsLastGoodStatusOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewChecker("1.0.0")
	c.apiURL = srv.URL
	c.status.LatestVersion = "1.0.0"
	c.status.UpdateAvailable = false

	c.checkOnce(context.Background())

	st := c.Status()
	if st.Err == "" {
		t.Fatal("expected Err to be set")
	}
	if st.LatestVersion != "1.0.0" {
		t.Errorf("expected previous LatestVersion to be retained, got %q", st.LatestVersion)
	}
}
