package helps

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tls "github.com/refraction-networking/utls"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type utlsClientRoundTripFunc func(*http.Request) (*http.Response, error)

func (f utlsClientRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type trackedReadCloser struct {
	io.Reader
	closeCount int
	closeErr   error
	onClose    func()
}

func (r *trackedReadCloser) Close() error {
	r.closeCount++
	if r.onClose != nil {
		r.onClose()
	}
	return r.closeErr
}

type contextDialerFunc func(context.Context, string, string) (net.Conn, error)

func (f contextDialerFunc) Dial(network, addr string) (net.Conn, error) {
	return f(context.Background(), network, addr)
}

func (f contextDialerFunc) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return f(ctx, network, addr)
}

type trackedNetConn struct {
	net.Conn
	closeCount atomic.Int32
}

func (c *trackedNetConn) Close() error {
	c.closeCount.Add(1)
	return c.Conn.Close()
}

func TestCloseConnectionBodyClosesConnectionBeforeBodyOnce(t *testing.T) {
	bodyErr := errors.New("body close failed")
	connectionErr := errors.New("connection close failed")
	var closeOrder []string
	body := &trackedReadCloser{
		Reader:   strings.NewReader("response"),
		closeErr: bodyErr,
		onClose: func() {
			closeOrder = append(closeOrder, "body")
		},
	}
	connectionCloseCount := 0
	wrapped := &closeConnectionBody{
		ReadCloser: body,
		closeConnection: func() error {
			connectionCloseCount++
			closeOrder = append(closeOrder, "connection")
			return connectionErr
		},
	}

	payload, errRead := io.ReadAll(wrapped)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if got, want := string(payload), "response"; got != want {
		t.Fatalf("response body = %q, want %q", got, want)
	}

	errClose := wrapped.Close()
	if !errors.Is(errClose, bodyErr) {
		t.Fatalf("close error = %v, want body close error", errClose)
	}
	if !errors.Is(errClose, connectionErr) {
		t.Fatalf("close error = %v, want connection close error", errClose)
	}
	if errCloseAgain := wrapped.Close(); errCloseAgain != errClose {
		t.Fatalf("second close error = %v, want %v", errCloseAgain, errClose)
	}
	if body.closeCount != 1 {
		t.Fatalf("body close count = %d, want 1", body.closeCount)
	}
	if connectionCloseCount != 1 {
		t.Fatalf("connection close count = %d, want 1", connectionCloseCount)
	}
	if want := []string{"connection", "body"}; !reflect.DeepEqual(closeOrder, want) {
		t.Fatalf("close order = %v, want %v", closeOrder, want)
	}
}

func TestUtlsRoundTripperDialUsesRequestContext(t *testing.T) {
	dialStarted := make(chan struct{})
	roundTripper := &utlsRoundTripper{dialer: contextDialerFunc(func(ctx context.Context, _, _ string) (net.Conn, error) {
		close(dialStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	})}
	ctx, cancel := context.WithCancel(t.Context())
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/backend-api/codex/responses", nil)
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	roundTripDone := make(chan error, 1)
	go func() {
		resp, errRoundTrip := roundTripper.RoundTrip(req)
		if resp != nil && resp.Body != nil {
			errRoundTrip = errors.Join(errRoundTrip, resp.Body.Close())
		}
		roundTripDone <- errRoundTrip
	}()

	select {
	case <-dialStarted:
	case <-time.After(time.Second):
		t.Fatal("dial did not start")
	}
	cancel()
	select {
	case errRoundTrip := <-roundTripDone:
		if !errors.Is(errRoundTrip, context.Canceled) {
			t.Fatalf("RoundTrip error = %v, want context canceled", errRoundTrip)
		}
	case <-time.After(time.Second):
		t.Fatal("RoundTrip did not stop after context cancellation")
	}
}

func TestUtlsRoundTripperHandshakeUsesRequestContext(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		if errClose := clientConn.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) && !errors.Is(errClose, io.ErrClosedPipe) {
			t.Errorf("close client connection: %v", errClose)
		}
		if errClose := serverConn.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) && !errors.Is(errClose, io.ErrClosedPipe) {
			t.Errorf("close server connection: %v", errClose)
		}
	})

	trackedConn := &trackedNetConn{Conn: clientConn}
	dialDone := make(chan struct{})
	roundTripper := &utlsRoundTripper{dialer: contextDialerFunc(func(context.Context, string, string) (net.Conn, error) {
		close(dialDone)
		return trackedConn, nil
	})}
	ctx, cancel := context.WithCancel(t.Context())
	connectionDone := make(chan error, 1)
	go func() {
		h2Conn, errConnect := roundTripper.createConnection(ctx, "chatgpt.com", "chatgpt.com:443")
		if h2Conn != nil {
			errConnect = errors.Join(errConnect, h2Conn.Close())
		}
		connectionDone <- errConnect
	}()

	select {
	case <-dialDone:
	case <-time.After(time.Second):
		t.Fatal("dial did not complete")
	}
	cancel()
	select {
	case errConnect := <-connectionDone:
		if !errors.Is(errConnect, context.Canceled) {
			t.Fatalf("createConnection error = %v, want context canceled", errConnect)
		}
	case <-time.After(time.Second):
		t.Fatal("TLS handshake did not stop after context cancellation")
	}
	if got := trackedConn.closeCount.Load(); got != 1 {
		t.Fatalf("connection close count = %d, want 1", got)
	}
}

