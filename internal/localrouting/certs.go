package localrouting

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type CertAssets struct {
	CACertPath string
	CertPath   string
	KeyPath    string
}

type CertManager struct {
	stateDir string
	store    *RouteStore
	mu       sync.Mutex
}

func NewCertManager(stateDir string, store *RouteStore) *CertManager {
	return &CertManager{stateDir: stateDir, store: store}
}

func (m *CertManager) EnsureRouteCert(host string) (CertAssets, error) {
	if m == nil {
		return CertAssets{}, fmt.Errorf("cert manager is nil")
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return CertAssets{}, fmt.Errorf("host is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := os.MkdirAll(m.certDir(), 0o755); err != nil {
		return CertAssets{}, fmt.Errorf("create cert dir: %w", err)
	}
	caCert, caKey, caAssets, err := m.ensureCA()
	if err != nil {
		return CertAssets{}, err
	}
	assets := m.routeAssets(host)
	if certValidForHost(assets.CertPath, host) {
		assets.CACertPath = caAssets.CACertPath
		return assets, nil
	}
	leafDER, keyPEM, certPEM, err := issueLeafCert(host, caCert, caKey)
	if err != nil {
		return CertAssets{}, err
	}
	_ = leafDER
	if err := os.WriteFile(assets.KeyPath, keyPEM, 0o600); err != nil {
		return CertAssets{}, fmt.Errorf("write leaf key: %w", err)
	}
	if err := os.WriteFile(assets.CertPath, certPEM, 0o644); err != nil {
		return CertAssets{}, fmt.Errorf("write leaf cert: %w", err)
	}
	assets.CACertPath = caAssets.CACertPath
	return assets, nil
}

func (m *CertManager) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(chi *tls.ClientHelloInfo) (*tls.Certificate, error) {
			host := normalizeRequestHost(chi.ServerName)
			if host == "" && chi.Conn != nil {
				host = normalizeRequestHost(chi.Conn.LocalAddr().String())
			}
			if host == "" {
				routes, err := m.store.List()
				if err == nil && len(routes) > 0 {
					host = routes[0].Host
				}
			}
			if host == "" {
				return nil, fmt.Errorf("missing server name")
			}
			routes, err := m.store.List()
			if err == nil {
				if route, ok := findRoute(host, routes); ok {
					host = route.Host
				}
			}
			assets, err := m.EnsureRouteCert(host)
			if err != nil {
				return nil, err
			}
			cert, err := tls.LoadX509KeyPair(assets.CertPath, assets.KeyPath)
			if err != nil {
				return nil, fmt.Errorf("load key pair: %w", err)
			}
			return &cert, nil
		},
	}
}

func (m *CertManager) CAPath() string {
	return filepath.Join(m.certDir(), "ca.pem")
}

func (m *CertManager) certDir() string {
	return filepath.Join(m.stateDir, "certs")
}

func (m *CertManager) ensureCA() (*x509.Certificate, *rsa.PrivateKey, CertAssets, error) {
	caCertPath := filepath.Join(m.certDir(), "ca.pem")
	caKeyPath := filepath.Join(m.certDir(), "ca.key")
	if cert, key, ok := loadCA(caCertPath, caKeyPath); ok {
		return cert, key, CertAssets{CACertPath: caCertPath}, nil
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, CertAssets{}, fmt.Errorf("generate ca key: %w", err)
	}
	serial, err := randSerial()
	if err != nil {
		return nil, nil, CertAssets{}, err
	}
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "CLIProxyAPI Local Routing CA", Organization: []string{"CLIProxyAPI"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(3650 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, CertAssets{}, fmt.Errorf("create ca cert: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(caCertPath, certPEM, 0o644); err != nil {
		return nil, nil, CertAssets{}, fmt.Errorf("write ca cert: %w", err)
	}
	if err := os.WriteFile(caKeyPath, keyPEM, 0o600); err != nil {
		return nil, nil, CertAssets{}, fmt.Errorf("write ca key: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, CertAssets{}, fmt.Errorf("parse ca cert: %w", err)
	}
	return cert, key, CertAssets{CACertPath: caCertPath}, nil
}

func (m *CertManager) routeAssets(host string) CertAssets {
	sum := sha1.Sum([]byte(host))
	token := hex.EncodeToString(sum[:8])
	base := fmt.Sprintf("%s-%s", SanitizeLabel(strings.ReplaceAll(host, ".", "-")), token)
	return CertAssets{
		CertPath: filepath.Join(m.certDir(), base+".pem"),
		KeyPath:  filepath.Join(m.certDir(), base+".key"),
	}
}

func loadCA(certPath, keyPath string) (*x509.Certificate, *rsa.PrivateKey, bool) {
	certPEM, errCert := os.ReadFile(certPath)
	keyPEM, errKey := os.ReadFile(keyPath)
	if errCert != nil || errKey != nil {
		return nil, nil, false
	}
	certBlock, _ := pem.Decode(certPEM)
	keyBlock, _ := pem.Decode(keyPEM)
	if certBlock == nil || keyBlock == nil {
		return nil, nil, false
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, false
	}
	key, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, false
	}
	return cert, key, true
}

func issueLeafCert(host string, caCert *x509.Certificate, caKey *rsa.PrivateKey) ([]byte, []byte, []byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generate leaf key: %w", err)
	}
	serial, err := randSerial()
	if err != nil {
		return nil, nil, nil, err
	}
	names := []string{host}
	if !strings.HasPrefix(host, "*.") {
		names = append(names, "*."+host)
	}
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: host,
		},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    names,
	}
	if ip := net.ParseIP(host); ip != nil {
		tpl.IPAddresses = []net.IP{ip}
		tpl.DNSNames = nil
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create leaf cert: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	certPEM = append(certPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw})...)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return der, keyPEM, certPEM, nil
}

func certValidForHost(certPath, host string) bool {
	payload, err := os.ReadFile(certPath)
	if err != nil {
		return false
	}
	block, _ := pem.Decode(payload)
	if block == nil {
		return false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	if cert.VerifyHostname(host) != nil {
		return false
	}
	wantWildcard := "*." + host
	for _, name := range cert.DNSNames {
		if strings.EqualFold(name, wantWildcard) {
			return true
		}
	}
	return false
}

func randSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}
	return serial, nil
}

func TrustInstallCommand(caCertPath string) string {
	switch runtime.GOOS {
	case "darwin":
		return fmt.Sprintf("sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain %q", caCertPath)
	case "linux":
		return fmt.Sprintf("sudo cp %q /usr/local/share/ca-certificates/cliproxyapi-local-routing.crt && sudo update-ca-certificates", caCertPath)
	case "windows":
		return fmt.Sprintf("certutil -addstore Root %q", caCertPath)
	default:
		return ""
	}
}
