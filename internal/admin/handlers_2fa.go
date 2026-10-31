package admin

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"swarmdash/internal/store"
	"swarmdash/internal/totp"
)

// Two-factor authentication (TOTP) for local-password accounts. SSO
// accounts never see any of this: their second factor, if any, is the
// identity provider's business, and handleSSOCallback starts a session
// directly.
//
// Login becomes two steps. handleLoginSubmit checks the password as
// before, but for an account with TOTPEnabled it sets a short-lived signed
// "password OK, code pending" cookie instead of a session and sends the
// browser to /login/2fa, where handleLogin2FASubmit checks the code and
// only then calls startSession. The pending state lives entirely in that
// HMAC-signed cookie (keyed off the cluster secret), so there's no
// half-authenticated session row that requireAuth could ever mistake for a
// real one.

const (
	mfaCookieName = "swarmdash_mfa"
	mfaPendingTTL = 5 * time.Minute

	recoveryCodeCount = 10
	totpIssuer        = "swarmdash"
)

var (
	errWrong2FACode    = errors.New("wrong two-factor code")
	errMFAPendingStale = errors.New("two-factor step expired or tampered with")
)

func (s *Server) mfaKey() []byte {
	sum := sha256.Sum256([]byte("swarmdash-mfa-pending:" + s.cfg.ClusterSecret))
	return sum[:]
}

func (s *Server) signMFAPending(username string, expires time.Time) string {
	payload := username + "|" + strconv.FormatInt(expires.Unix(), 10)
	mac := hmac.New(sha256.New, s.mfaKey())
	mac.Write([]byte(payload))
	return url.QueryEscape(payload + "|" + hex.EncodeToString(mac.Sum(nil)))
}

// mfaPendingUser returns the username whose password was verified by the
// pending-2FA cookie on r, or "" if there's no valid, unexpired one.
func (s *Server) mfaPendingUser(r *http.Request) string {
	c, err := r.Cookie(mfaCookieName)
	if err != nil {
		return ""
	}
	raw, err := url.QueryUnescape(c.Value)
	if err != nil {
		return ""
	}
	i := strings.LastIndex(raw, "|")
	if i < 0 {
		return ""
	}
	payload, sig := raw[:i], raw[i+1:]
	mac := hmac.New(sha256.New, s.mfaKey())
	mac.Write([]byte(payload))
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(want)) {
		return ""
	}
	j := strings.LastIndex(payload, "|")
	if j < 0 {
		return ""
	}
	exp, err := strconv.ParseInt(payload[j+1:], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return ""
	}
	return payload[:j]
}

func (s *Server) setMFAPending(w http.ResponseWriter, r *http.Request, username string) {
	exp := time.Now().Add(mfaPendingTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     mfaCookieName,
		Value:    s.signMFAPending(username, exp),
		Path:     "/login",
		HttpOnly: true,
		Secure:   s.isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		Expires:  exp,
	})
}

func (s *Server) clearMFAPending(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: mfaCookieName, Value: "", Path: "/login", Secure: s.isSecureRequest(r), MaxAge: -1})
}

func (s *Server) handleLogin2FAPage(w http.ResponseWriter, r *http.Request) {
	if s.mfaPendingUser(r) == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.render(w, r, "login_2fa.html", map[string]any{
		"Error": r.URL.Query().Get("error"),
	})
}

