package admin

import "testing"

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
