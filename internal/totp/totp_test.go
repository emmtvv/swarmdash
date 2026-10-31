package totp

import (
	"encoding/base32"
	"strings"
	"testing"
	"time"
)

// RFC 6238 appendix B test vectors (SHA1, secret "12345678901234567890"),
// truncated to the 6 digits authenticator apps use.
func TestCodeRFC6238Vectors(t *testing.T) {
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	for _, tc := range []struct {
		unix int64
		want string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
	} {
		got, err := Code(secret, Step(time.Unix(tc.unix, 0)))
		if err != nil {
			t.Fatalf("Code: %v", err)
		}
		if got != tc.want {
			t.Errorf("Code at %d = %s, want %s", tc.unix, got, tc.want)
		}
	}
}

func TestValidateSkewAndReplay(t *testing.T) {
	secret := GenerateSecret()
	now := time.Unix(1_700_000_000, 0)
	prev, _ := Code(secret, Step(now)-1)

	step, ok := Validate(secret, prev, now, 0)
	if !ok || step != Step(now)-1 {
		t.Fatalf("Validate(previous step) = %d, %v; want accepted within skew", step, ok)
	}
	if _, ok := Validate(secret, prev, now, step); ok {
		t.Fatal("Validate accepted a code at or before lastStep (replay)")
	}

	old, _ := Code(secret, Step(now)-3)
	if _, ok := Validate(secret, old, now, 0); ok {
		t.Fatal("Validate accepted a code outside the skew window")
	}
	if _, ok := Validate(secret, "12345", now, 0); ok {
		t.Fatal("Validate accepted a 5-digit code")
	}
	cur, _ := Code(secret, Step(now))
	if _, ok := Validate(secret, cur[:3]+" "+cur[3:], now, 0); !ok {
		t.Fatal("Validate rejected a correct code typed with a space")
	}
}

func TestURIAndQR(t *testing.T) {
	uri := URI("swarmdash", "alice@example", "ABC")
	if !strings.HasPrefix(uri, "otpauth://totp/swarmdash:alice@example?") || !strings.Contains(uri, "secret=ABC") {
		t.Fatalf("URI = %s", uri)
	}
	svg, err := QRSVG(uri)
	if err != nil || !strings.HasPrefix(svg, "<svg") {
		t.Fatalf("QRSVG = %q, %v", svg, err)
	}
}
