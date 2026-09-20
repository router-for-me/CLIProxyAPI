package claude

import (
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	tls "github.com/refraction-networking/utls"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type claudeTestDialer struct {
	conn net.Conn
}

func (d claudeTestDialer) Dial(_, _ string) (net.Conn, error) {
	return d.conn, nil
}

func TestUtlsRoundTripperBoundsTLSHandshake(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer func() {
		if errClose := serverConn.Close(); errClose != nil {
			t.Errorf("server connection close returned error: %v", errClose)
		}
	}()

	transport := &utlsRoundTripper{dialer: claudeTestDialer{conn: clientConn}}
	ctx := context.WithValue(context.Background(), claudeRefreshHandshakeTimeoutContextKey{}, 20*time.Millisecond)
	startedAt := time.Now()
	_, err := transport.dialTLSContext(ctx, "tcp", "example.com:443")
	if err == nil {
		t.Fatal("expected TLS handshake timeout")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("error = %v, want timeout error", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("TLS handshake took %s, want less than one second", elapsed)
	}
}

func TestClaudeOAuthTLSClientHelloSpecMatchesNative220Capture(t *testing.T) {
	t.Parallel()

	const wantJA3 = "771,4865-4866-4867-49195-49199-49196-49200-52393-52392-49161-49171-49162-49172-156-157-47-53,0-23-65281-10-11-35-13-51-45-43,29-23-24,0"
	const wantJA3MD5 = "203503b7023848ab87b9836c336b8e81"
	wantCipherSuites := []uint16{4865, 4866, 4867, 49195, 49199, 49196, 49200, 52393, 52392, 49161, 49171, 49162, 49172, 156, 157, 47, 53}
	wantExtensions := []uint16{0, 23, 65281, 10, 11, 35, 13, 51, 45, 43}

	spec := claudeOAuthTLSClientHelloSpec()
	if !reflect.DeepEqual(spec.CipherSuites, wantCipherSuites) {
		t.Fatalf("cipher suites = %v, want %v", spec.CipherSuites, wantCipherSuites)
	}
	extensionTypes := claudeOAuthExtensionTypes(t, spec.Extensions)
	if !reflect.DeepEqual(extensionTypes, wantExtensions) {
		t.Fatalf("extension types = %v, want %v", extensionTypes, wantExtensions)
	}
	curves := spec.Extensions[3].(*tls.SupportedCurvesExtension).Curves
	points := spec.Extensions[4].(*tls.SupportedPointsExtension).SupportedPoints
	actualJA3 := "771," + joinClaudeOAuthUint16(spec.CipherSuites) + "," + joinClaudeOAuthUint16(extensionTypes) + "," + joinClaudeOAuthCurves(curves) + "," + joinClaudeOAuthUint8(points)
	if actualJA3 != wantJA3 {
		t.Fatalf("JA3 = %q, want %q", actualJA3, wantJA3)
	}
	if strings.Contains(actualJA3, "-16-") {
		t.Fatal("OAuth JA3 unexpectedly contains ALPN extension 16")
	}
	hash := md5.Sum([]byte(actualJA3)) // #nosec G401 -- JA3 requires MD5.
	if got := hex.EncodeToString(hash[:]); got != wantJA3MD5 {
		t.Fatalf("JA3 MD5 = %s, want %s", got, wantJA3MD5)
	}

	record := captureClaudeOAuthClientHello(t)
	if got := len(record) - 9; got != 245 {
		t.Fatalf("ClientHello length = %d, want 245", got)
	}
}

func TestClaudeOAuthTLSResumptionIsWireSafe(t *testing.T) {
	t.Parallel()

	// RFC 8446 4.2.11 requires pre_shared_key to be the final extension.
	spec := claudeOAuthTLSClientHelloSpec()
	last := spec.Extensions[len(spec.Extensions)-1]
	if _, ok := last.(*tls.UtlsPreSharedKeyExtension); !ok {
		t.Fatalf("last OAuth extension = %T, want *tls.UtlsPreSharedKeyExtension", last)
	}

	// Without OmitEmptyPsk uTLS refuses to marshal an empty PSK, and without
	// PreferSkipResumptionOnNilExtension a HelloCustom resumption attempt panics.
	cfg := newClaudeOAuthTLSConfig("api.anthropic.com", tls.NewLRUClientSessionCache(claudeOAuthSessionCacheCapacity))
	if cfg.ServerName != "api.anthropic.com" {
		t.Fatalf("ServerName = %q, want api.anthropic.com", cfg.ServerName)
	}
	if cfg.ClientSessionCache == nil {
		t.Fatal("ClientSessionCache = nil, want a session cache so resumption is possible")
	}
	if !cfg.OmitEmptyPsk {
		t.Fatal("OmitEmptyPsk = false, want true so an unresumed ClientHello stays byte-identical")
	}
	if !cfg.PreferSkipResumptionOnNilExtension {
		t.Fatal("PreferSkipResumptionOnNilExtension = false, want true to avoid a HelloCustom resumption panic")
	}

	// ClaudeAuth is rebuilt for every refresh and every executor profile check, so
	// the cache must be keyed on the proxy rather than owned by the transport;
	// otherwise every dial starts with an empty cache and never resumes.
	first := newUtlsRoundTripper(&sdkconfig.SDKConfig{ProxyURL: "http://127.0.0.1:9"})
	second := newUtlsRoundTripper(&sdkconfig.SDKConfig{ProxyURL: "http://127.0.0.1:9"})
	if first.sessionCache == nil || second.sessionCache == nil {
		t.Fatal("round tripper session cache = nil, want a shared per-proxy cache")
	}
	if first.sessionCache != second.sessionCache {
		t.Fatal("same-proxy transports have different session caches, so resumption can never hit")
	}

	// Resumption must not cross proxy boundaries.
	other := newUtlsRoundTripper(&sdkconfig.SDKConfig{ProxyURL: "http://127.0.0.1:10"})
	if first.sessionCache == other.sessionCache {
		t.Fatal("different proxies share a session cache, want per-proxy isolation")
	}

	// Same check through the real entry point: two ClaudeAuth values built the way
	// refresh and the executor profile check build them must still share a cache.
	cacheOf := func(service *ClaudeAuth) tls.ClientSessionCache {
		t.Helper()
		transport, ok := service.httpClient.Transport.(*utlsRoundTripper)
		if !ok {
			t.Fatalf("ClaudeAuth transport type = %T, want *utlsRoundTripper", service.httpClient.Transport)
		}
		return transport.sessionCache
	}
	if cacheOf(NewClaudeAuthWithProxyURL(nil, "http://127.0.0.1:11")) != cacheOf(NewClaudeAuthWithProxyURL(nil, "http://127.0.0.1:11")) {
		t.Fatal("per-operation ClaudeAuth instances do not share a session cache, so refresh can never resume")
	}
}

func TestClaudeOAuthSessionCacheBoundsProxyCardinality(t *testing.T) {
	firstProxy := "http://127.0.0.1:31000"
	first := claudeOAuthSessionCache(firstProxy)
	for index := 1; index <= claudeOAuthProxySessionCacheCapacity; index++ {
		claudeOAuthSessionCache("http://127.0.0.1:" + strconv.Itoa(31000+index))
	}
	if got := claudeOAuthSessionCaches.Len(); got > claudeOAuthProxySessionCacheCapacity {
		t.Fatalf("OAuth session caches = %d, want at most %d", got, claudeOAuthProxySessionCacheCapacity)
	}
	if recreated := claudeOAuthSessionCache(firstProxy); recreated == first {
		t.Fatal("least recently used OAuth proxy session cache was not evicted")
	}
}

func TestClaudeOAuthRequestHeaderOrderMatchesNative220Capture(t *testing.T) {
	t.Parallel()

	wantRefresh := []string{"Accept", "Content-Type", "User-Agent", "Content-Length", "Accept-Encoding", "Host", "Connection"}
	wantProfile := []string{"Accept", "Content-Type", "Authorization", "Cache-Control", "User-Agent", "Accept-Encoding", "Host", "Connection"}
	if got := claudeOAuthRequestHeaderOrder("POST", "/v1/oauth/token"); !reflect.DeepEqual(got, wantRefresh) {
		t.Fatalf("refresh header order = %v, want %v", got, wantRefresh)
	}
	if got := claudeOAuthRequestHeaderOrder("GET", "/api/oauth/profile"); !reflect.DeepEqual(got, wantProfile) {
		t.Fatalf("profile header order = %v, want %v", got, wantProfile)
	}
	// The claude_cli roles companion lookup uses the same authenticated Axios GET shape.
	if got := claudeOAuthRequestHeaderOrder("GET", "/api/oauth/claude_cli/roles"); !reflect.DeepEqual(got, wantProfile) {
		t.Fatalf("roles header order = %v, want %v", got, wantProfile)
	}
	// The authorization-code exchange is a POST and keeps the JSON-body order.
	if got := claudeOAuthRequestHeaderOrder("POST", "/api/oauth/profile"); !reflect.DeepEqual(got, wantRefresh) {
		t.Fatalf("non-GET profile target header order = %v, want %v", got, wantRefresh)
	}
}

func claudeOAuthExtensionTypes(t *testing.T, extensions []tls.TLSExtension) []uint16 {
	t.Helper()
	result := make([]uint16, 0, len(extensions))
	for _, extension := range extensions {
		switch extension.(type) {
		case *tls.SNIExtension:
			result = append(result, 0)
		case *tls.ExtendedMasterSecretExtension:
			result = append(result, 23)
		case *tls.RenegotiationInfoExtension:
			result = append(result, 65281)
		case *tls.SupportedCurvesExtension:
			result = append(result, 10)
		case *tls.SupportedPointsExtension:
			result = append(result, 11)
		case *tls.SessionTicketExtension:
			result = append(result, 35)
		case *tls.SignatureAlgorithmsExtension:
			result = append(result, 13)
		case *tls.KeyShareExtension:
			result = append(result, 51)
		case *tls.PSKKeyExchangeModesExtension:
			result = append(result, 45)
		case *tls.SupportedVersionsExtension:
			result = append(result, 43)
		case *tls.UtlsPreSharedKeyExtension:
			// pre_shared_key contributes zero bytes until a session is cached, so
			// it never appears in the fresh ClientHello the native capture covers
			// and must stay out of the JA3 extension list. The record length
			// assertion in the caller proves the byte neutrality.
			continue
		default:
			t.Fatalf("unexpected OAuth TLS extension %T", extension)
		}
	}
	return result
}

func joinClaudeOAuthUint16(values []uint16) string {
	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = strconv.Itoa(int(value))
	}
	return strings.Join(parts, "-")
}

func joinClaudeOAuthCurves(values []tls.CurveID) string {
	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = strconv.Itoa(int(value))
	}
	return strings.Join(parts, "-")
}

