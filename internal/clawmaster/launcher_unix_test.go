//go:build !windows

// Unix only: the run launcher (profiles), which Windows does not support.

package clawmaster

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"testing"
)

func TestProcessCertificateOnlyTrustsAnthropicAndHasNoDiskKeys(t *testing.T) {
	certs, err := newProcessCertificate()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(certs.dir) })
	st, err := os.Stat(certs.dir)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatal("certificate directory is not private")
	}
	st, err = os.Stat(certs.caPath)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatal("certificate is not private")
	}
	entries, err := os.ReadDir(certs.dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "ca.pem" {
		t.Fatal("unexpected private key or artifact on disk")
	}
	data, err := os.ReadFile(certs.caPath)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("missing CA")
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(certs.leaf.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "api.anthropic.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "other.example"}); err == nil {
		t.Fatal("certificate covers an unrelated host")
	}
}
