package admin

import (
	"context"
	"net/http"

	"swarmdash/internal/store"
)

func newUserContext(r *http.Request, user *store.User) context.Context {
	return context.WithValue(r.Context(), ctxKeyUser, user)
}

func userFromContext(r *http.Request) *store.User {
	u, _ := r.Context().Value(ctxKeyUser).(*store.User)
	return u
}
