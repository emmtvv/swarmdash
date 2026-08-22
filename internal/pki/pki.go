// Package pki generates a small, self-contained certificate authority for
// securing admin<->agent traffic with mutual TLS. It deliberately issues
// one shared server certificate for every agent replica and one shared
// client certificate for admin - the same "single shared credential"
// model already used for the cluster secret, rather than per-node
// identities, since agents are interchangeable and node IPs aren't stable
// enough to be useful as certificate identities anyway.
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

// AgentServerName is the fixed SAN on the shared agent certificate and the
// TLS ServerName admin uses when dialing an agent - since agents are
// reached by node IP (which varies per node and can change), verifying
// against a fixed logical name is simpler and more robust than trying to
// keep per-node SANs in sync.
const AgentServerName = "swarmdash-agent"

type Bundle struct {
	CACert []byte
	// CAKey is the CA's private key, PEM-encoded. Callers that only need
	// mTLS between admin and agent can ignore it, but it must be kept
	// (written to disk alongside CACert, same as tls_cmd.go's `init` does)
	// for Reissue to be able to mint new leaf certs later without minting
	// a whole new CA.
	CAKey     []byte
	AgentCert []byte
	AgentKey  []byte
	AdminCert []byte
	AdminKey  []byte
}

// Generate builds a fresh CA plus an agent server cert and an admin client
// cert signed by it. CA validity is long (5y) since rotating it means
// re-trusting a new root everywhere (see the package doc); leaf certs are
// valid for 1y and can be replaced without touching the CA via Reissue.
func Generate() (*Bundle, error) {
	caCert, caKey, caDER, err := generateCA()
	if err != nil {
		return nil, err
	}
	bundle, err := issueLeaves(caCert, caKey, caDER)
	if err != nil {
		return nil, err
	}
	caKeyDER, err := x509.MarshalECPrivateKey(caKey)
	if err != nil {
		return nil, err
	}
	bundle.CAKey = pemEncode("EC PRIVATE KEY", caKeyDER)
	return bundle, nil
}

// Reissue generates fresh agent/admin leaf certs (new keypairs, a new 1y
// validity window) signed by the existing CA in caCertPEM/caKeyPEM,
// leaving the CA itself untouched. Because the CA doesn't change, already-
// distributed ca.crt files stay valid trust anchors - only agent.{crt,key}
// and admin.{crt,key} need redistributing after this, unlike Generate
// which mints a new CA and so invalidates every previously issued
// certificate.
func Reissue(caCertPEM, caKeyPEM []byte) (*Bundle, error) {
	caCert, caKey, err := parseCA(caCertPEM, caKeyPEM)
	if err != nil {
		return nil, err
	}
	bundle, err := issueLeaves(caCert, caKey, caCert.Raw)
	if err != nil {
		return nil, err
	}
	bundle.CAKey = caKeyPEM
	return bundle, nil
}

func generateCA() (*x509.Certificate, *ecdsa.PrivateKey, []byte, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "swarmdash cluster CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(5, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create CA cert: %w", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, nil, nil, err
	}
	return caCert, caKey, caDER, nil
}

// parseCA decodes a CA certificate/key pair written by Generate (PEM,
// SEC1 EC private key) back into the form CreateCertificate needs.
func parseCA(caCertPEM, caKeyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certBlock, _ := pem.Decode(caCertPEM)
	if certBlock == nil {
		return nil, nil, fmt.Errorf("decode CA certificate: no PEM block found")
	}
	caCert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse CA certificate: %w", err)
	}
	if !caCert.IsCA {
		return nil, nil, fmt.Errorf("parse CA certificate: not a CA certificate")
	}
	keyBlock, _ := pem.Decode(caKeyPEM)
	if keyBlock == nil {
		return nil, nil, fmt.Errorf("decode CA key: no PEM block found")
	}
	caKey, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse CA key: %w", err)
	}
	pub, ok := caCert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&caKey.PublicKey) {
		return nil, nil, fmt.Errorf("CA key does not match CA certificate")
	}
	return caCert, caKey, nil
}

func issueLeaves(caCert *x509.Certificate, caKey *ecdsa.PrivateKey, caDER []byte) (*Bundle, error) {
	agentCertPEM, agentKeyPEM, err := issue(caCert, caKey, &x509.Certificate{
		SerialNumber: newSerial(),
		Subject:      pkix.Name{CommonName: AgentServerName},
		DNSNames:     []string{AgentServerName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	if err != nil {
		return nil, fmt.Errorf("issue agent cert: %w", err)
	}

	adminCertPEM, adminKeyPEM, err := issue(caCert, caKey, &x509.Certificate{
		SerialNumber: newSerial(),
		Subject:      pkix.Name{CommonName: "swarmdash-admin"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	if err != nil {
		return nil, fmt.Errorf("issue admin cert: %w", err)
	}

	return &Bundle{
		CACert:    pemEncode("CERTIFICATE", caDER),
		AgentCert: agentCertPEM,
		AgentKey:  agentKeyPEM,
		AdminCert: adminCertPEM,
		AdminKey:  adminKeyPEM,
	}, nil
}

// newSerial returns a random 128-bit serial number. Reissue can mint many
// leaf certs from the same CA over time, so serials need to be
// collision-resistant rather than the small fixed constants Generate's
// single CA+agent+admin triple used to get away with.
func newSerial() *big.Int {
	max := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		panic(err) // crypto/rand failing means the system is unusable anyway
	}
	return n
}

func issue(caCert *x509.Certificate, caKey *ecdsa.PrivateKey, template *x509.Certificate) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pemEncode("CERTIFICATE", der), pemEncode("EC PRIVATE KEY", keyDER), nil
}

func pemEncode(blockType string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
}
