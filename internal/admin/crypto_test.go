package admin

import (
	"testing"
	"time"

	"swarmdash/internal/store"
)

func TestEncryptDecryptSecret_RoundTrip(t *testing.T) {
	s := &Server{cfg: Config{ClusterSecret: "test-secret"}}

	for _, plaintext := range []string{"", "hunter2", "a fairly long registry password with spaces & symbols !@#$%"} {
		ct, err := s.encryptSecret(plaintext)
		if err != nil {
			t.Fatalf("encryptSecret(%q): %v", plaintext, err)
		}
		got, err := s.decryptSecret(ct)
		if err != nil {
			t.Fatalf("decryptSecret round trip for %q: %v", plaintext, err)
		}
		if got != plaintext {
			t.Fatalf("round trip = %q, want %q", got, plaintext)
		}
	}
}

func TestEncryptSecret_Nondeterministic(t *testing.T) {
	s := &Server{cfg: Config{ClusterSecret: "test-secret"}}

	ct1, err := s.encryptSecret("same plaintext")
	if err != nil {
		t.Fatalf("encryptSecret: %v", err)
	}
	ct2, err := s.encryptSecret("same plaintext")
	if err != nil {
		t.Fatalf("encryptSecret: %v", err)
	}
	if string(ct1) == string(ct2) {
		t.Fatal("two encryptions of the same plaintext produced identical ciphertext (nonce not random?)")
	}

	for _, ct := range [][]byte{ct1, ct2} {
		got, err := s.decryptSecret(ct)
		if err != nil {
			t.Fatalf("decryptSecret: %v", err)
		}
		if got != "same plaintext" {
			t.Fatalf("decrypted = %q, want %q", got, "same plaintext")
		}
	}
}

func TestEncryptSecret_DifferentKeysDifferentCiphertext(t *testing.T) {
	s1 := &Server{cfg: Config{ClusterSecret: "secret-one"}}
	s2 := &Server{cfg: Config{ClusterSecret: "secret-two"}}

	ct1, err := s1.encryptSecret("plaintext")
	if err != nil {
		t.Fatalf("encryptSecret: %v", err)
	}
	ct2, err := s2.encryptSecret("plaintext")
	if err != nil {
		t.Fatalf("encryptSecret: %v", err)
	}
	if string(ct1) == string(ct2) {
		t.Fatal("different cluster secrets produced identical ciphertext")
	}
}

func TestDecryptSecret_WrongKeyFails(t *testing.T) {
	s1 := &Server{cfg: Config{ClusterSecret: "secret-one"}}
	s2 := &Server{cfg: Config{ClusterSecret: "secret-two"}}

	ct, err := s1.encryptSecret("plaintext")
	if err != nil {
		t.Fatalf("encryptSecret: %v", err)
	}
	if _, err := s2.decryptSecret(ct); err == nil {
		t.Fatal("expected decryption with wrong cluster secret to fail")
	}
}

func TestDecryptSecret_TooShortCiphertext(t *testing.T) {
	s := &Server{cfg: Config{ClusterSecret: "test-secret"}}

	if _, err := s.decryptSecret(nil); err == nil {
		t.Fatal("expected error for empty ciphertext")
	}
	if _, err := s.decryptSecret([]byte("short")); err == nil {
		t.Fatal("expected error for too-short ciphertext")
	}
}

func TestDecryptSecret_GarbageCiphertext(t *testing.T) {
	s := &Server{cfg: Config{ClusterSecret: "test-secret"}}

	// 32 bytes: longer than the GCM nonce (12 bytes) but not a valid seal.
	garbage := make([]byte, 32)
	for i := range garbage {
		garbage[i] = byte(i)
	}
	if _, err := s.decryptSecret(garbage); err == nil {
		t.Fatal("expected error for garbage ciphertext")
	}
}

