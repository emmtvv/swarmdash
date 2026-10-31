// Package totp implements RFC 6238 time-based one-time passwords (the
// 6-digit, 30-second, HMAC-SHA1 flavor every authenticator app speaks) for
// swarmdash's local-account two-factor authentication, plus the otpauth://
// URI and QR code used to enroll an authenticator app.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"

	"rsc.io/qr"
)

const (
	// Period and Digits are the RFC 6238 defaults; authenticator apps
	// assume them when the otpauth URI omits them, and some ignore the URI
	// parameters entirely, so they're fixed rather than configurable.
	Period = 30
	Digits = 6

	// skewSteps accepts a code from one step either side of now, covering
	// clock drift between the server and the user's phone plus the time it
	// takes to type the code in.
	skewSteps = 1
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateSecret returns a new random 160-bit secret, base32-encoded the
// way authenticator apps expect it to be typed in or embedded in a URI.
func GenerateSecret() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failing means the system is unusable anyway
	}
	return b32.EncodeToString(b)
}

// Step is the RFC 6238 time step counter for t.
func Step(t time.Time) int64 {
	return t.Unix() / Period
}

// Code computes the code for secret at time step step.
func Code(secret string, step int64) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", fmt.Errorf("decode totp secret: %w", err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	// RFC 4226 section 5.3 dynamic truncation.
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1000000), nil
}

// Validate checks code against secret at now, allowing skewSteps of clock
// drift. A code from a step at or before lastStep is rejected even if it
// would otherwise match, so an accepted code can't be replayed. On success
// it returns the matched step, which the caller stores as the new lastStep.
func Validate(secret, code string, now time.Time, lastStep int64) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != Digits {
		return 0, false
	}
	cur := Step(now)
	for step := cur - skewSteps; step <= cur+skewSteps; step++ {
		if step <= lastStep {
			continue
		}
		want, err := Code(secret, step)
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}

// URI builds the otpauth:// enrollment URI for an authenticator app (see
// https://github.com/google/google-authenticator/wiki/Key-Uri-Format).
func URI(issuer, account, secret string) string {
	label := url.PathEscape(issuer) + ":" + url.PathEscape(account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(Digits))
	q.Set("period", fmt.Sprint(Period))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// QRSVG renders text as a QR code in a self-contained SVG document - one
// <path> of unit squares, so it can be inlined into a page without an
// image endpoint or a data: URI the CSP would have to allow.
func QRSVG(text string) (string, error) {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	const quiet = 4 // QR spec's minimum quiet zone, in modules
	n := code.Size + 2*quiet
	var path strings.Builder
	for y := 0; y < code.Size; y++ {
		for x := 0; x < code.Size; x++ {
			if code.Black(x, y) {
				fmt.Fprintf(&path, "M%d %dh1v1h-1z", x+quiet, y+quiet)
			}
		}
	}
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges"><rect width="%d" height="%d" fill="#fff"/><path d="%s" fill="#000"/></svg>`,
		n, n, n, n, path.String()), nil
}
