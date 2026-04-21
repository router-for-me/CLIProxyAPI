package misc

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCustomRootCAsFromEnvLoadsInlinePEM(t *testing.T) {
	certPEM := mustCreateTestCertificatePEM(t)
	t.Setenv("CODEX_CA_CERTIFICATE", certPEM)
	t.Setenv("SSL_CERT_FILE", "")

	pool, err := CustomRootCAsFromEnv()
	if err != nil {
		t.Fatalf("CustomRootCAsFromEnv() error = %v", err)
	}
	if pool == nil {
		t.Fatal("CustomRootCAsFromEnv() returned nil pool")
	}
	if len(pool.Subjects()) == 0 {
		t.Fatal("expected custom root CA pool to contain certificates")
	}
}

func TestCustomRootCAsFromEnvLoadsPEMFile(t *testing.T) {
	certPEM := mustCreateTestCertificatePEM(t)
	dir := t.TempDir()
	certPath := filepath.Join(dir, "custom-ca.pem")
	if err := os.WriteFile(certPath, []byte(certPEM), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("CODEX_CA_CERTIFICATE", "")
	t.Setenv("SSL_CERT_FILE", certPath)

	pool, err := CustomRootCAsFromEnv()
	if err != nil {
		t.Fatalf("CustomRootCAsFromEnv() error = %v", err)
	}
	if pool == nil {
		t.Fatal("CustomRootCAsFromEnv() returned nil pool")
	}
	if len(pool.Subjects()) == 0 {
		t.Fatal("expected custom root CA pool to contain certificates")
	}
}

func mustCreateTestCertificatePEM(t *testing.T) string {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey() error = %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "CLIProxyAPI Test CA",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("x509.CreateCertificate() error = %v", err)
	}

	return string(pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: der,
	}))
}
