package admin

import (
	"context"
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

const csrfCookieName = "swarmdash_csrf"

// isSecureRequest reports whether the request arrived over an encrypted
// connection, either directly (UITLSCertFile/-KeyFile terminate TLS in
// this process, so r.TLS is set) or via a reverse proxy that terminates
// TLS and sets X-Forwarded-Proto - the same signal ssoRedirectURI already
// trusts for building the OAuth redirect URI. Used to gate the Secure
// attribute on cookies: set it whenever the connection is HTTPS so
// browsers never send them in cleartext, but don't hard-code it, since an
// admin reachable only over plain HTTP (a trusted, internal cluster
// network) would otherwise never be able to log in at all.
//
// X-Forwarded-Proto is only trusted from a --trusted-proxies address -
// same gate clientIP applies to X-Forwarded-For (see auth.go). Without
// that flag any client could otherwise claim X-Forwarded-Proto: https on
// a plaintext connection and get a Secure cookie the browser then refuses
// to send back over HTTP, silently breaking its own session.
func (s *Server) isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !s.isTrustedProxy(host) {
		return false
	}
	return r.Header.Get("X-Forwarded-Proto") == "https"
}

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
		// api.github.com: the dashboard's update-check banner fetches the
		// latest release directly from the browser (see
		// internal/web/static/app.js) rather than through the admin
		// server. Only loosen the policy for it while the check is
		// actually enabled (Settings -> General) - turning the banner off
		// should mean the browser never talks to GitHub at all, not just
		// that the result goes unused.
		connectSrc := "connect-src 'self'"
		if !s.isUpdateCheckDisabled() {
			connectSrc += " https://api.github.com"
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
			connectSrc,
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
				Secure:   s.isSecureRequest(r),
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
