package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"swarmdash/internal/store"
)

func g5PasswordUser(username, password string, mustChange bool) *store.User {
	hash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	return &store.User{Username: username, PasswordHash: string(hash), Role: "admin", MustChangePassword: mustChange}
}

func TestHandleAccountPasswordPage(t *testing.T) {
	s := newTestServer(t, nil)
	user := g5PasswordUser("bob", "oldpass", true)

	r := withUser(httptest.NewRequest(http.MethodGet, "/account/password", nil), user)
	w := httptest.NewRecorder()
	s.handleAccountPasswordPage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "required before you can continue") {
		t.Errorf("body missing forced-change notice, got: %s", w.Body.String())
	}
}

func TestHandleAccountPasswordChange(t *testing.T) {
	s := newTestServer(t, nil)
	user := g5PasswordUser("bob", "oldpass", true)
	if err := s.store.PutUser(*user); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	form := "current_password=oldpass&new_password=newpass1&confirm_password=newpass1"
	r := withUser(httptest.NewRequest(http.MethodPost, "/account/password", strings.NewReader(form)), user)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleAccountPasswordChange(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want /", loc)
	}

	updated, err := s.store.GetUser("bob")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if updated.MustChangePassword {
		t.Error("MustChangePassword still true after change")
	}
	if bcrypt.CompareHashAndPassword([]byte(updated.PasswordHash), []byte("newpass1")) != nil {
		t.Error("stored password hash doesn't match new password")
	}

	entries, err := s.store.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "user.password_change" || entries[0].Username != "bob" {
		t.Fatalf("unexpected audit entries: %+v", entries)
	}
}

func TestHandleAccountPasswordChange_SSO(t *testing.T) {
	s := newTestServer(t, nil)
	user := g5PasswordUser("bob", "oldpass", false)
	user.AuthSource = "sso"

	form := "current_password=oldpass&new_password=newpass1&confirm_password=newpass1"
	r := withUser(httptest.NewRequest(http.MethodPost, "/account/password", strings.NewReader(form)), user)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleAccountPasswordChange(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "/account/password?error=") {
		t.Errorf("Location = %q, want redirect back with an error", loc)
	}
}

func TestHandleAccountPasswordChange_WrongCurrentPassword(t *testing.T) {
	s := newTestServer(t, nil)
	user := g5PasswordUser("bob", "oldpass", false)
	if err := s.store.PutUser(*user); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	form := "current_password=wrongpass&new_password=newpass1&confirm_password=newpass1"
	r := withUser(httptest.NewRequest(http.MethodPost, "/account/password", strings.NewReader(form)), user)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleAccountPasswordChange(w, r)

	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "current+password+is+incorrect") {
		t.Errorf("Location = %q, want current-password-incorrect error", loc)
	}
	entries, _ := s.store.ListAudit(0, 10)
	if len(entries) != 0 {
		t.Fatalf("expected no audit entries, got: %+v", entries)
	}
}

func TestHandleAccountPasswordChange_Mismatch(t *testing.T) {
	s := newTestServer(t, nil)
	user := g5PasswordUser("bob", "oldpass", false)

	form := "current_password=oldpass&new_password=newpass1&confirm_password=different"
	r := withUser(httptest.NewRequest(http.MethodPost, "/account/password", strings.NewReader(form)), user)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleAccountPasswordChange(w, r)

	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "do+not+match") {
		t.Errorf("Location = %q, want mismatch error", loc)
	}
}

func TestHandleAccountPasswordChange_SameAsCurrent(t *testing.T) {
	s := newTestServer(t, nil)
	user := g5PasswordUser("bob", "oldpass", false)

	form := "current_password=oldpass&new_password=oldpass&confirm_password=oldpass"
	r := withUser(httptest.NewRequest(http.MethodPost, "/account/password", strings.NewReader(form)), user)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleAccountPasswordChange(w, r)

	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "must+be+different") {
		t.Errorf("Location = %q, want same-as-current error", loc)
	}
}

func TestHandleAccountPasswordChange_EmptyNewPassword(t *testing.T) {
	s := newTestServer(t, nil)
	user := g5PasswordUser("bob", "oldpass", false)

	form := "current_password=oldpass&new_password=&confirm_password="
	r := withUser(httptest.NewRequest(http.MethodPost, "/account/password", strings.NewReader(form)), user)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleAccountPasswordChange(w, r)

	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "new+password+is+required") {
		t.Errorf("Location = %q, want required error", loc)
	}
}