func (s *Server) handleLogin2FASubmit(w http.ResponseWriter, r *http.Request) {
	username := s.mfaPendingUser(r)
	if username == "" {
		s.auditAs(r, "", "user.login.2fa", "", "", errMFAPendingStale)
		http.Redirect(w, r, "/login?error="+url.QueryEscape("sign-in timed out - enter your password again"), http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/login/2fa?error=bad+request", http.StatusSeeOther)
		return
	}
	ip := s.clientIP(r)

	// Shares the password step's (username, IP) lockout: 10^6 codes is a
	// small space, so guessing has to be throttled here just as hard.
	if wait, err := s.loginLockedFor(username, ip); err != nil {
		s.log.Error("check login lockout", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if wait > 0 {
		s.clearMFAPending(w, r)
		http.Redirect(w, r, "/login?error="+url.QueryEscape(s.t(r, "too many failed attempts - try again in %dm", int(wait.Minutes())+1)), http.StatusSeeOther)
		return
	}

	user, err := s.store.GetUser(username)
	if err != nil || !user.TOTPEnabled {
		s.clearMFAPending(w, r)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	method, ok := s.checkSecondFactor(&user, r.FormValue("code"))
	if !ok {
		s.recordLoginFailure(username, ip)
		s.auditAs(r, username, "user.login.2fa", username, "", errWrong2FACode)
		http.Redirect(w, r, "/login/2fa?error="+url.QueryEscape("invalid code"), http.StatusSeeOther)
		return
	}
	if err := s.store.PutUser(user); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.store.DeleteLoginAttempt(loginAttemptKey(username, ip)); err != nil {
		s.log.Error("clear login attempts", "err", err)
	}

	s.clearMFAPending(w, r)
	if err := s.startSession(w, r, username); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.auditAs(r, username, "user.login", username, method, nil)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// checkSecondFactor accepts either a current TOTP code or one of the
// account's unused recovery codes, updating user in place (the replay
// guard's last step, or the consumed recovery code) - the caller persists
// it. method says which kind matched, for the audit log.
func (s *Server) checkSecondFactor(user *store.User, code string) (method string, ok bool) {
	code = strings.TrimSpace(code)
	if code == "" {
		return "", false
	}
	if secret, err := s.decryptSecret(user.TOTPSecretEnc); err == nil {
		if step, ok := totp.Validate(secret, code, time.Now(), user.TOTPLastStep); ok {
			user.TOTPLastStep = step
			return "2fa=totp", true
		}
	}
	h := hashRecoveryCode(code)
	for i, stored := range user.RecoveryCodeHashes {
		if hmac.Equal([]byte(stored), []byte(h)) {
			user.RecoveryCodeHashes = append(user.RecoveryCodeHashes[:i:i], user.RecoveryCodeHashes[i+1:]...)
			return fmt.Sprintf("2fa=recovery_code remaining=%d", len(user.RecoveryCodeHashes)), true
		}
	}
	return "", false
}

// hashRecoveryCode normalizes a recovery code (case, dashes, spaces - how
// people retype them off paper) before hashing. Plain SHA-256 rather than
// bcrypt is enough: each code is 40 random bits, single-use, and guessing
// goes through the same login lockout as everything else.
func hashRecoveryCode(code string) string {
	code = strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(code))
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// newRecoveryCodes returns fresh plaintext codes (shown to the user once)
// and their hashes (stored).
func newRecoveryCodes() (plain, hashes []string) {
	for i := 0; i < recoveryCodeCount; i++ {
		raw := randomToken(5) // 10 hex chars
		c := raw[:5] + "-" + raw[5:]
		plain = append(plain, c)
		hashes = append(hashes, hashRecoveryCode(c))
	}
	return plain, hashes
}

// handleAccountSecurityPage is the account page: two-factor status and
// setup, and the UI language (see i18n.go).
func (s *Server) handleAccountSecurityPage(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)
	data := map[string]any{
		"User":       user,
		"Error":      r.URL.Query().Get("error"),
		"Flash":      r.URL.Query().Get("flash"),
		"Required":   s.require2FA() && user.AuthSource != "sso" && !user.TOTPEnabled,
		"SSO":        user.AuthSource == "sso",
		"Remaining":  len(user.RecoveryCodeHashes),
		"Languages":  supportedLanguages,
		"CurrentTag": s.requestLanguage(r),
	}
	// Enrollment in progress: a secret has been generated but not yet
	// confirmed with a code, so show the QR code to scan.
	if !user.TOTPEnabled && len(user.TOTPSecretEnc) > 0 {
		if secret, err := s.decryptSecret(user.TOTPSecretEnc); err == nil {
			uri := totp.URI(totpIssuer, user.Username, secret)
			svg, err := totp.QRSVG(uri)
			if err == nil {
				data["QR"] = template.HTML(svg) // generated by us from a fixed alphabet - no user markup
			}
			data["Secret"] = spacedSecret(secret)
			data["Enrolling"] = true
		}
	}
	s.render(w, r, "account_security.html", data)
}

// spacedSecret groups a base32 secret into blocks of four for typing it
// into an authenticator app by hand.
func spacedSecret(secret string) string {
	var b strings.Builder
	for i, c := range secret {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func accountSecurityRedirect(w http.ResponseWriter, r *http.Request, key, msg string) {
	redirect(w, r, "/account/security?"+key+"="+url.QueryEscape(msg))
}

// handle2FASetup starts (or restarts) enrollment: a new secret is stored,
// still disabled, until handle2FAEnable sees a matching code.
func (s *Server) handle2FASetup(w http.ResponseWriter, r *http.Request) {
	user := *userFromContext(r)
	if user.AuthSource == "sso" {
		accountSecurityRedirect(w, r, "error", "this account signs in via SSO - two-factor is handled by your identity provider")
		return
	}
	if user.TOTPEnabled {
		accountSecurityRedirect(w, r, "error", "two-factor is already enabled - disable it first to enroll a new device")
		return
	}
	enc, err := s.encryptSecret(totp.GenerateSecret())
	if err != nil {
		http.Error(w, "encrypt secret: "+err.Error(), http.StatusInternalServerError)
		return
	}
	user.TOTPSecretEnc = enc
	user.TOTPLastStep = 0
	if err := s.store.PutUser(user); err != nil {
		http.Error(w, "update user: "+err.Error(), http.StatusInternalServerError)
		return
	}
	redirect(w, r, "/account/security")
}

func (s *Server) handle2FAEnable(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	user := *userFromContext(r)
	if user.TOTPEnabled || len(user.TOTPSecretEnc) == 0 {
		redirect(w, r, "/account/security")
		return
	}
	secret, err := s.decryptSecret(user.TOTPSecretEnc)
	if err != nil {
		http.Error(w, "decrypt secret: "+err.Error(), http.StatusInternalServerError)
		return
	}
	step, ok := totp.Validate(secret, r.FormValue("code"), time.Now(), 0)
	if !ok {
		s.audit(r, "user.2fa.enable", user.Username, "", errWrong2FACode)
		accountSecurityRedirect(w, r, "error", "that code doesn't match - check the time on your phone and try the next code")
		return
	}
	plain, hashes := newRecoveryCodes()
	user.TOTPEnabled = true
	user.TOTPLastStep = step
	user.RecoveryCodeHashes = hashes
	if err := s.store.PutUser(user); err != nil {
		http.Error(w, "update user: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "user.2fa.enable", user.Username, "", nil)
	s.renderRecoveryCodes(w, r, &user, plain)
}

func (s *Server) renderRecoveryCodes(w http.ResponseWriter, r *http.Request, user *store.User, codes []string) {
	s.render(w, r, "account_recovery_codes.html", map[string]any{
		"User":  user,
		"Codes": codes,
	})
}

// verifyPasswordAndCode guards disabling 2FA and regenerating recovery
// codes: a hijacked session alone (no password, no phone) mustn't be able
// to strip the second factor off an account.
func (s *Server) verifyPasswordAndCode(r *http.Request, user *store.User) error {
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(r.FormValue("password"))) != nil {
		return errors.New("current password is incorrect")
	}
	if _, ok := s.checkSecondFactor(user, r.FormValue("code")); !ok {
		return errors.New("invalid two-factor code")
	}
	return nil
}

func (s *Server) handle2FADisable(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	user := *userFromContext(r)
	if !user.TOTPEnabled {
		// Cancelling an unfinished enrollment needs no proof - nothing is
		// protected by it yet.
		user.TOTPSecretEnc = nil
		_ = s.store.PutUser(user)
		redirect(w, r, "/account/security")
		return
	}
	if err := s.verifyPasswordAndCode(r, &user); err != nil {
		s.audit(r, "user.2fa.disable", user.Username, "", err)
		accountSecurityRedirect(w, r, "error", err.Error())
		return
	}
	if s.require2FA() {
		accountSecurityRedirect(w, r, "error", "two-factor is required for every local account on this instance (Settings → General)")
		return
	}
	user.TOTPEnabled, user.TOTPSecretEnc, user.TOTPLastStep, user.RecoveryCodeHashes = false, nil, 0, nil
	if err := s.store.PutUser(user); err != nil {
		http.Error(w, "update user: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "user.2fa.disable", user.Username, "", nil)
	accountSecurityRedirect(w, r, "flash", "two-factor authentication disabled")
}

func (s *Server) handle2FARecoveryCodes(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	user := *userFromContext(r)
	if !user.TOTPEnabled {
		redirect(w, r, "/account/security")
		return
	}
	if err := s.verifyPasswordAndCode(r, &user); err != nil {
		s.audit(r, "user.2fa.recovery_codes", user.Username, "", err)
		accountSecurityRedirect(w, r, "error", err.Error())
		return
	}
	plain, hashes := newRecoveryCodes()
	user.RecoveryCodeHashes = hashes
	if err := s.store.PutUser(user); err != nil {
		http.Error(w, "update user: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "user.2fa.recovery_codes", user.Username, "", nil)
	s.renderRecoveryCodes(w, r, &user, plain)
}

// handleUserReset2FA is the admin's break-glass for a user who lost their
// phone and their recovery codes.
func (s *Server) handleUserReset2FA(w http.ResponseWriter, r *http.Request) {
	target := r.PathValue("username")
	user, err := s.store.GetUser(target)
	if err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	user.TOTPEnabled, user.TOTPSecretEnc, user.TOTPLastStep, user.RecoveryCodeHashes = false, nil, 0, nil
	if err := s.store.PutUser(user); err != nil {
		http.Error(w, "update user: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "user.2fa.reset", target, "", nil)
	redirect(w, r, "/settings/users")
}

// require2FA reports Settings -> General's "Require two-factor" switch,
// cached the same way as isUpdateCheckDisabled since requireAuth consults
// it on every request.
func (s *Server) require2FA() bool {
	s.require2FALoaded.Do(func() {
		cfg, err := s.store.GetAppSettings()
		if err == nil {
			s.require2FAValue.Store(cfg.Require2FA)
		}
	})
	return s.require2FAValue.Load()
}

func (s *Server) setRequire2FA(v bool) {
	s.require2FALoaded.Do(func() {})
	s.require2FAValue.Store(v)
}

// needs2FAEnrollment reports whether user must be sent to enroll before
// reaching path - mirroring the MustChangePassword redirect in requireAuth.
func (s *Server) needs2FAEnrollment(user *store.User, path string) bool {
	if user.TOTPEnabled || user.AuthSource == "sso" || !s.require2FA() {
		return false
	}
	return !strings.HasPrefix(path, "/account/")
}
