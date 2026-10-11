package claudemaster

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func startServerWithOpenListener(t *testing.T) (*processCertificate, *Proxy) {
	t.Helper()
	certs, err := loadOrCreatePersistentCertificate(filepath.Join(canonicalTestTempDir(t), "state"), []net.IP{net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := startServerProxy(certs, "127.0.0.1:0", "127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "served") }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })
	return certs, proxy
}

// A client arriving through an authenticating tunnel speaks plain HTTP to a loopback address and
// presents no certificate; the conversation inside the tunnel is still TLS, trusting the server CA.
func TestOpenLoopbackListenerServesWithNoClientCertificate(t *testing.T) {
	certs, proxy := startServerWithOpenListener(t)
	if proxy.OpenAddr() == "" || proxy.OpenAddr() == proxy.Addr() {
		t.Fatalf("open address %q", proxy.OpenAddr())
	}
	roots := x509.NewCertPool()
	roots.AddCert(certs.ca)
	transport := &http.Transport{
		Proxy:           http.ProxyURL(&url.URL{Scheme: "http", Host: proxy.OpenAddr()}),
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
	}
	t.Cleanup(transport.CloseIdleConnections)
	resp, err := (&http.Client{Transport: transport, Timeout: 10 * time.Second}).Post("https://"+masterAPIHost+"/v1/messages", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("a tunnel client with no certificate was not served: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if body, _ := io.ReadAll(resp.Body); string(body) != "served" {
		t.Fatalf("body = %q", body)
	}
}

func TestTheCertificateListenerStillDemandsACertificateBesideTheOpenOne(t *testing.T) {
	certs, proxy := startServerWithOpenListener(t)
	roots := x509.NewCertPool()
	roots.AddCert(certs.ca)
	conn, err := tls.Dial("tcp", proxy.Addr(), &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
	if err == nil {
		defer func() { _ = conn.Close() }()
		_, _ = conn.Write([]byte("CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\n\r\n"))
		if _, err = http.ReadResponse(bufio.NewReader(conn), nil); err == nil {
			t.Fatal("the certificate listener served a client with no certificate")
		}
	}
}

func TestTheOpenListenerRefusesAnythingButLoopback(t *testing.T) {
	certs, err := loadOrCreatePersistentCertificate(filepath.Join(canonicalTestTempDir(t), "state"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"10.0.0.10:8444", "0.0.0.0:8444", "8.8.8.8:8444", "example.com:8444", ":8444", "127.0.0.1"} {
		proxy, err := startServerProxy(certs, "127.0.0.1:0", bad, http.NotFoundHandler())
		if err == nil {
			_ = proxy.Close()
			t.Errorf("open listener on %q was accepted", bad)
		}
	}
	for _, ok := range []string{"127.0.0.1:0", "[::1]:0"} {
		if _, _, err := loopbackEndpoint(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
}

func TestConnectOpenInsistsOnLoopbackAndACAFile(t *testing.T) {
	ctx := context.Background()
	if code, err := connectOpen(ctx, ConnectOptions{Open: "10.0.0.10:8444", CAFile: "x"}, nil); err == nil || code != 2 {
		t.Fatalf("a non-loopback --open was accepted: %d %v", code, err)
	}
	if code, err := connectOpen(ctx, ConnectOptions{Open: "127.0.0.1:8444"}, nil); err == nil || code != 2 {
		t.Fatalf("--open without --ca was accepted: %d %v", code, err)
	}
	bad := filepath.Join(canonicalTestTempDir(t), "not-a-ca.pem")
	if err := os.WriteFile(bad, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := connectOpen(ctx, ConnectOptions{Open: "127.0.0.1:8444", CAFile: bad}, nil); err == nil {
		t.Fatal("a file that is not a CA certificate was accepted")
	}
}

func TestCheckOpenReportsATunnelThatIsDownOrLeadsElsewhere(t *testing.T) {
	_, proxy := startServerWithOpenListener(t)
	if err := checkOpen(context.Background(), proxy.OpenAddr()); err != nil {
		t.Fatalf("a healthy open listener was reported unusable: %v", err)
	}
	// A listener that is not a proxy at all (the tunnel leads somewhere else).
	other, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	go func() {
		for {
			conn, err := other.Accept()
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte("SSH-2.0-OpenSSH\r\n"))
			_ = conn.Close()
		}
	}()
	if err := checkOpen(context.Background(), other.Addr().String()); err == nil || !strings.Contains(err.Error(), "not a claude-master proxy") {
		t.Fatalf("a non-proxy was not recognised: %v", err)
	}
	addr := proxy.OpenAddr()
	_ = proxy.Close()
	if err := checkOpen(context.Background(), addr); err == nil || !strings.Contains(err.Error(), "tunnel") {
		t.Fatalf("a tunnel that is down must say so: %v", err)
	}
}

// Some other HTTP service answers every request with a perfectly good response. That must not pass
// for the proxy: only the proxy's own probe header does.
func TestCheckOpenIsNotFooledByAnOrdinaryHTTPService(t *testing.T) {
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "hello from some other service")
	})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	if err := checkOpen(context.Background(), listener.Addr().String()); err == nil || !strings.Contains(err.Error(), "not a claude-master proxy") {
		t.Fatalf("an ordinary HTTP service passed for the proxy: %v", err)
	}
}
