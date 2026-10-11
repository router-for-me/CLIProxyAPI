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
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Authentication to the proxy is by client certificate, never by a password: the proxy's outer
// listener is TLS and requires a certificate signed by its CA before it will read a CONNECT. A
// launch makes a throwaway certificate for its own Claude child; a server issues long-lived ones
// to client boxes from a certificate request, so a private key never leaves the box that made it.
//
// Every CA here is name-constrained to the DNS name api.anthropic.com, so a leaked CA key cannot
// impersonate any other site by name. (No IP-range constraint: see newProcessCertificateIn.)

func mustCIDRs(blocks ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(blocks))
	for _, block := range blocks {
		_, network, err := net.ParseCIDR(block)
		if err != nil {
			panic(err)
		}
		out = append(out, network)
	}
	return out
}

var (
	privateRanges = mustCIDRs("127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7")
)

// privateIP accepts only a literal loopback or private-range address. A server must not listen on a
// public interface, and a client must not hand its connection to one.
func privateIP(host string) (net.IP, error) {
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || ip.IsUnspecified() {
		return nil, errors.New("the address must be a specific loopback or private IP address")
	}
	for _, network := range privateRanges {
		if network.Contains(ip) {
			return ip, nil
		}
	}
	return nil, errors.New("the address must be loopback or private (10/8, 172.16/12, 192.168/16, fc00::/7)")
}

// loopbackEndpoint splits HOST:PORT and checks that HOST is a literal loopback address. The open
// (no certificate) listener and its client both insist on it: nothing is ever offered unauthenticated
// to a network, and a client never sends a plain proxy request across one.
func loopbackEndpoint(endpoint string) (net.IP, string, error) {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil || port == "" {
		return nil, "", errors.New("expected ADDRESS:PORT")
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return nil, "", errors.New("the address must be a literal loopback address (127.0.0.1 or ::1)")
	}
	return ip, port, nil
}

// privateEndpoint splits HOST:PORT and checks that HOST is a private IP.
func privateEndpoint(endpoint string) (net.IP, string, error) {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil || port == "" {
		return nil, "", errors.New("expected ADDRESS:PORT")
	}
	ip, err := privateIP(host)
	return ip, port, err
}

const (
	persistentCAFile    = "ca.pem"
	persistentCAKeyFile = "ca.key"
	clientKeyFile       = "client.key"
	clientCertFile      = "client.pem"
	clientRequestFile   = "client.csr"
	maxClientCertDays   = 90
)

var clientNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// leafSlot caches one renewing leaf certificate.
type leafSlot struct {
	mu   sync.Mutex
	cert tls.Certificate
}

// proxyServerCertificate is the certificate the proxy presents to its own clients. It is renewed on
// new handshakes exactly like the api.anthropic.com leaf and carries IP names only.
func (p *processCertificate) proxyServerCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	p.proxySlot.mu.Lock()
	defer p.proxySlot.mu.Unlock()
	now := p.now()
	if p.proxySlot.cert.Leaf != nil && !now.Before(p.proxySlot.cert.Leaf.NotBefore) && now.Before(p.proxySlot.cert.Leaf.NotAfter.Add(-24*time.Hour)) {
		certificate := p.proxySlot.cert
		return &certificate, nil
	}
	if !now.Before(p.ca.NotAfter) {
		return nil, errors.New("CA expired; create a new one")
	}
	ips := p.proxyIPs
	if len(ips) == 0 {
		ips = []net.IP{net.IPv4(127, 0, 0, 1)}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, errors.New("cannot create proxy certificate key")
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, errors.New("cannot create certificate serial")
	}
	end := now.Add(7 * 24 * time.Hour)
	if end.After(p.ca.NotAfter) {
		end = p.ca.NotAfter
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "claude-master proxy"}, IPAddresses: ips, NotBefore: now.Add(-time.Minute), NotAfter: end, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, p.ca, &key.PublicKey, p.caKey)
	if err != nil {
		return nil, errors.New("cannot create proxy certificate")
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, errors.New("cannot inspect proxy certificate")
	}
	p.proxySlot.cert = tls.Certificate{Certificate: [][]byte{der, p.caDER}, PrivateKey: key, Leaf: parsed}
	certificate := p.proxySlot.cert
	return &certificate, nil
}

// clientPool is the set of CAs whose client certificates the proxy accepts.
func (p *processCertificate) clientPool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(p.ca)
	return pool
}

