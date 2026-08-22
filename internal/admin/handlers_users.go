package admin

import (
	"net/http"
	"sort"
	"time"

	"golang.org/x/crypto/bcrypt"

	"swarmdash/internal/store"
)

func (s *Server) handleUsersPage(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers()
	if err != nil {
		http.Error(w, "list users: "+err.Error(), http.StatusInternalServerError)
		return
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Username < users[j].Username })
	s.render(w, r, "users.html", map[string]any{
		"User":  userFromContext(r),
		"Users": users,
	})
}

func (s *Server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	username := r.FormValue("username")
	password := r.FormValue("password")
	role := r.FormValue("role")
	ssoAccount := r.FormValue("sso_account") == "on"
	if username == "" || (password == "" && !ssoAccount) {
		http.Error(w, "username and password are required", http.StatusBadRequest)
		return
	}
	if !validRole(role) {
		http.Error(w, "role must be admin or viewer", http.StatusBadRequest)
		return
	}
	if _, err := s.store.GetUser(username); err == nil {
		http.Error(w, "a user with that username already exists", http.StatusBadRequest)
		return
	}

	user := store.User{
		Username:  username,
		Role:      role,
		CreatedAt: time.Now(),
	}
	if ssoAccount {
		// Pre-provisioned for SSO (Settings -> SSO): no local password,
		// this identity can only sign in via the "Sign in with ..." button.
		user.AuthSource = "sso"
		user.PasswordHash = ssoUnusablePasswordHash
	} else {
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			http.Error(w, "hash password: "+err.Error(), http.StatusInternalServerError)
			return
		}
		user.AuthSource = "local"
		user.PasswordHash = string(hash)
		user.MustChangePassword = true
	}
	if err := s.store.PutUser(user); err != nil {
		http.Error(w, "create user: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "user.create", username, "role="+role, nil)
	redirect(w, r, "/settings/users")
}

func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	target := r.PathValue("username")
	if target == userFromContext(r).Username {
		http.Error(w, "cannot delete your own account", http.StatusBadRequest)
		return
	}

	users, err := s.store.ListUsers()
	if err != nil {
		http.Error(w, "list users: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var targetRole string
	admins := 0
	for _, u := range users {
		if u.Role == "admin" {
			admins++
		}
		if u.Username == target {
			targetRole = u.Role
		}
	}
	if targetRole == "admin" && admins <= 1 {
		http.Error(w, "cannot delete the last remaining admin", http.StatusBadRequest)
		return
	}

	if err := s.store.DeleteUser(target); err != nil {
		http.Error(w, "delete user: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "user.delete", target, "", nil)
	redirect(w, r, "/settings/users")
}

func (s *Server) handleUserResetPassword(w http.ResponseWriter, r *http.Request) {
	target := r.PathValue("username")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	password := r.FormValue("password")
	if password == "" {
		http.Error(w, "password is required", http.StatusBadRequest)
		return
	}

	user, err := s.store.GetUser(target)
	if err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	if user.AuthSource == "sso" {
		http.Error(w, "this account signs in via SSO and has no local password to reset", http.StatusBadRequest)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "hash password: "+err.Error(), http.StatusInternalServerError)
		return
	}
	user.PasswordHash = string(hash)
	user.MustChangePassword = true
	if err := s.store.PutUser(user); err != nil {
		http.Error(w, "update user: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "user.password_reset", target, "", nil)
	redirect(w, r, "/settings/users")
}
