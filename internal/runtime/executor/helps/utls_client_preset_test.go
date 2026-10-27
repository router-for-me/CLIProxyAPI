package helps

import (
	stdtls "crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	tls "github.com/refraction-networking/utls"
)

// presetClientHelloServer serves a self-signed TLS endpoint and records the
// ClientHello that each uTLS preset sends. The certificate is trusted through
// the round tripper's roots override so the handshake completes normally.
type presetClientHelloServer struct {
	roots  *x509.CertPool
	host   string
	addr   string
	hellos chan *stdtls.ClientHelloInfo
}

func newPresetClientHelloServer(t *testing.T) *presetClientHelloServer {
	t.Helper()
	hellos := make(chan *stdtls.ClientHelloInfo, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	server.EnableHTTP2 = true
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &stdtls.Config{
		GetConfigForClient: func(hello *stdtls.ClientHelloInfo) (*stdtls.Config, error) {
			select {
			case hellos <- hello:
			default:
			}
			return nil, nil
		},
	}
	server.StartTLS()
	t.Cleanup(server.Close)

	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	parsed, errParse := url.Parse(server.URL)
	if errParse != nil {
		t.Fatalf("parse test server URL %q: %v", server.URL, errParse)
	}
	return &presetClientHelloServer{
		roots:  roots,
		host:   parsed.Hostname(),
		addr:   parsed.Host,
		hellos: hellos,
	}
}

func (s *presetClientHelloServer) dial(t *testing.T, preset string) (*tls.UConn, *stdtls.ClientHelloInfo) {
	t.Helper()
	roundTripper := newUtlsRoundTripper("", preset)
	roundTripper.roots = s.roots
	conn, errConn := roundTripper.createConnection(t.Context(), s.host, s.addr)
	if errConn != nil {
		t.Fatalf("createConnection with preset %q: %v", preset, errConn)
	}
	t.Cleanup(func() {
		if errClose := conn.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			t.Errorf("close preset connection: %v", errClose)
		}
	})
	select {
	case hello := <-s.hellos:
		return conn, hello
	case <-t.Context().Done():
		t.Fatal("waiting for ClientHello: context done")
		return nil, nil
	}
}

func TestUtlsRoundTripperClientHelloFollowsPreset(t *testing.T) {
	server := newPresetClientHelloServer(t)

	_, legacyHello := server.dial(t, "")
	_, chromeAutoHello := server.dial(t, TLSPresetChromeAuto)
	_, chrome120Hello := server.dial(t, TLSPresetChrome120)
	_, firefoxHello := server.dial(t, TLSPresetFirefox120)
	_, safariHello := server.dial(t, TLSPresetSafari16)
	_, iosHello := server.dial(t, TLSPresetIOS16)

	legacy := clientHelloFingerprint(legacyHello)
	chromeAuto := clientHelloFingerprint(chromeAutoHello)
	chrome120 := clientHelloFingerprint(chrome120Hello)
	firefox := clientHelloFingerprint(firefoxHello)
	safari := clientHelloFingerprint(safariHello)
	ios := clientHelloFingerprint(iosHello)

	if legacy != chromeAuto {
		t.Fatalf("empty preset fingerprint differs from chrome_auto:\nempty:      %s\nchrome_auto: %s", legacy, chromeAuto)
	}
	if chrome120 == firefox {
		t.Fatalf("chrome_120 and firefox_120 produced the same ClientHello fingerprint: %s", chrome120)
	}
	if chrome120 == safari {
		t.Fatalf("chrome_120 and safari_16 produced the same ClientHello fingerprint: %s", chrome120)
	}
	if safari == firefox {
		t.Fatalf("safari_16 and firefox_120 produced the same ClientHello fingerprint: %s", safari)
	}
	if ios == chrome120 {
		t.Fatalf("ios_16 and chrome_120 produced the same ClientHello fingerprint: %s", ios)
	}
}

func TestUtlsRoundTripperHandshakeAndALPNPerPreset(t *testing.T) {
	server := newPresetClientHelloServer(t)
	presets := []string{
		"",
		TLSPresetChromeAuto,
		TLSPresetChrome120,
		TLSPresetFirefox120,
		TLSPresetSafari16,
		TLSPresetIOS16,
	}
	for _, preset := range presets {
		name := preset
		if name == "" {
			name = "unset"
		}
		t.Run(name, func(t *testing.T) {
			conn, hello := server.dial(t, preset)
			if got := conn.ConnectionState().NegotiatedProtocol; got != "h2" {
				t.Fatalf("negotiated protocol = %q, want h2", got)
			}
			if !slices.Contains(hello.SupportedProtos, "h2") {
				t.Fatalf("ClientHello ALPN = %v, want h2", hello.SupportedProtos)
			}
		})
	}

	t.Run(TLSPresetRandomChrome, func(t *testing.T) {
		resolved := ResolveAccountTLSPreset(TLSPresetRandomChrome, "auth-random-handshake", "")
		conn, _ := server.dial(t, resolved)
		if got := conn.ConnectionState().NegotiatedProtocol; got != "h2" {
			t.Fatalf("negotiated protocol = %q, want h2", got)
		}
	})
}

// clientHelloFingerprint reduces a ClientHello to the stable, order-independent
// fields that identify the browser profile. GREASE values and extension order
// are excluded because Chrome randomizes them on every connection.
func clientHelloFingerprint(hello *stdtls.ClientHelloInfo) string {
	if hello == nil {
		return "<nil>"
	}
	var builder strings.Builder
	writeUint16Values := func(values []uint16) {
		for _, value := range values {
			if isGREASE(value) {
				continue
			}
			fmt.Fprintf(&builder, "%04x,", value)
		}
		builder.WriteByte('|')
	}

	writeUint16Values(hello.CipherSuites)
	curves := make([]uint16, 0, len(hello.SupportedCurves))
	for _, curve := range hello.SupportedCurves {
		curves = append(curves, uint16(curve))
	}
	writeUint16Values(curves)
	writeUint16Values(hello.SupportedVersions)

	extensions := append([]uint16(nil), hello.Extensions...)
	slices.Sort(extensions)
	writeUint16Values(extensions)
	builder.WriteString(strings.Join(hello.SupportedProtos, ","))
	return builder.String()
}

func isGREASE(value uint16) bool {
	return value&0x0f0f == 0x0a0a
}
