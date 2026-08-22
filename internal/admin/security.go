package admin

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
)

const csrfCookieName = "swarmdash_csrf"

func csrfTokenFromContext(r *http.Request) string {
	v, _ := r.Context().Value(ctxKeyCSRF).(string)
	return v
}

func cspNonceFromContext(r *http.Request) string {
	v, _ := r.Context().Value(ctxKeyNonce).(string)
	return v
}

// security wraps the entire mux (see Handler in server.go). It:
//
//   - sets defensive response headers (nosniff, frame-busting, a CSP
//     scoped to this app's own static assets, ...) on every response, and
//   - implements CSRF protection for the browser session cookie via the
//     double-submit-cookie pattern: a random token is set as an HttpOnly
//     cookie on first contact, and every mutating request must echo that
//     same value back via the X-CSRF-Token header or a csrf_token form
//     field. app.js injects the latter automatically into every plain
//     <form method="post"> from the <meta name="csrf-token"> tag layout.html
//     renders, so page templates don't each need to wire it up by hand.
//
// A per-response nonce is also generated here and stashed in the request
// context (see cspNonceFromContext) so the small number of inline <script>
// blocks scattered across page templates can be allow-listed individually
// in the CSP header instead of the whole app needing 'unsafe-inline'.
func (s *Server) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce := randomToken(16)

		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=(), usb=(), payment=()")
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		h.Set("Content-Security-Policy", strings.Join([]string{
			"default-src 'self'",
			"script-src 'self' 'nonce-" + nonce + "'",
			// Inline style="" attributes are used throughout the templates;
			// there's no nonce mechanism for those (only for <style>
			// elements), so style-src keeps 'unsafe-inline'. Far lower
			// severity than allowing arbitrary inline script.
			"style-src 'self' 'unsafe-inline'",
			"img-src 'self' data:",
			"font-src 'self'",
			"connect-src 'self'",
			"object-src 'none'",
			"base-uri 'self'",
			"form-action 'self'",
			"frame-ancestors 'none'",
		}, "; "))

		token := csrfCookieValue(r)
		if token == "" {
			token = randomToken(32)
			http.SetCookie(w, &http.Cookie{
				Name:     csrfCookieName,
				Value:    token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
			})
		}
		ctx := context.WithValue(r.Context(), ctxKeyCSRF, token)
		ctx = context.WithValue(ctx, ctxKeyNonce, nonce)
		r = r.WithContext(ctx)

		if !csrfExempt(r) && r.Method != http.MethodGet && r.Method != http.MethodHead {
			submitted := r.Header.Get("X-CSRF-Token")
			if submitted == "" {
				// ParseMultipartForm also handles plain
				// application/x-www-form-urlencoded bodies (it calls
				// ParseForm internally first); for those it just returns
				// ErrNotMultipart once the form values are already
				// populated, which FormValue below then reads normally.
				// Needed so the backup/restore file upload (multipart) can
				// carry its token as a regular form field too.
				_ = r.ParseMultipartForm(32 << 20)
				submitted = r.FormValue("csrf_token")
			}
			if token == "" || submitted == "" || subtle.ConstantTimeCompare([]byte(token), []byte(submitted)) != 1 {
				http.Error(w, "invalid or missing CSRF token - please refresh the page and try again", http.StatusForbidden)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func csrfCookieValue(r *http.Request) string {
	c, err := r.Cookie(csrfCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// csrfExempt reports requests that can't be forged via a victim's browser
// in the first place, so double-submit validation would only get in the
// way: bearer-token API calls (a third-party page can't attach an
// Authorization header to a cross-site request) and the CI deploy-hook
// endpoint, which is unauthenticated by design - see
// handlers_deploy_hooks.go - and carries its own credential in the URL.
func csrfExempt(r *http.Request) bool {
	if bearerToken(r) != "" {
		return true
	}
	return strings.HasPrefix(r.URL.Path, "/hooks/deploy/")
}