type claudeCodeTLSFingerprintFixture struct {
	ClientHelloLength   int
	JA3                 string
	JA3MD5              string
	ALPN                []string
	HTTPVersion         string
	CipherSuites        []uint16
	ExtensionTypes      []uint16
	ExtensionLengths    [][2]int
	SupportedGroups     []uint16
	PointFormats        []uint8
	SignatureAlgorithms []uint16
	SupportedVersions   []uint16
	KeyShareGroups      []uint16
}

func TestClaudeCodeTLSClientHelloSpecMatches220Capture(t *testing.T) {
	t.Parallel()

	fixture := claudeCodeTLSFingerprintFixture{
		ClientHelloLength: 508,
		JA3:               "771,4865-4866-4867-49195-49199-49196-49200-52393-52392-49161-49171-49162-49172-156-157-47-53,0-23-65281-10-11-35-16-5-13-18-51-45-43-21,29-23-24,0",
		JA3MD5:            "d871d02cecbde59abbf8f4806134addf",
		ALPN:              []string{"http/1.1"},
		HTTPVersion:       "HTTP/1.1",
		CipherSuites:      []uint16{4865, 4866, 4867, 49195, 49199, 49196, 49200, 52393, 52392, 49161, 49171, 49162, 49172, 156, 157, 47, 53},
		ExtensionTypes:    []uint16{0, 23, 65281, 10, 11, 35, 16, 5, 13, 18, 51, 45, 43, 21},
		ExtensionLengths: [][2]int{
			{0, 22}, {23, 0}, {65281, 1}, {10, 8}, {11, 2}, {35, 0}, {16, 11},
			{5, 5}, {13, 20}, {18, 0}, {51, 38}, {45, 2}, {43, 5}, {21, 231},
		},
		SupportedGroups:     []uint16{29, 23, 24},
		PointFormats:        []uint8{0},
		SignatureAlgorithms: []uint16{1027, 2052, 1025, 1283, 2053, 1281, 2054, 1537, 513},
		SupportedVersions:   []uint16{772, 771},
		KeyShareGroups:      []uint16{29},
	}

	record := captureClaudeCodeClientHello(t)
	if got := len(record) - 9; got != fixture.ClientHelloLength {
		t.Fatalf("ClientHello length = %d, want %d", got, fixture.ClientHelloLength)
	}
	if got := parseClientHelloExtensionLengths(t, record); !reflect.DeepEqual(got, fixture.ExtensionLengths) {
		t.Fatalf("extension lengths = %v, want %v", got, fixture.ExtensionLengths)
	}

	spec, errFingerprint := (&tls.Fingerprinter{}).FingerprintClientHello(record)
	if errFingerprint != nil {
		t.Fatal(errFingerprint)
	}
	actual := summarizeClaudeCodeClientHelloSpec(t, spec)
	if !reflect.DeepEqual(actual.CipherSuites, fixture.CipherSuites) {
		t.Fatalf("cipher suites = %v, want %v", actual.CipherSuites, fixture.CipherSuites)
	}
	if !reflect.DeepEqual(actual.ExtensionTypes, fixture.ExtensionTypes) {
		t.Fatalf("extension types = %v, want %v", actual.ExtensionTypes, fixture.ExtensionTypes)
	}
	if !reflect.DeepEqual(actual.ALPN, fixture.ALPN) {
		t.Fatalf("ALPN = %v, want %v", actual.ALPN, fixture.ALPN)
	}
	if !reflect.DeepEqual(actual.SupportedGroups, fixture.SupportedGroups) {
		t.Fatalf("supported groups = %v, want %v", actual.SupportedGroups, fixture.SupportedGroups)
	}
	if !reflect.DeepEqual(actual.PointFormats, fixture.PointFormats) {
		t.Fatalf("point formats = %v, want %v", actual.PointFormats, fixture.PointFormats)
	}
	if !reflect.DeepEqual(actual.SignatureAlgorithms, fixture.SignatureAlgorithms) {
		t.Fatalf("signature algorithms = %v, want %v", actual.SignatureAlgorithms, fixture.SignatureAlgorithms)
	}
	if !reflect.DeepEqual(actual.SupportedVersions, fixture.SupportedVersions) {
		t.Fatalf("supported versions = %v, want %v", actual.SupportedVersions, fixture.SupportedVersions)
	}
	if !reflect.DeepEqual(actual.KeyShareGroups, fixture.KeyShareGroups) {
		t.Fatalf("key share groups = %v, want %v", actual.KeyShareGroups, fixture.KeyShareGroups)
	}
	if actual.JA3 != fixture.JA3 || actual.JA3MD5 != fixture.JA3MD5 {
		t.Fatalf("JA3 = %q (%s), want %q (%s)", actual.JA3, actual.JA3MD5, fixture.JA3, fixture.JA3MD5)
	}

	transport, ok := newClaudeCodeRoundTripper("").(*http.Transport)
	if !ok {
		t.Fatalf("Claude Code transport type = %T, want *http.Transport", newClaudeCodeRoundTripper(""))
	}
	if transport.ForceAttemptHTTP2 {
		t.Fatal("Claude Code transport must not force HTTP/2")
	}
	if fixture.HTTPVersion != "HTTP/1.1" {
		t.Fatalf("fixture HTTP version = %q, want HTTP/1.1", fixture.HTTPVersion)
	}
}

