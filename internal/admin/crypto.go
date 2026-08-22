package admin

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
)

// encryptSecret/decryptSecret protect registry passwords at rest, keyed off
// the cluster secret (already a high-entropy value every admin/agent shares
// - reusing it avoids inventing a separate key-management story for this
// one field). AES-256-GCM, nonce prepended to the ciphertext.
func (s *Server) encryptSecret(plaintext string) ([]byte, error) {
	block, err := aes.NewCipher(s.encKey())
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

func (s *Server) decryptSecret(ciphertext []byte) (string, error) {
	block, err := aes.NewCipher(s.encKey())
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

func (s *Server) encKey() []byte {
	sum := sha256.Sum256([]byte("swarmdash-registry-cred-key:" + s.cfg.ClusterSecret))
	return sum[:]
}
