package admin

import (
	"net/http"
	"net/url"

	"golang.org/x/crypto/bcrypt"
)

func (s *Server) handleAccountPasswordPage(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)
	s.render(w, r, "account_password.html", map[string]any{
		"User":  user,
		"Force": user.MustChangePassword,
		"Error": r.URL.Query().Get("error"),
	})
}

func (s *Server) handleAccountPasswordChange(w http.ResponseWriter, r *http.Request) {
	fail := func(msg string) {
		http.Redirect(w, r, "/account/password?error="+url.QueryEscape(msg), http.StatusSeeOther)
	}

	if err := r.ParseForm(); err != nil {
		fail("errors.bad_request")
		return
	}

	user := userFromContext(r)
	if user.AuthSource == "sso" {
		fail("errors.this_account_signs_in_via_sso_and")
		return
	}

	current := r.FormValue("current_password")
	newPassword := r.FormValue("new_password")
	confirm := r.FormValue("confirm_password")

	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(current)) != nil {
		fail("errors.current_password_is_incorrect")
		return
	}
	if newPassword == "" {
		fail("errors.new_password_is_required")
		return
	}
	if newPassword != confirm {
		fail("errors.new_password_and_confirmation_do_not")
		return
	}
	if newPassword == current {
		fail("errors.new_password_must_be_different_from")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "hash password: "+err.Error(), http.StatusInternalServerError)
		return
	}

	updated := *user
	updated.PasswordHash = string(hash)
	updated.MustChangePassword = false
	if err := s.store.PutUser(updated); err != nil {
		http.Error(w, "update user: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "user.password_change", user.Username, "", nil)
	redirect(w, r, "/")
}