func TestClaudeCodeTLSResumptionIsWireSafe(t *testing.T) {
	t.Parallel()

	// RFC 8446 4.2.11 requires pre_shared_key to be the final extension, after
	// the padding extension.
	spec := claudeCodeTLSClientHelloSpec()
	last := spec.Extensions[len(spec.Extensions)-1]
	if _, ok := last.(*tls.UtlsPreSharedKeyExtension); !ok {
		t.Fatalf("last inference extension = %T, want *tls.UtlsPreSharedKeyExtension", last)
	}
	if _, ok := spec.Extensions[len(spec.Extensions)-2].(*tls.UtlsPaddingExtension); !ok {
		t.Fatalf("extension before pre_shared_key = %T, want *tls.UtlsPaddingExtension", spec.Extensions[len(spec.Extensions)-2])
	}

	// Without OmitEmptyPsk uTLS refuses to marshal an empty PSK, and without
	// PreferSkipResumptionOnNilExtension a HelloCustom resumption attempt panics.
	cfg := newClaudeCodeTLSConfig("api.anthropic.com", tls.NewLRUClientSessionCache(claudeCodeSessionCacheCapacity))
	if cfg.ClientSessionCache == nil {
		t.Fatal("ClientSessionCache = nil, want a session cache so resumption is possible")
	}
	if !cfg.OmitEmptyPsk {
		t.Fatal("OmitEmptyPsk = false, want true so an unresumed ClientHello stays byte-identical")
	}
	if !cfg.PreferSkipResumptionOnNilExtension {
		t.Fatal("PreferSkipResumptionOnNilExtension = false, want true to avoid a HelloCustom resumption panic")
	}
}