func joinClaudeOAuthUint8(values []uint8) string {
	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = strconv.Itoa(int(value))
	}
	return strings.Join(parts, "-")
}

func captureClaudeOAuthClientHello(t *testing.T) []byte {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		if errClose := clientConn.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			t.Errorf("close client connection: %v", errClose)
		}
		if errClose := serverConn.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			t.Errorf("close server connection: %v", errClose)
		}
	})
	// Use the production config so the captured bytes reflect the real dial path.
	cfg := newClaudeOAuthTLSConfig("api.anthropic.com", tls.NewLRUClientSessionCache(claudeOAuthSessionCacheCapacity))
	tlsConn := tls.UClient(clientConn, cfg, tls.HelloCustom)
	if errPreset := tlsConn.ApplyPreset(claudeOAuthTLSClientHelloSpec()); errPreset != nil {
		t.Fatal(errPreset)
	}
	handshakeDone := make(chan error, 1)
	go func() { handshakeDone <- tlsConn.Handshake() }()
	if errDeadline := serverConn.SetReadDeadline(time.Now().Add(5 * time.Second)); errDeadline != nil {
		t.Fatal(errDeadline)
	}
	header := make([]byte, 5)
	if _, errRead := io.ReadFull(serverConn, header); errRead != nil {
		t.Fatal(errRead)
	}
	payload := make([]byte, int(binary.BigEndian.Uint16(header[3:5])))
	if _, errRead := io.ReadFull(serverConn, payload); errRead != nil {
		t.Fatal(errRead)
	}
	if errClose := serverConn.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	select {
	case <-handshakeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("OAuth uTLS handshake did not exit")
	}
	return append(header, payload...)
}
