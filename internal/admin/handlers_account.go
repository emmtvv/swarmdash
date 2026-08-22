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
		fail("bad request")
		return
	}

	user := userFromContext(r)
	if user.AuthSource == "sso" {
		fail("this account signs in via SSO and has no local password to change")
		return
	}

	current := r.FormValue("current_password")
	newPassword := r.FormValue("new_password")
	confirm := r.FormValue("confirm_password")

	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(current)) != nil {
		fail("current password is incorrect")
		return
	}
	if newPassword == "" {
		fail("new password is required")
		return
	}
	if newPassword != confirm {
		fail("new password and confirmation do not match")
		return
	}
	if newPassword == current {
		fail("new password must be different from the current password")
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