func TestClaudeCodeRequestHeaderOrderMatchesNative220Capture(t *testing.T) {
	t.Parallel()

	if got, want := claudeCodeRequestHeaderOrder(http.MethodPost, "/v1/messages?beta=true"), claudeCodeMessagesHeaderOrder; !reflect.DeepEqual(got, want) {
		t.Fatalf("Messages header order = %v, want %v", got, want)
	}
	if got, want := claudeCodeRequestHeaderOrder(http.MethodPost, "/v1/messages/count_tokens?beta=true"), claudeCodeCountTokensHeaderOrder; !reflect.DeepEqual(got, want) {
		t.Fatalf("count_tokens header order = %v, want %v", got, want)
	}
	for _, name := range claudeCodeCountTokensHeaderOrder {
		if name == "X-Stainless-Timeout" {
			t.Fatal("count_tokens header order unexpectedly contains X-Stainless-Timeout")
		}
	}
}

func TestCachedClaudeCodeRoundTripperReusesTransport(t *testing.T) {
	t.Parallel()

	const proxyURL = "http://127.0.0.1:29653"
	first := cachedClaudeCodeRoundTripper(proxyURL)
	second := cachedClaudeCodeRoundTripper(proxyURL)
	if first != second {
		t.Fatal("Claude Code transport cache returned different transports for one proxy")
	}
}

func TestCachedClaudeCodeRoundTripperBoundsProxyCardinality(t *testing.T) {
	firstProxy := fmt.Sprintf("http://127.0.0.1:%d", 30000)
	first := cachedClaudeCodeRoundTripper(firstProxy)
	for index := 1; index <= claudeCodeRoundTripperCacheCapacity; index++ {
		cachedClaudeCodeRoundTripper(fmt.Sprintf("http://127.0.0.1:%d", 30000+index))
	}
	if got := claudeCodeRoundTripperCache.Len(); got > claudeCodeRoundTripperCacheCapacity {
		t.Fatalf("transport cache entries = %d, want at most %d", got, claudeCodeRoundTripperCacheCapacity)
	}
	if recreated := cachedClaudeCodeRoundTripper(firstProxy); recreated == first {
		t.Fatal("least recently used proxy transport was not evicted")
	}
}

func TestClaudeCodeTLSClientHelloCapture(t *testing.T) {
	proxyURL := os.Getenv("CPA_TLS_FP_PROXY")
	if proxyURL == "" {
		t.Skip("CPA_TLS_FP_PROXY is not set")
	}

	client := NewUtlsHTTPClient(t.Context(), nil, &cliproxyauth.Auth{ProxyURL: proxyURL}, 0)
	req, errRequest := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewBufferString(`{"model":"claude-opus-4-6","max_tokens":1,"messages":[{"role":"user","content":"x"}]}`))
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "dummy-tls-fingerprint")
	resp, errDo := client.Do(req)
	if errDo != nil {
		t.Fatal(errDo)
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatal(errClose)
	}
}

func TestFallbackRoundTripperSelectsProviderFingerprint(t *testing.T) {
	t.Parallel()

	route := func(label string) http.RoundTripper {
		return utlsClientRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"X-Test-Route": []string{label}},
				Body:       io.NopCloser(strings.NewReader("{}")),
				Request:    req,
			}, nil
		})
	}
	roundTripper := &fallbackRoundTripper{
		anthropic: route("anthropic"),
		chrome:    route("chrome"),
		fallback:  route("fallback"),
	}
	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "Anthropic HTTPS", url: "https://api.anthropic.com/v1/messages", want: "anthropic"},
		{name: "Anthropic explicit HTTPS port", url: "https://api.anthropic.com:443/v1/messages", want: "anthropic"},
		{name: "Anthropic custom port", url: "https://api.anthropic.com:8443/v1/messages", want: "fallback"},
		{name: "Anthropic userinfo", url: "https://caller@api.anthropic.com/v1/messages", want: "fallback"},
		{name: "Anthropic lookalike", url: "https://api.anthropic.com.example/v1/messages", want: "fallback"},
		{name: "ChatGPT HTTPS", url: "https://chatgpt.com/backend-api/codex/responses", want: "chrome"},
		{name: "Other HTTPS", url: "https://example.com/v1/messages", want: "fallback"},
		{name: "Anthropic HTTP", url: "http://api.anthropic.com/v1/messages", want: "fallback"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, errRequest := http.NewRequest(http.MethodGet, tt.url, nil)
			if errRequest != nil {
				t.Fatal(errRequest)
			}
			resp, errRoundTrip := roundTripper.RoundTrip(req)
			if errRoundTrip != nil {
				t.Fatal(errRoundTrip)
			}
			defer func() {
				if errClose := resp.Body.Close(); errClose != nil {
					t.Errorf("close response body: %v", errClose)
				}
			}()
			if got := resp.Header.Get("X-Test-Route"); got != tt.want {
				t.Fatalf("route = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewUtlsHTTPClientUsesContextRoundTripperForProtectedHost(t *testing.T) {
	t.Parallel()

	for _, targetURL := range []string{
		"https://api.anthropic.com/v1/messages",
		"https://chatgpt.com/backend-api/codex/responses",
	} {
		t.Run(targetURL, func(t *testing.T) {
			called := false
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", utlsClientRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				called = true
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("{}")),
					Request:    req,
				}, nil
			}))

			client := NewUtlsHTTPClient(ctx, nil, nil, 0)
			resp, err := client.Get(targetURL)
			if err != nil {
				t.Fatalf("client.Get returned error: %v", err)
			}
			if errClose := resp.Body.Close(); errClose != nil {
				t.Fatalf("response body close returned error: %v", errClose)
			}
			if !called {
				t.Fatal("expected context RoundTripper to handle protected host request")
			}
		})
	}
}

