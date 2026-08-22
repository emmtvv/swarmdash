package pki

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
)

func TestGenerate(t *testing.T) {
	bundle, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	fields := map[string][]byte{
		"CACert":    bundle.CACert,
		"AgentCert": bundle.AgentCert,
		"AgentKey":  bundle.AgentKey,
		"AdminCert": bundle.AdminCert,
		"AdminKey":  bundle.AdminKey,
	}
	for name, b := range fields {
		if len(b) == 0 {
			t.Fatalf("%s is empty", name)
		}
		block, _ := pem.Decode(b)
		if block == nil {
			t.Fatalf("%s does not decode as PEM", name)
		}
	}

	caBlock, _ := pem.Decode(bundle.CACert)
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}
	if !caCert.IsCA {
		t.Fatal("CA cert IsCA = false, want true")
	}

	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	agentBlock, _ := pem.Decode(bundle.AgentCert)
	agentCert, err := x509.ParseCertificate(agentBlock.Bytes)
	if err != nil {
		t.Fatalf("parse agent cert: %v", err)
	}
	if _, err := agentCert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("agent cert did not verify against CA pool: %v", err)
	}
	if len(agentCert.DNSNames) != 1 || agentCert.DNSNames[0] != AgentServerName {
		t.Fatalf("agent cert DNSNames = %v, want [%s]", agentCert.DNSNames, AgentServerName)
	}
	if !hasExtKeyUsage(agentCert.ExtKeyUsage, x509.ExtKeyUsageServerAuth) {
		t.Fatalf("agent cert ExtKeyUsage = %v, want ServerAuth", agentCert.ExtKeyUsage)
	}

	adminBlock, _ := pem.Decode(bundle.AdminCert)
	adminCert, err := x509.ParseCertificate(adminBlock.Bytes)
	if err != nil {
		t.Fatalf("parse admin cert: %v", err)
	}
	if _, err := adminCert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Fatalf("admin cert did not verify against CA pool: %v", err)
	}
	if !hasExtKeyUsage(adminCert.ExtKeyUsage, x509.ExtKeyUsageClientAuth) {
		t.Fatalf("admin cert ExtKeyUsage = %v, want ClientAuth", adminCert.ExtKeyUsage)
	}

	// Both leaf certs must chain to the same CA.
	if err := agentCert.CheckSignatureFrom(caCert); err != nil {
		t.Fatalf("agent cert not signed by CA: %v", err)
	}
	if err := adminCert.CheckSignatureFrom(caCert); err != nil {
		t.Fatalf("admin cert not signed by CA: %v", err)
	}
}

func TestReissue(t *testing.T) {
	original, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	renewed, err := Reissue(original.CACert, original.CAKey)
	if err != nil {
		t.Fatalf("Reissue: %v", err)
	}

	if string(renewed.CACert) != string(original.CACert) {
		t.Fatal("Reissue changed the CA certificate, want it left untouched")
	}
	if string(renewed.AgentCert) == string(original.AgentCert) {
		t.Fatal("Reissue did not change the agent certificate")
	}
	if string(renewed.AdminCert) == string(original.AdminCert) {
		t.Fatal("Reissue did not change the admin certificate")
	}

	caBlock, _ := pem.Decode(renewed.CACert)
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	agentBlock, _ := pem.Decode(renewed.AgentCert)
	agentCert, err := x509.ParseCertificate(agentBlock.Bytes)
	if err != nil {
		t.Fatalf("parse renewed agent cert: %v", err)
	}
	if _, err := agentCert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("renewed agent cert did not verify against the original CA pool: %v", err)
	}
}

func TestReissueRejectsMismatchedKey(t *testing.T) {
	a, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	b, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := Reissue(a.CACert, b.CAKey); err == nil {
		t.Fatal("Reissue with a CA key that doesn't match the CA cert succeeded, want an error")
	}
}

func hasExtKeyUsage(usages []x509.ExtKeyUsage, want x509.ExtKeyUsage) bool {
	for _, u := range usages {
		if u == want {
			return true
		}
	}
	return false
}