// writeClientCertificate makes this launch's own client certificate for its Claude child. It lasts as
// long as the process CA, because Claude reads it once at startup and may run for weeks; the key
// lives only in this launch's private temporary directory.
func (p *processCertificate) writeClientCertificate() (certPath, keyPath string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", errors.New("cannot create client key")
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", errors.New("cannot create certificate serial")
	}
	now := p.now()
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "claude-master launch"}, NotBefore: now.Add(-time.Minute), NotAfter: p.ca.NotAfter.Add(-24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, p.ca, &key.PublicKey, p.caKey)
	if err != nil {
		return "", "", errors.New("cannot create client certificate")
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", errors.New("cannot encode client key")
	}
	certPath, keyPath = filepath.Join(p.dir, clientCertFile), filepath.Join(p.dir, clientKeyFile)
	if err := writePrivateFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); err != nil {
		return "", "", err
	}
	if err := writePrivateFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})); err != nil {
		return "", "", err
	}
	return certPath, keyPath, nil
}

// loadOrCreatePersistentCertificate is a server's CA: created once in dir (private, 0700) and
// reloaded on every start, so client boxes keep trusting it across restarts. proxyIPs are the
// addresses the proxy certificate names.
func loadOrCreatePersistentCertificate(dir string, proxyIPs []net.IP) (*processCertificate, error) {
	if err := ownedDirectory(dir, true, true); err != nil {
		return nil, fmt.Errorf("state directory: %w", err)
	}
	certPath, keyPath := filepath.Join(dir, persistentCAFile), filepath.Join(dir, persistentCAKeyFile)
	_, errKey := os.Lstat(keyPath)
	_, errCert := os.Lstat(certPath)
	switch {
	case errKey == nil && errCert == nil:
		return loadPersistentCertificate(dir, certPath, keyPath, proxyIPs)
	case os.IsNotExist(errKey) && os.IsNotExist(errCert):
		certs, err := newProcessCertificateIn(dir, time.Now)
		if err != nil {
			return nil, err
		}
		keyDER, err := x509.MarshalECPrivateKey(certs.caKey)
		if err != nil {
			return nil, errors.New("cannot encode the CA key")
		}
		if err := writePrivateFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})); err != nil {
			return nil, err
		}
		if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certs.caDER}), 0o644); err != nil {
			return nil, errors.New("cannot write the CA certificate")
		}
		certs.caPath, certs.proxyIPs = certPath, proxyIPs
		return certs, nil
	default:
		return nil, errors.New("the state directory holds only one of the CA certificate and key; restore both or remove both")
	}
}

func loadPersistentCertificate(dir, certPath, keyPath string, proxyIPs []net.IP) (*processCertificate, error) {
	keyFile, err := openPrivateFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("CA key: %w", err)
	}
	keyPEM, errRead := readAllLimited(keyFile)
	_ = keyFile.Close()
	if errRead != nil {
		return nil, errors.New("cannot read the CA key")
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("the CA key is not PEM")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("the CA key is not an EC private key")
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, errors.New("cannot read the CA certificate")
	}
	block, _ = pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("the CA certificate is not PEM")
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !ca.IsCA {
		return nil, errors.New("the CA certificate is invalid")
	}
	if pub, ok := ca.PublicKey.(*ecdsa.PublicKey); !ok || !pub.Equal(&key.PublicKey) {
		return nil, errors.New("the CA key does not match the CA certificate")
	}
	if !time.Now().Before(ca.NotAfter) {
		return nil, errors.New("the CA certificate has expired; move the state directory aside and start again, then reissue client certificates")
	}
	return &processCertificate{dir: dir, caPath: certPath, ca: ca, caDER: block.Bytes, caKey: key, now: time.Now, proxyIPs: proxyIPs}, nil
}

// signClientRequest signs a client's certificate request (client-init made it) for at most
// maxClientCertDays. Only the name and the public key are taken from the request.
func (p *processCertificate) signClientRequest(csrPEM []byte, days int) ([]byte, string, error) {
	if days < 1 || days > maxClientCertDays {
		return nil, "", fmt.Errorf("--days must be 1 to %d: client certificates are short-lived so a lost box expires on its own", maxClientCertDays)
	}
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, "", errors.New("that file is not a certificate request")
	}
	request, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || request.CheckSignature() != nil {
		return nil, "", errors.New("the certificate request is invalid or its signature does not match")
	}
	name := request.Subject.CommonName
	if !clientNamePattern.MatchString(name) {
		return nil, "", errors.New("the request's name must be 1 to 63 lowercase letters, digits or hyphens")
	}
	pub, ok := request.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, "", errors.New("the request must carry an ECDSA P-256 key")
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, "", errors.New("cannot create certificate serial")
	}
	now := p.now()
	end := now.AddDate(0, 0, days)
	if end.After(p.ca.NotAfter) {
		end = p.ca.NotAfter
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: name}, NotBefore: now.Add(-time.Minute), NotAfter: end, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, p.ca, pub, p.caKey)
	if err != nil {
		return nil, "", errors.New("cannot sign the client certificate")
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), name, nil
}