type claudeCodeClientHelloSummary struct {
	CipherSuites        []uint16
	ExtensionTypes      []uint16
	ALPN                []string
	SupportedGroups     []uint16
	PointFormats        []uint8
	SignatureAlgorithms []uint16
	SupportedVersions   []uint16
	KeyShareGroups      []uint16
	JA3                 string
	JA3MD5              string
}

func captureClaudeCodeClientHello(t *testing.T) []byte {
	t.Helper()

	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		if errClose := clientConn.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			t.Errorf("close client pipe: %v", errClose)
		}
		if errClose := serverConn.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			t.Errorf("close server pipe: %v", errClose)
		}
	})
	// Use the production config so the captured bytes reflect the real dial path,
	// including the resumption settings.
	cfg := newClaudeCodeTLSConfig("api.anthropic.com", tls.NewLRUClientSessionCache(claudeCodeSessionCacheCapacity))
	tlsConn := tls.UClient(clientConn, cfg, tls.HelloCustom)
	if errPreset := tlsConn.ApplyPreset(claudeCodeTLSClientHelloSpec()); errPreset != nil {
		t.Fatal(errPreset)
	}
	handshakeDone := make(chan error, 1)
	go func() {
		handshakeDone <- tlsConn.Handshake()
	}()
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
		t.Fatal("uTLS handshake did not exit after the capture connection closed")
	}
	return append(header, payload...)
}

func parseClientHelloExtensionLengths(t *testing.T, record []byte) [][2]int {
	t.Helper()
	if len(record) < 9 || record[0] != 22 || record[5] != 1 {
		t.Fatalf("invalid TLS ClientHello record")
	}
	body := record[9:]
	offset := 2 + 32
	if offset >= len(body) {
		t.Fatal("truncated ClientHello random")
	}
	sessionLength := int(body[offset])
	offset += 1 + sessionLength
	if offset+2 > len(body) {
		t.Fatal("truncated ClientHello cipher suites")
	}
	cipherLength := int(binary.BigEndian.Uint16(body[offset : offset+2]))
	offset += 2 + cipherLength
	if offset >= len(body) {
		t.Fatal("truncated ClientHello compression methods")
	}
	compressionLength := int(body[offset])
	offset += 1 + compressionLength
	if offset+2 > len(body) {
		t.Fatal("truncated ClientHello extensions")
	}
	extensionsLength := int(binary.BigEndian.Uint16(body[offset : offset+2]))
	offset += 2
	end := offset + extensionsLength
	if end > len(body) {
		t.Fatal("truncated ClientHello extension data")
	}
	lengths := make([][2]int, 0)
	for offset+4 <= end {
		extensionType := int(binary.BigEndian.Uint16(body[offset : offset+2]))
		extensionLength := int(binary.BigEndian.Uint16(body[offset+2 : offset+4]))
		lengths = append(lengths, [2]int{extensionType, extensionLength})
		offset += 4 + extensionLength
	}
	if offset != end {
		t.Fatal("misaligned ClientHello extension data")
	}
	return lengths
}

