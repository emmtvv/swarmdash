package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarmdash/internal/updatecheck"
)

func TestGeneralSettings_SaveTogglesUpdateCheck(t *testing.T) {
	s := newTestServer(t, nil)

	if s.isUpdateCheckDisabled() {
		t.Fatal("update check should default to enabled")
	}

	form := strings.NewReader("update_check_disabled=1")
	r := withUser(httptest.NewRequest(http.MethodPost, "/settings/general", form), testAdmin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleGeneralSettingsSave(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	if !s.isUpdateCheckDisabled() {
		t.Error("update check should be disabled after saving with the checkbox set")
	}

	saved, err := s.store.GetAppSettings()
	if err != nil {
		t.Fatalf("get app settings: %v", err)
	}
	if !saved.UpdateCheckDisabled {
		t.Error("UpdateCheckDisabled was not persisted to the store")
	}

	// Unchecking re-enables it.
	form2 := strings.NewReader("")
	r2 := withUser(httptest.NewRequest(http.MethodPost, "/settings/general", form2), testAdmin)
	r2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w2 := httptest.NewRecorder()
	s.handleGeneralSettingsSave(w2, r2)
	if s.isUpdateCheckDisabled() {
		t.Error("update check should be re-enabled after saving with the checkbox cleared")
	}
}

func TestHandleDashboard_OmitsVersionWhenUpdateCheckDisabled(t *testing.T) {
	docker := newFakeDocker(t, dashboardDockerMux(nil, nil, nil))
	s := newTestServer(t, docker)
	s.setUpdateCheckDisabled(true)

	r := withUser(httptest.NewRequest(http.MethodGet, "/", nil), testAdmin)
	w := httptest.NewRecorder()
	s.handleDashboard(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if strings.Contains(w.Body.String(), updatecheck.CurrentVersion) && updatecheck.CurrentVersion != "" && updatecheck.CurrentVersion != "dev" {
		t.Errorf("dashboard rendered the current version %q even though the update check is disabled", updatecheck.CurrentVersion)
	}
	if strings.Contains(w.Body.String(), "update-banner") {
		t.Error("dashboard rendered the update-banner element even though the update check is disabled")
	}
}

func TestSecurity_CSPOmitsGitHubWhenUpdateCheckDisabled(t *testing.T) {
	s := newTestServer(t, nil)
	s.setUpdateCheckDisabled(true)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	s.security(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(w, r)

	if csp := w.Header().Get("Content-Security-Policy"); strings.Contains(csp, "api.github.com") {
		t.Errorf("CSP still allows api.github.com while the update check is disabled: %q", csp)
	}
}
