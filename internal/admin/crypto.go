package admin

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"swarmdash/internal/store"
)

// encryptSecret/decryptSecret protect registry passwords at rest, keyed off
// the cluster secret (already a high-entropy value every admin/agent shares
// - reusing it avoids inventing a separate key-management story for this
// one field). AES-256-GCM, nonce prepended to the ciphertext. Both are thin
// wrappers around the key-based encryptWithKey/decryptWithKey below, which
// exist separately so RotateClusterSecret can encrypt/decrypt with two
// different keys in the same process without needing two *Server values.
func (s *Server) encryptSecret(plaintext string) ([]byte, error) {
	return encryptWithKey(s.encKey(), plaintext)
}

func (s *Server) decryptSecret(ciphertext []byte) (string, error) {
	return decryptWithKey(s.encKey(), ciphertext)
}

func (s *Server) encKey() []byte {
	return deriveEncKey(s.cfg.ClusterSecret)
}

// deriveEncKey derives the AES-256 key used to encrypt registry passwords,
// GitOps auth tokens, and the SSO client secret from the cluster secret.
func deriveEncKey(clusterSecret string) []byte {
	sum := sha256.Sum256([]byte("swarmdash-registry-cred-key:" + clusterSecret))
	return sum[:]
}

func encryptWithKey(key []byte, plaintext string) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

func decryptWithKey(key []byte, ciphertext []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	nonce, ct := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// RotateClusterSecret re-encrypts every value at rest that was encrypted
// with the old cluster secret's derived key (registry passwords, GitOps
// auth tokens, the SSO client secret) so it can be decrypted with the new
// one instead - the migration a plain "change the env var and redeploy"
// can't do on its own, since the encryption key has never been separable
// from the cluster secret. Callers are responsible for actually rolling
// the cluster secret out to every admin/agent afterwards; this only
// touches the store's ciphertext columns.
//
// Not atomic across rows: a failure partway through leaves some rows
// re-encrypted under newSecret and others still under oldSecret. Safe to
// re-run (each row is only touched if it still decrypts under the old
// key), so the fix for a partial failure is running it again, not a
// rollback.
func RotateClusterSecret(st store.Interface, oldSecret, newSecret string) error {
	oldKey := deriveEncKey(oldSecret)
	newKey := deriveEncKey(newSecret)

	creds, err := st.ListRegistryCredentials()
	if err != nil {
		return fmt.Errorf("list registry credentials: %w", err)
	}
	for _, c := range creds {
		reenc, changed, err := rotateCiphertext(oldKey, newKey, c.PasswordEnc)
		if err != nil {
			return fmt.Errorf("registry credential %q: %w", c.Server, err)
		}
		if !changed {
			continue
		}
		c.PasswordEnc = reenc
		if err := st.PutRegistryCredential(c); err != nil {
			return fmt.Errorf("save registry credential %q: %w", c.Server, err)
		}
	}

	stacks, err := st.ListGitStacks()
	if err != nil {
		return fmt.Errorf("list gitops stacks: %w", err)
	}
	for _, g := range stacks {
		reenc, changed, err := rotateCiphertext(oldKey, newKey, g.AuthTokenEnc)
		if err != nil {
			return fmt.Errorf("gitops stack %q: %w", g.StackName, err)
		}
		if !changed {
			continue
		}
		g.AuthTokenEnc = reenc
		if err := st.PutGitStack(g); err != nil {
			return fmt.Errorf("save gitops stack %q: %w", g.StackName, err)
		}
	}

	cfg, err := st.GetSSOConfig()
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("get sso config: %w", err)
	}
	if err == nil {
		reenc, changed, err := rotateCiphertext(oldKey, newKey, cfg.ClientSecretEnc)
		if err != nil {
			return fmt.Errorf("sso config: %w", err)
		}
		if changed {
			cfg.ClientSecretEnc = reenc
			if err := st.PutSSOConfig(cfg); err != nil {
				return fmt.Errorf("save sso config: %w", err)
			}
		}
	}

	return nil
}

// rotateCiphertext decrypts ciphertext with oldKey and re-encrypts the
// result with newKey. Empty ciphertext (no credential set) is left alone.
func rotateCiphertext(oldKey, newKey []byte, ciphertext []byte) (reenc []byte, changed bool, err error) {
	if len(ciphertext) == 0 {
		return nil, false, nil
	}
	plaintext, err := decryptWithKey(oldKey, ciphertext)
	if err != nil {
		return nil, false, fmt.Errorf("decrypt with old cluster secret: %w", err)
	}
	reenc, err = encryptWithKey(newKey, plaintext)
	if err != nil {
		return nil, false, fmt.Errorf("encrypt with new cluster secret: %w", err)
	}
	return reenc, true, nil
}
