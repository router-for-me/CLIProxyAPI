package claudemaster

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"
)

// proxyTestAuth is one proxy's PKI as its tests need it: the CA that vouches for the proxy and signs
// its clients, and one client certificate. Every started proxy gets its own.
type proxyTestAuth struct {
	certs  *processCertificate
	client tls.Certificate
	roots  *x509.CertPool // trusts the proxy's own certificate
}

var proxyTestAuths sync.Map // *Proxy -> *proxyTestAuth

func newProxyTestAuth(t *testing.T) *proxyTestAuth {
	t.Helper()
	certs, err := newProcessCertificateWithClock(time.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(certs.dir) })
	certPath, keyPath, err := certs.writeClientCertificate()
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	return &proxyTestAuth{certs: certs, client: pair, roots: certs.clientPool()}
}

func (a *proxyTestAuth) apply(opts ProxyOptions) ProxyOptions {
	opts.ProxyCertificate, opts.ClientCAs = a.certs.proxyServerCertificate, a.certs.clientPool()
	return opts
}

// clientTLS is what a client of the proxy presents and trusts; origin is the certificate pool for
// the api.anthropic.com leaf the proxy serves inside the tunnel.
func (a *proxyTestAuth) clientTLS(origin *x509.CertPool) *tls.Config {
	roots := a.roots.Clone()
	if origin != nil {
		roots = origin.Clone()
		roots.AddCert(a.certs.ca)
	}
	return &tls.Config{RootCAs: roots, Certificates: []tls.Certificate{a.client}, MinVersion: tls.VersionTLS12}
}

func proxyTestAuthFor(p *Proxy) *proxyTestAuth {
	value, ok := proxyTestAuths.Load(p)
	if !ok {
		panic("proxy was not started by the test harness")
	}
	return value.(*proxyTestAuth)
}

// proxyTestDialRaw opens an authenticated TLS connection to the proxy itself, on which a test
// writes its own CONNECT.
func proxyTestDialRaw(p *Proxy) (net.Conn, error) {
	auth := proxyTestAuthFor(p)
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", p.Addr(), auth.clientTLS(nil))
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second)) // a stuck exchange fails, it does not hang
	return conn, nil
}

func TestProxyRequiresAClientCertificateSignedByItsCA(t *testing.T) {
	p, _, _ := proxyTestStart(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unexpected inference") }), nil)
	auth := proxyTestAuthFor(p)
	other := newProxyTestAuth(t)
	dial := func(config *tls.Config) error {
		conn, err := tls.Dial("tcp", p.Addr(), config)
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
		// TLS 1.3 reports a refused client certificate on the first read, not in the handshake.
		_, _ = conn.Write([]byte("CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\n\r\n"))
		_, err = http.ReadResponse(bufio.NewReader(conn), nil)
		return err
	}
	good := auth.clientTLS(nil)
	if err := dial(good); err != nil {
		t.Fatalf("a certificate from the proxy's own CA was refused: %v", err)
	}
	none := good.Clone()
	none.Certificates = nil
	if dial(none) == nil {
		t.Fatal("a client with no certificate was served")
	}
	foreign := good.Clone()
	foreign.Certificates = []tls.Certificate{other.client}
	if dial(foreign) == nil {
		t.Fatal("a certificate from another CA was accepted")
	}
}
