package claudemaster

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// issueTestClient runs the real client-init / issue sequence and returns the client's directory.
func issueTestClient(t *testing.T, stateDir, name string, days int) string {
	t.Helper()
	dir := filepath.Join(canonicalTestTempDir(t), "client")
	request, err := CreateClientRequest(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, signed, err := SignClientRequest(stateDir, request, days)
	if err != nil || signed != name {
		t.Fatalf("issue: %v (%q)", err, signed)
	}
	if err := os.WriteFile(filepath.Join(dir, clientCertFile), certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	ca, err := os.ReadFile(filepath.Join(stateDir, persistentCAFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, persistentCAFile), ca, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newTestServer(t *testing.T) (stateDir string, certs *processCertificate, proxy *Proxy) {
	t.Helper()
	stateDir = filepath.Join(canonicalTestTempDir(t), "state")
	var err error
	certs, err = loadOrCreatePersistentCertificate(stateDir, []net.IP{net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	proxy, err = startServerProxy(certs, "127.0.0.1:0", "", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "served") }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })
	return stateDir, certs, proxy
}

func clientTransport(t *testing.T, dir string, proxy *Proxy) *http.Transport {
	t.Helper()
	identity, err := LoadClientIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(identity.CertPath, identity.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	caPEM, _ := os.ReadFile(identity.CAPath)
	transport := &http.Transport{
		Proxy:           http.ProxyURL(&url.URL{Scheme: "https", Host: proxy.Addr()}),
		TLSClientConfig: &tls.Config{RootCAs: x509CertPool(caPEM), Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12},
	}
	t.Cleanup(transport.CloseIdleConnections)
	return transport
}

func post(t *testing.T, transport *http.Transport) (string, error) {
	t.Helper()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	resp, err := client.Post("https://"+masterAPIHost+"/v1/messages", "application/json", strings.NewReader(`{}`))
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return string(body), nil
}

func TestServerAcceptsAnIssuedClientAndRefusesEverythingElse(t *testing.T) {
	stateDir, _, proxy := newTestServer(t)
	good := issueTestClient(t, stateDir, "dev-box-1", 30)
	if body, err := post(t, clientTransport(t, good, proxy)); err != nil || body != "served" {
		t.Fatalf("an issued client was not served: %q %v", body, err)
	}
	// A certificate from a different server's CA is refused, as is no certificate at all.
	otherState, _, _ := newTestServer(t)
	foreign := issueTestClient(t, otherState, "dev-box-1", 30)
	transport := clientTransport(t, good, proxy)
	identity, _ := LoadClientIdentity(foreign)
	pair, _ := tls.LoadX509KeyPair(identity.CertPath, identity.KeyPath)
	transport.TLSClientConfig.Certificates = []tls.Certificate{pair}
	if body, err := post(t, transport); err == nil {
		t.Fatalf("another server's client certificate was served: %q", body)
	}
	transport = clientTransport(t, good, proxy)
	transport.TLSClientConfig.Certificates = nil
	if _, err := post(t, transport); err == nil {
		t.Fatal("a client with no certificate was served")
	}
}

func TestIssuedCertificatesAreShortLivedNamedAndNeverCarryTheKey(t *testing.T) {
	stateDir := filepath.Join(canonicalTestTempDir(t), "state")
	dir := issueTestClient(t, stateDir, "dev-box-1", 30)
	identity, err := LoadClientIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Name != "dev-box-1" || time.Until(identity.Expires) > 31*24*time.Hour || time.Until(identity.Expires) < 29*24*time.Hour {
		t.Fatalf("identity = %+v", identity)
	}
	request, _ := os.ReadFile(filepath.Join(dir, clientRequestFile))
	key, _ := os.ReadFile(filepath.Join(dir, clientKeyFile))
	if strings.Contains(string(request), "PRIVATE KEY") || len(key) == 0 {
		t.Fatal("the certificate request carries the private key, or no key was made")
	}
	for _, days := range []int{0, -1, maxClientCertDays + 1} {
		if _, _, err := SignClientRequest(stateDir, filepath.Join(dir, clientRequestFile), days); err == nil {
			t.Fatalf("%d days accepted", days)
		}
	}
	if _, err := CreateClientRequest(dir, "dev-box-1"); err == nil {
		t.Fatal("an existing client key was overwritten")
	}
	for _, name := range []string{"", "Upper", "has space", "-lead", strings.Repeat("a", 64), "a/b"} {
		if _, err := CreateClientRequest(filepath.Join(canonicalTestTempDir(t), "x"), name); err == nil {
			t.Fatalf("name %q accepted", name)
		}
	}
}

func TestSignerRefusesATamperedRequest(t *testing.T) {
	stateDir := filepath.Join(canonicalTestTempDir(t), "state")
	dir := filepath.Join(canonicalTestTempDir(t), "client")
	path, err := CreateClientRequest(dir, "dev-box-1")
	if err != nil {
		t.Fatal(err)
	}
	// Re-sign nothing: swap the name inside the DER so the signature no longer matches.
	raw, _ := os.ReadFile(path)
	block, _ := pem.Decode(raw)
	tampered := strings.Replace(string(block.Bytes), "dev-box-1", "dev-box-2", 1)
	if tampered == string(block.Bytes) {
		t.Fatal("the name was not found in the request")
	}
	block.Bytes = []byte(tampered)
	bad := filepath.Join(dir, "bad.csr")
	if err := os.WriteFile(bad, pem.EncodeToMemory(block), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SignClientRequest(stateDir, bad, 30); err == nil {
		t.Fatal("a request whose signature does not match was signed")
	}
}

func TestPersistentCAIsReloadedAndNameConstrained(t *testing.T) {
	dir := filepath.Join(canonicalTestTempDir(t), "state")
	first, err := loadOrCreatePersistentCertificate(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreatePersistentCertificate(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(first.caDER) != string(second.caDER) {
		t.Fatal("the CA changed across a restart; every client would have to be reissued")
	}
	roots := x509.NewCertPool()
	roots.AddCert(first.ca)
	sign := func(template *x509.Certificate) *x509.Certificate {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		template.SerialNumber = big.NewInt(time.Now().UnixNano())
		template.NotBefore, template.NotAfter = time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		der, err := x509.CreateCertificate(rand.Reader, template, first.ca, &key.PublicKey, first.caKey)
		if err != nil {
			t.Fatal(err)
		}
		cert, _ := x509.ParseCertificate(der)
		return cert
	}
	verify := func(cert *x509.Certificate, name string) error {
		_, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: name, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
		return err
	}
	if err := verify(sign(&x509.Certificate{Subject: pkix.Name{CommonName: masterAPIHost}, DNSNames: []string{masterAPIHost}}), masterAPIHost); err != nil {
		t.Fatalf("the CA cannot vouch for api.anthropic.com: %v", err)
	}
	if err := verify(sign(&x509.Certificate{DNSNames: []string{"github.com"}}), "github.com"); err == nil {
		t.Fatal("a leaked CA key could impersonate github.com")
	}
	// The proxy's own certificate names an IP address; the CA must be able to vouch for that.
	if err := verify(sign(&x509.Certificate{IPAddresses: []net.IP{net.ParseIP("10.1.2.3")}}), "10.1.2.3"); err != nil {
		t.Fatalf("the CA cannot vouch for the proxy's address: %v", err)
	}
	// Half a state directory is an error, not a silent new CA.
	if err := os.Remove(filepath.Join(dir, persistentCAKeyFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreatePersistentCertificate(dir, nil); err == nil {
		t.Fatal("a missing CA key was treated as a fresh install")
	}
}

func TestOnlyPrivateAddressesAreAccepted(t *testing.T) {
	for _, ok := range []string{"10.0.0.5:8443", "127.0.0.1:1", "[::1]:8443", "192.168.1.1:80", "172.16.5.5:443", "172.31.255.255:1"} {
		if _, _, err := privateEndpoint(ok); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"0.0.0.0:8443", "8.8.8.8:443", "example.com:443", "10.0.0.5", "10.0.0.5:", "172.32.0.1:1", "169.254.169.254:80", "[::]:1", ":8443"} {
		if _, _, err := privateEndpoint(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestConnectReportsAServerItCannotUse(t *testing.T) {
	stateDir, _, proxy := newTestServer(t)
	dir := issueTestClient(t, stateDir, "dev-box-1", 30)
	identity, err := LoadClientIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	host, port, _ := net.SplitHostPort(proxy.Addr())
	if err := checkServer(context.Background(), net.ParseIP(host), port, identity); err != nil {
		t.Fatalf("a healthy server was reported unusable: %v", err)
	}
	other, _, otherProxy := newTestServer(t)
	_ = other
	_, otherPort, _ := net.SplitHostPort(otherProxy.Addr())
	if err := checkServer(context.Background(), net.ParseIP(host), otherPort, identity); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("a server that does not know this box must say so: %v", err)
	}
	_ = proxy.Close()
	if err := checkServer(context.Background(), net.ParseIP(host), port, identity); err == nil || !strings.Contains(err.Error(), "is it running") {
		t.Fatalf("a server that is down must say so: %v", err)
	}
}

func TestClientIdentityRejectsAnExpiredOrForeignCertificate(t *testing.T) {
	stateDir := filepath.Join(canonicalTestTempDir(t), "state")
	dir := issueTestClient(t, stateDir, "dev-box-1", 30)
	foreignState := filepath.Join(canonicalTestTempDir(t), "state")
	foreign := issueTestClient(t, foreignState, "dev-box-1", 30)
	data, _ := os.ReadFile(filepath.Join(foreign, persistentCAFile))
	if err := os.WriteFile(filepath.Join(dir, persistentCAFile), data, 0o644); err != nil { // trust the wrong CA
		t.Fatal(err)
	}
	if _, err := LoadClientIdentity(dir); err == nil {
		t.Fatal("a certificate that the trusted CA did not sign was accepted")
	}
	if err := os.Remove(filepath.Join(dir, clientCertFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadClientIdentity(dir); err == nil || !strings.Contains(err.Error(), "client.pem") {
		t.Fatalf("a missing certificate must name the file to fetch: %v", err)
	}
}

func TestServerCertificateNamesLoopbackSoATunnelClientCanVerifyIt(t *testing.T) {
	names := serverNames(net.ParseIP("10.0.0.10"))
	dir := filepath.Join(canonicalTestTempDir(t), "state")
	certs, err := loadOrCreatePersistentCertificate(dir, names)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := certs.proxyServerCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(certs.ca)
	for _, ip := range []string{"10.0.0.10", "127.0.0.1"} {
		if _, err := leaf.Leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: ip, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
			t.Errorf("the server certificate does not verify for %s: %v", ip, err)
		}
	}
	if got := serverNames(net.ParseIP("127.0.0.1")); len(got) != 1 {
		t.Errorf("a loopback listener listed loopback twice: %v", got)
	}
}

// The server's certificate is one the client trusts, but the server does not accept THIS client's
// certificate. With TLS 1.3 the client's handshake still succeeds; the rejection is an alert on the
// next read, so checkServer must read before it reports success.
func TestConnectReportsAClientCertificateTheServerDoesNotAccept(t *testing.T) {
	stateDir, certs, _ := newTestServer(t)
	dir := issueTestClient(t, stateDir, "dev-box-1", 30)
	identity, err := LoadClientIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	otherPool := x509.NewCertPool()
	otherCerts, err := loadOrCreatePersistentCertificate(filepath.Join(canonicalTestTempDir(t), "other"), nil)
	if err != nil {
		t.Fatal(err)
	}
	otherPool.AddCert(otherCerts.ca) // the server will accept only certificates from ANOTHER CA
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		GetCertificate: certs.proxyServerCertificate, ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs: otherPool, MinVersion: tls.VersionTLS13,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { _ = conn.(*tls.Conn).Handshake(); _ = conn.Close() }()
		}
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	err = checkServer(context.Background(), net.ParseIP(host), port, identity)
	if err == nil || !strings.Contains(err.Error(), "did not accept this box's certificate") {
		t.Fatalf("a server that rejects this box's certificate must say so, not report success: %v", err)
	}
}
