// Package secretenv resolves a secret value from either a plain env var or
// a "_FILE"-suffixed env var pointing at a file (the convention used by
// Docker secrets, which are always mounted as files under /run/secrets).
package secretenv

import (
	"os"
	"strings"
)

// Resolve returns the value of envVar if set, otherwise reads and trims the
// file referenced by envVar+"_FILE" if that's set, otherwise "".
func Resolve(envVar string) (string, error) {
	if v := os.Getenv(envVar); v != "" {
		return v, nil
	}
	if path := os.Getenv(envVar + "_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	return "", nil
}