func summarizeClaudeCodeClientHelloSpec(t *testing.T, spec *tls.ClientHelloSpec) claudeCodeClientHelloSummary {
	t.Helper()
	summary := claudeCodeClientHelloSummary{CipherSuites: append([]uint16(nil), spec.CipherSuites...)}
	for _, extension := range spec.Extensions {
		switch ext := extension.(type) {
		case *tls.SNIExtension:
			summary.ExtensionTypes = append(summary.ExtensionTypes, 0)
		case *tls.ExtendedMasterSecretExtension:
			summary.ExtensionTypes = append(summary.ExtensionTypes, 23)
		case *tls.RenegotiationInfoExtension:
			summary.ExtensionTypes = append(summary.ExtensionTypes, 65281)
		case *tls.SupportedCurvesExtension:
			summary.ExtensionTypes = append(summary.ExtensionTypes, 10)
			for _, curve := range ext.Curves {
				summary.SupportedGroups = append(summary.SupportedGroups, uint16(curve))
			}
		case *tls.SupportedPointsExtension:
			summary.ExtensionTypes = append(summary.ExtensionTypes, 11)
			summary.PointFormats = append(summary.PointFormats, ext.SupportedPoints...)
		case *tls.SessionTicketExtension:
			summary.ExtensionTypes = append(summary.ExtensionTypes, 35)
		case *tls.ALPNExtension:
			summary.ExtensionTypes = append(summary.ExtensionTypes, 16)
			summary.ALPN = append(summary.ALPN, ext.AlpnProtocols...)
		case *tls.StatusRequestExtension:
			summary.ExtensionTypes = append(summary.ExtensionTypes, 5)
		case *tls.SignatureAlgorithmsExtension:
			summary.ExtensionTypes = append(summary.ExtensionTypes, 13)
			for _, algorithm := range ext.SupportedSignatureAlgorithms {
				summary.SignatureAlgorithms = append(summary.SignatureAlgorithms, uint16(algorithm))
			}
		case *tls.SCTExtension:
			summary.ExtensionTypes = append(summary.ExtensionTypes, 18)
		case *tls.KeyShareExtension:
			summary.ExtensionTypes = append(summary.ExtensionTypes, 51)
			for _, keyShare := range ext.KeyShares {
				summary.KeyShareGroups = append(summary.KeyShareGroups, uint16(keyShare.Group))
			}
		case *tls.PSKKeyExchangeModesExtension:
			summary.ExtensionTypes = append(summary.ExtensionTypes, 45)
		case *tls.SupportedVersionsExtension:
			summary.ExtensionTypes = append(summary.ExtensionTypes, 43)
			summary.SupportedVersions = append(summary.SupportedVersions, ext.Versions...)
		case *tls.UtlsPaddingExtension:
			summary.ExtensionTypes = append(summary.ExtensionTypes, 21)
		default:
			t.Fatalf("unexpected ClientHello extension type %T", extension)
		}
	}
	cipherStrings := make([]string, 0, len(summary.CipherSuites))
	for _, cipher := range summary.CipherSuites {
		cipherStrings = append(cipherStrings, strconv.Itoa(int(cipher)))
	}
	extensionStrings := make([]string, 0, len(summary.ExtensionTypes))
	for _, extensionType := range summary.ExtensionTypes {
		extensionStrings = append(extensionStrings, strconv.Itoa(int(extensionType)))
	}
	groupStrings := make([]string, 0, len(summary.SupportedGroups))
	for _, group := range summary.SupportedGroups {
		groupStrings = append(groupStrings, strconv.Itoa(int(group)))
	}
	pointStrings := make([]string, 0, len(summary.PointFormats))
	for _, point := range summary.PointFormats {
		pointStrings = append(pointStrings, strconv.Itoa(int(point)))
	}
	summary.JA3 = fmt.Sprintf("771,%s,%s,%s,%s", strings.Join(cipherStrings, "-"), strings.Join(extensionStrings, "-"), strings.Join(groupStrings, "-"), strings.Join(pointStrings, "-"))
	digest := md5.Sum([]byte(summary.JA3)) // #nosec G401 -- JA3 requires MD5.
	summary.JA3MD5 = hex.EncodeToString(digest[:])
	return summary
}
