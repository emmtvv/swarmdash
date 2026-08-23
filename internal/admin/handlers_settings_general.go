package admin

import (
	"net/http"
	"strconv"

	"swarmdash/internal/store"
)

func (s *Server) handleGeneralSettingsPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "settings_general.html", map[string]any{
		"User":                userFromContext(r),
		"UpdateCheckDisabled": s.isUpdateCheckDisabled(),
	})
}

func (s *Server) handleGeneralSettingsSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	disabled := r.FormValue("update_check_disabled") != ""

	if err := s.store.PutAppSettings(store.AppSettings{UpdateCheckDisabled: disabled}); err != nil {
		http.Error(w, "save settings: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.setUpdateCheckDisabled(disabled)

	s.audit(r, "settings.general.save", "", "update_check_disabled="+strconv.FormatBool(disabled), nil)
	redirect(w, r, "/settings/general")
}

// isUpdateCheckDisabled reports whether the dashboard's update-available
// banner (see internal/web/static/app.js) should be suppressed - both the
// banner markup itself (see handleDashboard) and the CSP's connect-src
// exception for api.github.com (see security.go) key off this, so turning
// it off actually stops the browser from ever making that request rather
// than just hiding the result. Loaded from the store once per process and
// cached; see setUpdateCheckDisabled for how the cache is kept in sync
// with a save.
func (s *Server) isUpdateCheckDisabled() bool {
	s.updateCheckLoaded.Do(func() {
		cfg, err := s.store.GetAppSettings()
		if err == nil {
			s.updateCheckDisabled.Store(cfg.UpdateCheckDisabled)
		}
	})
	return s.updateCheckDisabled.Load()
}

// setUpdateCheckDisabled updates the cache after a successful save.
// Calling this also satisfies updateCheckLoaded (a no-op Do), so a later
// isUpdateCheckDisabled doesn't immediately clobber it by loading from the
// store again.
func (s *Server) setUpdateCheckDisabled(disabled bool) {
	s.updateCheckLoaded.Do(func() {})
	s.updateCheckDisabled.Store(disabled)
}
