package admin

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"swarmdash/internal/i18n"
)

// langCookieName remembers a language picked before signing in (the login
// page's picker) or by an API-token-less browser; a signed-in user's own
// User.Language wins over it.
const langCookieName = "swarmdash_lang"

var supportedLanguages = i18n.Languages

// requestLanguage resolves the UI language for r: the signed-in user's
// saved choice, then the language cookie (the login page's picker), then
// English. The browser's Accept-Language is deliberately not consulted -
// the language is always one somebody picked in the UI.
func (s *Server) requestLanguage(r *http.Request) string {
	if u := userFromContext(r); u != nil && i18n.Supported(u.Language) {
		return u.Language
	}
	if c, err := r.Cookie(langCookieName); err == nil && i18n.Supported(c.Value) {
		return c.Value
	}
	return i18n.Default
}

// t translates a server-side message (flash/error text) into r's language.
func (s *Server) t(r *http.Request, msg string, args ...any) string {
	return i18n.T(s.requestLanguage(r), msg, args...)
}

// handleSetLanguage switches the UI language: always via the cookie (so it
// also applies on the login page), and on the account itself when signed
// in, so it follows the user to other browsers. Reachable without a
// session for the login page's picker.
func (s *Server) handleSetLanguage(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	tag := r.FormValue("lang")
	if !i18n.Supported(tag) {
		http.Error(w, "unsupported language", http.StatusBadRequest)
		return
	}
	cookie := &http.Cookie{
		Name:     langCookieName,
		Value:    tag,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(365 * 24 * time.Hour),
	}
	http.SetCookie(w, cookie)

	if user := s.currentUser(r); user != nil && user.Language != tag {
		user.Language = tag
		if err := s.store.PutUser(*user); err != nil {
			http.Error(w, "update user: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	back := r.FormValue("return")
	if back == "" {
		back = refererPath(r)
	}
	http.Redirect(w, r, safeReturnPath(back), http.StatusSeeOther)
}

// refererPath is the path (and query) of the page the request came from,
// for the sidebar's picker, which is on every page and so can't name one.
// Only a same-host Referer counts; anything else means "/".
func refererPath(r *http.Request) string {
	u, err := url.Parse(r.Referer())
	if err != nil || u.Host != r.Host {
		return "/"
	}
	if u.RawQuery != "" {
		return u.Path + "?" + u.RawQuery
	}
	return u.Path
}

// safeReturnPath only follows same-site relative paths, so the language
// form can't be turned into an open redirect.
func safeReturnPath(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") {
		return "/"
	}
	return p
}