// TestRotateClusterSecret_ReencryptsEveryCredentialType is the regression
// test for the "no key rotation path" gap: registry passwords, GitOps auth
// tokens, and the SSO client secret are all encrypted with a key derived
// from the cluster secret, so rotating the secret without this would leave
// every one of them permanently undecryptable.
func TestRotateClusterSecret_ReencryptsEveryCredentialType(t *testing.T) {
	const oldSecret, newSecret = "old-cluster-secret", "new-cluster-secret"
	old := &Server{cfg: Config{ClusterSecret: oldSecret}}
	st := newTestStore(t)

	regPassword, err := old.encryptSecret("registry-password")
	if err != nil {
		t.Fatalf("encrypt registry password: %v", err)
	}
	if err := st.PutRegistryCredential(store.RegistryCredential{
		Server: "registry.example.com", Username: "u", PasswordEnc: regPassword, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("put registry credential: %v", err)
	}

	gitToken, err := old.encryptSecret("gitops-token")
	if err != nil {
		t.Fatalf("encrypt gitops token: %v", err)
	}
	if err := st.PutGitStack(store.GitStack{
		ID: "g1", StackName: "app", RepoURL: "https://example.com/repo.git", AuthTokenEnc: gitToken, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("put gitops stack: %v", err)
	}

	ssoSecret, err := old.encryptSecret("sso-client-secret")
	if err != nil {
		t.Fatalf("encrypt sso client secret: %v", err)
	}
	if err := st.PutSSOConfig(store.SSOConfig{
		ID: store.SSOConfigID, Enabled: true, ClientSecretEnc: ssoSecret, UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("put sso config: %v", err)
	}

	if err := RotateClusterSecret(st, oldSecret, newSecret); err != nil {
		t.Fatalf("RotateClusterSecret: %v", err)
	}

	next := &Server{cfg: Config{ClusterSecret: newSecret}}

	cred, err := st.GetRegistryCredential("registry.example.com")
	if err != nil {
		t.Fatalf("get registry credential: %v", err)
	}
	if got, err := next.decryptSecret(cred.PasswordEnc); err != nil || got != "registry-password" {
		t.Errorf("registry password after rotation: got %q, err %v", got, err)
	}

	gs, err := st.GetGitStack("g1")
	if err != nil {
		t.Fatalf("get gitops stack: %v", err)
	}
	if got, err := next.decryptSecret(gs.AuthTokenEnc); err != nil || got != "gitops-token" {
		t.Errorf("gitops token after rotation: got %q, err %v", got, err)
	}

	cfg, err := st.GetSSOConfig()
	if err != nil {
		t.Fatalf("get sso config: %v", err)
	}
	if got, err := next.decryptSecret(cfg.ClientSecretEnc); err != nil || got != "sso-client-secret" {
		t.Errorf("sso client secret after rotation: got %q, err %v", got, err)
	}

	// The old key must no longer work - rotation actually re-encrypted,
	// not just left a stale copy readable under the old key too.
	if _, err := old.decryptSecret(cred.PasswordEnc); err == nil {
		t.Error("registry password still decrypts under the old cluster secret after rotation")
	}
}

func TestRotateClusterSecret_EmptyCredentialsLeftAlone(t *testing.T) {
	st := newTestStore(t)
	if err := st.PutRegistryCredential(store.RegistryCredential{
		Server: "registry.example.com", Username: "u", CreatedAt: time.Now(), // no PasswordEnc
	}); err != nil {
		t.Fatalf("put registry credential: %v", err)
	}

	if err := RotateClusterSecret(st, "old-secret", "new-secret"); err != nil {
		t.Fatalf("RotateClusterSecret: %v", err)
	}

	cred, err := st.GetRegistryCredential("registry.example.com")
	if err != nil {
		t.Fatalf("get registry credential: %v", err)
	}
	if len(cred.PasswordEnc) != 0 {
		t.Errorf("PasswordEnc = %v, want empty (rotation should leave unset credentials alone)", cred.PasswordEnc)
	}
}
