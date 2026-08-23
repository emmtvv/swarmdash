package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"swarmdash/internal/store"
)

func TestUserFromContext(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if u := userFromContext(r); u != nil {
		t.Fatalf("userFromContext() on bare request = %+v, want nil", u)
	}

	u := &store.User{Username: "alice", Role: "admin"}
	r = r.WithContext(newUserContext(r, u))
	if got := userFromContext(r); got != u {
		t.Fatalf("userFromContext() = %+v, want %+v", got, u)
	}
}
