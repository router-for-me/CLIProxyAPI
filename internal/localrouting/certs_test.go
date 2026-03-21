package localrouting

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"testing"
)

func TestEnsureRouteCertCreatesCAAndLeaf(t *testing.T) {
	store := NewRouteStore(t.TempDir(), 1355)
	manager := NewCertManager(store.StateDir(), store)

	assets, err := manager.EnsureRouteCert("app.localhost")
	if err != nil {
		t.Fatalf("EnsureRouteCert: %v", err)
	}
	if assets.CACertPath == "" || assets.CertPath == "" || assets.KeyPath == "" {
		t.Fatal("expected cert asset paths")
	}
	if _, err := os.Stat(assets.CACertPath); err != nil {
		t.Fatalf("stat ca cert: %v", err)
	}

	payload, err := os.ReadFile(assets.CertPath)
	if err != nil {
		t.Fatalf("read leaf cert: %v", err)
	}
	block, _ := pem.Decode(payload)
	if block == nil {
		t.Fatal("decode pem leaf cert")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse leaf cert: %v", err)
	}
	if err := cert.VerifyHostname("app.localhost"); err != nil {
		t.Fatalf("verify exact host: %v", err)
	}
	if err := cert.VerifyHostname("tenant.app.localhost"); err != nil {
		t.Fatalf("verify wildcard host: %v", err)
	}
}

func TestTrustInstallCommand(t *testing.T) {
	cmd := TrustInstallCommand("/tmp/ca.pem")
	if cmd == "" {
		t.Fatal("expected trust install command")
	}
}
