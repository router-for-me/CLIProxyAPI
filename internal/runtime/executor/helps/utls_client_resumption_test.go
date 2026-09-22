package helps

import (
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
	"testing"
	"time"

	tls "github.com/refraction-networking/utls"
)

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
