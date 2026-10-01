package claudemaster

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type processCertificate struct {
	dir    string
	caPath string
	mu     sync.Mutex
	leaf   tls.Certificate
	ca     *x509.Certificate
	caDER  []byte
	caKey  *ecdsa.PrivateKey
	now    func() time.Time
}

func newProcessCertificate() (*processCertificate, error) {
	return newProcessCertificateWithClock(time.Now)
}

func newProcessCertificateWithClock(nowFunc func() time.Time) (*processCertificate, error) {
	dir, err := os.MkdirTemp("", "claude-master-certs-")
	if err != nil {
		return nil, errors.New("cannot create private process certificate directory")
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(dir)
		}
	}()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, errors.New("cannot create process CA key")
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, errors.New("cannot create certificate serial")
	}
	now := nowFunc()
	// Only this child trusts this CA. Its key never leaves the process, and
	// longer validity lets short-lived leaves renew without restarting Claude.
	ca := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "claude-master process CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, errors.New("cannot create process CA certificate")
	}
	caPath := filepath.Join(dir, "ca.pem")
	if err := writePrivateFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})); err != nil {
		return nil, err
	}
	certs := &processCertificate{dir: dir, caPath: caPath, ca: ca, caDER: caDER, caKey: caKey, now: nowFunc}
	if _, err := certs.getCertificate(nil); err != nil {
		return nil, err
	}
	cleanup = false
	return certs, nil
}

// getCertificate renews on new local TLS handshakes only. Existing streams and
// sockets are untouched, and no background timer or network timeout is added.
func (p *processCertificate) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if p.leaf.Leaf != nil && !now.Before(p.leaf.Leaf.NotBefore) && now.Before(p.leaf.Leaf.NotAfter.Add(-24*time.Hour)) {
		certificate := p.leaf
		return &certificate, nil
	}
	if !now.Before(p.ca.NotAfter) {
		return nil, errors.New("process CA expired; restart the launcher")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, errors.New("cannot renew process leaf key")
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, errors.New("cannot create certificate serial")
	}
	end := now.Add(7 * 24 * time.Hour)
	if end.After(p.ca.NotAfter) {
		end = p.ca.NotAfter
	}
	leaf := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: masterAPIHost}, DNSNames: []string{masterAPIHost}, NotBefore: now.Add(-time.Minute), NotAfter: end, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, p.ca, &key.PublicKey, p.caKey)
	if err != nil {
		return nil, errors.New("cannot renew process leaf certificate")
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, errors.New("cannot inspect renewed process certificate")
	}
	// Publish a new immutable value rather than mutating one a handshake may use.
	certificate := tls.Certificate{Certificate: [][]byte{der, p.caDER}, PrivateKey: key, Leaf: parsed}
	p.leaf = certificate
	return &certificate, nil
}
