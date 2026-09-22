package helps

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	gotls "crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tls "github.com/refraction-networking/utls"
)

func TestChatGPTUtlsTransportReusesHTTP2AndResumesAfterReconnect(t *testing.T) {
	var (
		stateMu          sync.Mutex
		seenConnections  = make(map[net.Conn]struct{})
		resumptions      []bool
		connectionClosed = make(chan struct{}, 1)
	)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok")
	}))
	server.EnableHTTP2 = true
	server.TLS = &gotls.Config{MinVersion: gotls.VersionTLS13}
	server.Config.ConnState = func(conn net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			select {
			case connectionClosed <- struct{}{}:
			default:
			}
			return
		}
		if state != http.StateActive {
			return
		}
		tlsConn, ok := conn.(*gotls.Conn)
		if !ok {
			return
		}
		stateMu.Lock()
		defer stateMu.Unlock()
		if _, seen := seenConnections[conn]; seen {
			return
		}
		seenConnections[conn] = struct{}{}
		resumptions = append(resumptions, tlsConn.ConnectionState().DidResume)
	}
	server.StartTLS()
	t.Cleanup(server.Close)

	var dialCount atomic.Int32
	dialer := contextDialerFunc(func(ctx context.Context, network, _ string) (net.Conn, error) {
		dialCount.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	})
	clientTLSConfig := func(host string, sessionCache tls.ClientSessionCache) *tls.Config {
		config := newChatGPTTLSConfig(host, sessionCache)
		config.InsecureSkipVerify = true // Loopback test server uses an ephemeral certificate.
		config.MinVersion = tls.VersionTLS13
		return config
	}
	roundTripper := newUtlsRoundTripperWithDialer(dialer, clientTLSConfig)
	t.Cleanup(roundTripper.CloseIdleConnections)

	request := func() {
		t.Helper()
		req, errRequest := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://chatgpt.com/backend-api/codex/responses", nil)
		if errRequest != nil {
			t.Fatal(errRequest)
		}
		resp, errRoundTrip := roundTripper.RoundTrip(req)
		if errRoundTrip != nil {
			t.Fatal(errRoundTrip)
		}
		if resp.ProtoMajor != 2 {
			t.Fatalf("HTTP protocol = %q, want HTTP/2", resp.Proto)
		}
		payload, errRead := io.ReadAll(resp.Body)
		errClose := resp.Body.Close()
		if errRead != nil {
			t.Fatal(errRead)
		}
		if errClose != nil {
			t.Fatal(errClose)
		}
		if got := strings.TrimSpace(string(payload)); got != "ok" {
			t.Fatalf("response body = %q, want %q", got, "ok")
		}
	}

	request()
	request()
	if got := dialCount.Load(); got != 1 {
		t.Fatalf("dials after two requests = %d, want 1 reusable HTTP/2 connection", got)
	}

	// Simulate an upstream connection failure. The next safe request must cause
	// the HTTP/2 pool to discard the dead connection and establish a new one.
	server.CloseClientConnections()
	select {
	case <-connectionClosed:
	case <-time.After(time.Second):
		t.Fatal("HTTP/2 client did not observe the closed upstream connection")
	}
	request()
	if got := dialCount.Load(); got != 2 {
		t.Fatalf("dials after upstream close = %d, want 2 after automatic rebuild", got)
	}

	stateMu.Lock()
	gotResumptions := append([]bool(nil), resumptions...)
	stateMu.Unlock()
	if len(gotResumptions) != 2 {
		t.Fatalf("server TLS connections = %d, want 2", len(gotResumptions))
	}
	if gotResumptions[0] {
		t.Fatal("first TLS connection unexpectedly resumed")
	}
	if !gotResumptions[1] {
		t.Fatal("rebuilt TLS connection did not resume the cached session")
	}
}

// newResumptionTestCertificate mints a short-lived self-signed leaf for the
// loopback TLS server used by the resumption test.
func newResumptionTestCertificate(t *testing.T) gotls.Certificate {
	t.Helper()
	key, errKey := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if errKey != nil {
		t.Fatalf("generate test key: %v", errKey)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "api.anthropic.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"api.anthropic.com"},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,
	}
	der, errCreate := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if errCreate != nil {
		t.Fatalf("create test certificate: %v", errCreate)
	}
	leaf, errParse := x509.ParseCertificate(der)
	if errParse != nil {
		t.Fatalf("parse test certificate: %v", errParse)
	}
	return gotls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

// TestClaudeCodeTLSSessionResumptionCompletesHandshake proves the Claude Code
// inference ClientHello can actually resume: the spec places pre_shared_key
// after the padding extension, so a malformed ordering or padding interaction
// would surface here as a handshake failure rather than a silent regression.
func TestClaudeCodeTLSSessionResumptionCompletesHandshake(t *testing.T) {
	certificate := newResumptionTestCertificate(t)
	roots := x509.NewCertPool()
	roots.AddCert(certificate.Leaf)

	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	t.Cleanup(func() {
		if errClose := listener.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			t.Errorf("close listener: %v", errClose)
		}
	})

	serverConfig := &gotls.Config{
		Certificates: []gotls.Certificate{certificate},
		MinVersion:   gotls.VersionTLS13,
	}
	go func() {
		for {
			raw, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			go func(conn net.Conn) {
				server := gotls.Server(conn, serverConfig)
				if errHandshake := server.Handshake(); errHandshake != nil {
					_ = conn.Close()
					return
				}
				// The greeting flushes the post-handshake NewSessionTicket
				// messages the client needs in order to resume.
				_, _ = server.Write([]byte("ok\n"))
				_, _ = server.Read(make([]byte, 8))
				_ = server.Close()
			}(raw)
		}
	}()

	sessionCache := tls.NewLRUClientSessionCache(claudeCodeSessionCacheCapacity)
	dial := func(round int) (resumed bool, helloLength int) {
		raw, errDial := net.Dial("tcp", listener.Addr().String())
		if errDial != nil {
			t.Fatalf("round %d dial: %v", round, errDial)
		}
		defer func() {
			if errClose := raw.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
				t.Errorf("round %d close: %v", round, errClose)
			}
		}()

		config := newClaudeCodeTLSConfig("api.anthropic.com", sessionCache)
		config.RootCAs = roots
		conn := tls.UClient(raw, config, tls.HelloCustom)
		if errPreset := conn.ApplyPreset(claudeCodeTLSClientHelloSpec()); errPreset != nil {
			t.Fatalf("round %d apply preset: %v", round, errPreset)
		}
		if errHandshake := conn.Handshake(); errHandshake != nil {
			t.Fatalf("round %d handshake: %v", round, errHandshake)
		}
		helloLength = len(conn.HandshakeState.Hello.Raw)
		if _, errRead := conn.Read(make([]byte, 8)); errRead != nil && !errors.Is(errRead, io.EOF) {
			t.Fatalf("round %d read: %v", round, errRead)
		}
		_, _ = conn.Write([]byte("bye\n"))
		return conn.ConnectionState().DidResume, helloLength
	}

	firstResumed, firstLength := dial(1)
	if firstResumed {
		t.Fatal("first handshake reported resumption without a cached session")
	}
	secondResumed, secondLength := dial(2)
	if !secondResumed {
		t.Fatal("second handshake did not resume, so the session cache is not effective")
	}

	// The padding extension absorbs the pre_shared_key bytes, so a resumed
	// ClientHello keeps the same BoringSSL padding boundary as a fresh one.
	if firstLength != secondLength {
		t.Fatalf("resumed ClientHello length = %d, want %d to match the fresh handshake", secondLength, firstLength)
	}
}