// CreateClientRequest makes a client box's key (never leaves it) and a certificate request for the
// server to sign. It refuses to overwrite an existing key.
func CreateClientRequest(dir, name string) (string, error) {
	if !clientNamePattern.MatchString(name) {
		return "", errors.New("--name must be 1 to 63 lowercase letters, digits or hyphens")
	}
	if err := ownedDirectory(dir, true, true); err != nil {
		return "", fmt.Errorf("client directory: %w", err)
	}
	keyPath, requestPath := filepath.Join(dir, clientKeyFile), filepath.Join(dir, clientRequestFile)
	if _, err := os.Lstat(keyPath); err == nil {
		return "", errors.New("this directory already has a client key; use a new directory to start over")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", errors.New("cannot create the client key")
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", errors.New("cannot encode the client key")
	}
	requestDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: name}}, key)
	if err != nil {
		return "", errors.New("cannot create the certificate request")
	}
	if err := writePrivateFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})); err != nil {
		return "", err
	}
	if err := os.WriteFile(requestPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: requestDER}), 0o644); err != nil {
		return "", errors.New("cannot write the certificate request")
	}
	return requestPath, nil
}

// SignClientRequest is the server side of client-init: it signs the request with the CA in stateDir.
func SignClientRequest(stateDir, requestPath string, days int) (certPEM []byte, name string, err error) {
	certs, err := loadOrCreatePersistentCertificate(stateDir, nil)
	if err != nil {
		return nil, "", err
	}
	request, err := os.ReadFile(requestPath)
	if err != nil {
		return nil, "", errors.New("cannot read the certificate request")
	}
	return certs.signClientRequest(request, days)
}

// ClientIdentity is a client box's certificate, key and the server CA it trusts.
type ClientIdentity struct {
	CertPath, KeyPath, CAPath string
	Name                      string
	Expires                   time.Time
}

// LoadClientIdentity checks the directory a client box keeps: client.key and client.pem (its own
// certificate, signed by the server) and ca.pem (the server's public CA certificate).
func LoadClientIdentity(dir string) (ClientIdentity, error) {
	id := ClientIdentity{CertPath: filepath.Join(dir, clientCertFile), KeyPath: filepath.Join(dir, clientKeyFile), CAPath: filepath.Join(dir, persistentCAFile)}
	if err := ownedDirectory(dir, false, true); err != nil {
		return id, fmt.Errorf("client directory: %w", err)
	}
	keyFile, err := openPrivateFile(id.KeyPath)
	if err != nil {
		return id, fmt.Errorf("client.key: %w", err)
	}
	_ = keyFile.Close()
	read := func(path string) (*x509.Certificate, error) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("cannot read %s", filepath.Base(path))
		}
		block, _ := pem.Decode(raw)
		if block == nil || block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("%s is not a PEM certificate", filepath.Base(path))
		}
		return x509.ParseCertificate(block.Bytes)
	}
	cert, err := read(id.CertPath)
	if err != nil {
		return id, errors.New("client.pem is missing: ask the server to sign client.csr (claude-master issue) and put the result here")
	}
	ca, err := read(id.CAPath)
	if err != nil {
		return id, errors.New("ca.pem is missing: copy the server's ca.pem here")
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return id, fmt.Errorf("client.pem is not valid under ca.pem (expired, or signed by another CA): %w", err)
	}
	pair, err := tls.LoadX509KeyPair(id.CertPath, id.KeyPath)
	if err != nil || len(pair.Certificate) == 0 {
		return id, errors.New("client.pem and client.key do not belong together")
	}
	id.Name, id.Expires = cert.Subject.CommonName, cert.NotAfter
	return id, nil
}

func readAllLimited(f *os.File) ([]byte, error) {
	buf, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil || len(buf) > 1<<20 {
		return nil, errors.New("file is too large or unreadable")
	}
	return buf, nil
}
