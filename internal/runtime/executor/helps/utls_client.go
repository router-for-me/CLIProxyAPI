package helps

import (
	"context"
	stdtls "crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"time"

	tls "github.com/refraction-networking/utls"
	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/httpwire"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

// utlsRoundTripper implements http.RoundTripper using a Chrome fingerprint and
// a reusable HTTP/2 connection pool for ChatGPT.
type utlsRoundTripper struct {
	dialer       proxy.Dialer
	sessionCache tls.ClientSessionCache
	transport    *http2.Transport
	tlsConfig    func(string, tls.ClientSessionCache) *tls.Config

	lifecycleMu          sync.Mutex
	draining             bool
	closeIdleConnections func()
}

type utlsResponseBody struct {
	io.ReadCloser
	release func()
	once    sync.Once
}

func (b *utlsResponseBody) Read(payload []byte) (int, error) {
	read, errRead := b.ReadCloser.Read(payload)
	if errRead != nil {
		b.once.Do(b.release)
	}
	return read, errRead
}

func (b *utlsResponseBody) Close() error {
	errClose := b.ReadCloser.Close()
	b.once.Do(b.release)
	return errClose
}

const (
	chatGPTSessionCacheCapacity      = 32
	chatGPTRoundTripperCacheCapacity = 64
)

var chatGPTRoundTripperCache = internalcache.NewBoundedLRU[string, http.RoundTripper](
	chatGPTRoundTripperCacheCapacity,
	func(_ string, roundTripper http.RoundTripper) {
		if transport, ok := roundTripper.(interface{ closeWhenIdle() }); ok {
			transport.closeWhenIdle()
			return
		}
		if transport, ok := roundTripper.(interface{ CloseIdleConnections() }); ok {
			transport.CloseIdleConnections()
		}
	},
)

func newUtlsRoundTripper(proxyURL string) *utlsRoundTripper {
	var dialer proxy.Dialer = proxy.Direct
	if proxyURL != "" {
		proxyDialer, mode, errBuild := proxyutil.BuildDialer(proxyURL)
		if errBuild != nil {
			log.Errorf("utls: failed to configure proxy dialer for %q: %v", proxyutil.Redact(proxyURL), errBuild)
		} else if mode != proxyutil.ModeInherit && proxyDialer != nil {
			dialer = proxyDialer
		}
	}
	return newUtlsRoundTripperWithDialer(dialer, newChatGPTTLSConfig)
}

func newUtlsRoundTripperWithDialer(
	dialer proxy.Dialer,
	tlsConfig func(string, tls.ClientSessionCache) *tls.Config,
) *utlsRoundTripper {
	if dialer == nil {
		dialer = proxy.Direct
	}
	if tlsConfig == nil {
		tlsConfig = newChatGPTTLSConfig
	}
	roundTripper := &utlsRoundTripper{
		dialer:       dialer,
		sessionCache: tls.NewLRUClientSessionCache(chatGPTSessionCacheCapacity),
		tlsConfig:    tlsConfig,
	}
	roundTripper.transport = &http2.Transport{DialTLSContext: roundTripper.dialTLSContext}
	roundTripper.closeIdleConnections = roundTripper.transport.CloseIdleConnections
	return roundTripper
}

func newChatGPTTLSConfig(host string, sessionCache tls.ClientSessionCache) *tls.Config {
	return &tls.Config{
		ServerName:                         host,
		ClientSessionCache:                 sessionCache,
		OmitEmptyPsk:                       true,
		PreferSkipResumptionOnNilExtension: true,
	}
}

// chatGPTTLSClientHelloSpec extends the current Chrome fingerprint with an
// empty PSK extension. uTLS omits it on a cold connection and populates it from
// the per-transport session cache on later TLS 1.3 handshakes. RFC 8446
// requires pre_shared_key to remain the final ClientHello extension.
func chatGPTTLSClientHelloSpec() (*tls.ClientHelloSpec, error) {
	spec, err := tls.UTLSIdToSpec(tls.HelloChrome_Auto)
	if err != nil {
		return nil, fmt.Errorf("utls: build Chrome ClientHello: %w", err)
	}
	spec.Extensions = append(spec.Extensions, &tls.UtlsPreSharedKeyExtension{})
	return &spec, nil
}

func cachedChatGPTRoundTripper(proxyURL string) http.RoundTripper {
	return chatGPTRoundTripperCache.GetOrAdd(proxyURL, func() http.RoundTripper {
		return newUtlsRoundTripper(proxyURL)
	})
}

func (t *utlsRoundTripper) dialTLSContext(ctx context.Context, network, addr string, _ *stdtls.Config) (net.Conn, error) {
	contextDialer, ok := t.dialer.(proxy.ContextDialer)
	if !ok {
		return nil, fmt.Errorf("utls: dialer does not support context cancellation")
	}
	conn, errDial := contextDialer.DialContext(ctx, network, addr)
	if errDial != nil {
		return nil, fmt.Errorf("utls: dial upstream: %w", errDial)
	}

	host, _, errSplit := net.SplitHostPort(addr)
	if errSplit != nil {
		if errClose := conn.Close(); errClose != nil {
			log.Debugf("utls: close connection after address parse failure: %v", errClose)
		}
		return nil, fmt.Errorf("utls: split upstream address: %w", errSplit)
	}
	spec, errSpec := chatGPTTLSClientHelloSpec()
	if errSpec != nil {
		if errClose := conn.Close(); errClose != nil {
			log.Debugf("utls: close connection after ClientHello failure: %v", errClose)
		}
		return nil, errSpec
	}
	tlsConn := tls.UClient(conn, t.tlsConfig(host, t.sessionCache), tls.HelloCustom)
	if errPreset := tlsConn.ApplyPreset(spec); errPreset != nil {
		if errClose := tlsConn.Close(); errClose != nil {
			log.Debugf("utls: close connection after preset failure: %v", errClose)
		}
		return nil, fmt.Errorf("utls: apply Chrome ClientHello: %w", errPreset)
	}

	if errHandshake := tlsConn.HandshakeContext(ctx); errHandshake != nil {
		if errClose := tlsConn.Close(); errClose != nil {
			log.Debugf("utls: close connection after handshake failure: %v", errClose)
		}
		return nil, fmt.Errorf("utls: TLS handshake: %w", errHandshake)
	}
	if negotiatedProtocol := tlsConn.ConnectionState().NegotiatedProtocol; negotiatedProtocol != http2.NextProtoTLS {
		if errClose := tlsConn.Close(); errClose != nil {
			log.Debugf("utls: close connection after ALPN mismatch: %v", errClose)
		}
		return nil, fmt.Errorf("utls: unexpected ALPN protocol %q; want %q", negotiatedProtocol, http2.NextProtoTLS)
	}
	return tlsConn, nil
}

func (t *utlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.transport.RoundTrip(req)
	if err == nil {
		return t.trackResponse(resp), nil
	}
	if !isRetryableUtlsConnectionError(err) {
		return resp, err
	}
	retryReq, ok := replayableUtlsRequest(req)
	if !ok {
		return resp, err
	}

	// A connection-level failure before response headers may leave a stale
	// pooled connection behind. Close idle entries and make one immediate retry;
	// never replay a request whose HTTP semantics or body are not replay-safe.
	t.CloseIdleConnections()
	retryResp, errRetry := t.transport.RoundTrip(retryReq)
	if errRetry != nil {
		return retryResp, errRetry
	}
	return t.trackResponse(retryResp), nil
}

func (t *utlsRoundTripper) CloseIdleConnections() {
	if t.closeIdleConnections != nil {
		t.closeIdleConnections()
	}
}

// closeWhenIdle retires an evicted transport without interrupting active
// streams. Existing clients may still hold the round tripper; every response
// they finish after eviction closes any connection that has become idle.
func (t *utlsRoundTripper) closeWhenIdle() {
	t.lifecycleMu.Lock()
	t.draining = true
	t.lifecycleMu.Unlock()
	t.CloseIdleConnections()
}

func (t *utlsRoundTripper) trackResponse(resp *http.Response) *http.Response {
	if resp == nil || resp.Body == nil || resp.Body == http.NoBody {
		t.closeIdleIfDraining()
		return resp
	}

	resp.Body = &utlsResponseBody{
		ReadCloser: resp.Body,
		release:    t.releaseResponse,
	}
	return resp
}

func (t *utlsRoundTripper) releaseResponse() {
	t.lifecycleMu.Lock()
	shouldClose := t.draining
	t.lifecycleMu.Unlock()
	if shouldClose {
		t.CloseIdleConnections()
	}
}

func (t *utlsRoundTripper) closeIdleIfDraining() {
	t.lifecycleMu.Lock()
	shouldClose := t.draining
	t.lifecycleMu.Unlock()
	if shouldClose {
		t.CloseIdleConnections()
	}
}

func isRetryableUtlsConnectionError(err error) bool {
	return errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE)
}

func replayableUtlsRequest(req *http.Request) (*http.Request, bool) {
	if req == nil || (req.Body != nil && req.Body != http.NoBody && req.GetBody == nil) {
		return nil, false
	}
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		// These methods are replay-safe when the body can be reconstructed.
	default:
		if req.Header.Get("Idempotency-Key") == "" && req.Header.Get("X-Idempotency-Key") == "" {
			return nil, false
		}
	}

	retryReq := req.Clone(req.Context())
	if req.Body != nil && req.Body != http.NoBody {
		body, err := req.GetBody()
		if err != nil {
			return nil, false
		}
		retryReq.Body = body
	}
	return retryReq, true
}

// claudeCodeSessionCacheCapacity bounds the per-transport TLS session cache for
// the Anthropic inference plane.
const claudeCodeSessionCacheCapacity = 32

// newClaudeCodeTLSConfig builds the uTLS config for one inference-plane dial.
//
// OmitEmptyPsk keeps the pre_shared_key extension silent until a session is
// cached, so an unresumed ClientHello stays byte-identical to the captured
// native handshake. PreferSkipResumptionOnNilExtension turns uTLS's HelloCustom
// "resume without the matching extension" panic into a skipped resumption.
func newClaudeCodeTLSConfig(host string, sessionCache tls.ClientSessionCache) *tls.Config {
	return &tls.Config{
		ServerName:                         host,
		ClientSessionCache:                 sessionCache,
		OmitEmptyPsk:                       true,
		PreferSkipResumptionOnNilExtension: true,
	}
}

// claudeCodeTLSClientHelloSpec reproduces the deterministic Node/OpenSSL
// ClientHello emitted by Claude Code 2.1.220 on macOS arm64. Keep this spec in
// sync with a fresh native capture whenever the advertised Claude Code version
// changes.
func claudeCodeTLSClientHelloSpec() *tls.ClientHelloSpec {
	return &tls.ClientHelloSpec{
		CipherSuites: []uint16{
			tls.TLS_AES_128_GCM_SHA256,
			tls.TLS_AES_256_GCM_SHA384,
			tls.TLS_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_RSA_WITH_AES_256_CBC_SHA,
		},
		CompressionMethods: []uint8{0},
		Extensions: []tls.TLSExtension{
			&tls.SNIExtension{},
			&tls.ExtendedMasterSecretExtension{},
			&tls.RenegotiationInfoExtension{Renegotiation: tls.RenegotiateOnceAsClient},
			&tls.SupportedCurvesExtension{Curves: []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384}},
			&tls.SupportedPointsExtension{SupportedPoints: []byte{0}},
			&tls.SessionTicketExtension{},
			&tls.ALPNExtension{AlpnProtocols: []string{"http/1.1"}},
			&tls.StatusRequestExtension{},
			&tls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: []tls.SignatureScheme{
				tls.ECDSAWithP256AndSHA256,
				tls.PSSWithSHA256,
				tls.PKCS1WithSHA256,
				tls.ECDSAWithP384AndSHA384,
				tls.PSSWithSHA384,
				tls.PKCS1WithSHA384,
				tls.PSSWithSHA512,
				tls.PKCS1WithSHA512,
				tls.PKCS1WithSHA1,
			}},
			&tls.SCTExtension{},
			&tls.KeyShareExtension{KeyShares: []tls.KeyShare{{Group: tls.X25519}}},
			&tls.PSKKeyExchangeModesExtension{Modes: []uint8{tls.PskModeDHE}},
			&tls.SupportedVersionsExtension{Versions: []uint16{tls.VersionTLS13, tls.VersionTLS12}},
			&tls.UtlsPaddingExtension{GetPaddingLen: tls.BoringPaddingStyle},
			// pre_shared_key MUST be the final extension (RFC 8446 4.2.11), after
			// padding. It contributes zero bytes until a cached session exists.
			&tls.UtlsPreSharedKeyExtension{},
		},
	}
}

const claudeCodeRoundTripperCacheCapacity = 64

var claudeCodeRoundTripperCache = internalcache.NewBoundedLRU[string, http.RoundTripper](
	claudeCodeRoundTripperCacheCapacity,
	func(_ string, roundTripper http.RoundTripper) {
		if transport, ok := roundTripper.(interface{ CloseIdleConnections() }); ok {
			transport.CloseIdleConnections()
		}
	},
)

var claudeCodeMessagesHeaderOrder = []string{
	"Accept",
	"Authorization",
	"Content-Type",
	"User-Agent",
	"X-Claude-Code-Session-Id",
	"X-Stainless-Arch",
	"X-Stainless-Lang",
	"X-Stainless-OS",
	"X-Stainless-Package-Version",
	"X-Stainless-Retry-Count",
	"X-Stainless-Runtime",
	"X-Stainless-Runtime-Version",
	"X-Stainless-Timeout",
	"anthropic-beta",
	"anthropic-dangerous-direct-browser-access",
	"anthropic-version",
	"x-app",
	"x-client-request-id",
	"Connection",
	"Host",
	"Accept-Encoding",
	"Content-Length",
}

var claudeCodeCountTokensHeaderOrder = []string{
	"Accept",
	"Authorization",
	"Content-Type",
	"User-Agent",
	"X-Claude-Code-Session-Id",
	"X-Stainless-Arch",
	"X-Stainless-Lang",
	"X-Stainless-OS",
	"X-Stainless-Package-Version",
	"X-Stainless-Retry-Count",
	"X-Stainless-Runtime",
	"X-Stainless-Runtime-Version",
	"anthropic-beta",
	"anthropic-dangerous-direct-browser-access",
	"anthropic-version",
	"x-app",
	"x-client-request-id",
	"Connection",
	"Host",
	"Accept-Encoding",
	"Content-Length",
}

func claudeCodeRequestHeaderOrder(_, requestTarget string) []string {
	if strings.HasPrefix(requestTarget, "/v1/messages/count_tokens") {
		return claudeCodeCountTokensHeaderOrder
	}
	return claudeCodeMessagesHeaderOrder
}

func cachedClaudeCodeRoundTripper(proxyURL string) http.RoundTripper {
	return claudeCodeRoundTripperCache.GetOrAdd(proxyURL, func() http.RoundTripper {
		return newClaudeCodeRoundTripper(proxyURL)
	})
}

func newClaudeCodeRoundTripper(proxyURL string) http.RoundTripper {
	// The cache is scoped to this round tripper, which is already keyed by proxy,
	// so resumption never crosses proxy boundaries.
	sessionCache := tls.NewLRUClientSessionCache(claudeCodeSessionCacheCapacity)
	var dialer proxy.Dialer = proxy.Direct
	if proxyURL != "" {
		proxyDialer, mode, errBuild := proxyutil.BuildDialer(proxyURL)
		if errBuild != nil {
			log.Errorf("claude tls: failed to configure proxy dialer for %q: %v", proxyutil.Redact(proxyURL), errBuild)
		} else if mode != proxyutil.ModeInherit && proxyDialer != nil {
			dialer = proxyDialer
		}
	}

	transport := &http.Transport{
		ForceAttemptHTTP2: false,
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var (
				conn net.Conn
				err  error
			)
			if contextDialer, ok := dialer.(proxy.ContextDialer); ok {
				conn, err = contextDialer.DialContext(ctx, network, addr)
			} else {
				conn, err = dialer.Dial(network, addr)
			}
			if err != nil {
				return nil, fmt.Errorf("claude tls: dial upstream: %w", err)
			}

			host, _, errSplit := net.SplitHostPort(addr)
			if errSplit != nil {
				if errClose := conn.Close(); errClose != nil {
					log.Debugf("claude tls: close failed connection: %v", errClose)
				}
				return nil, fmt.Errorf("claude tls: split upstream address: %w", errSplit)
			}
			tlsConn := tls.UClient(conn, newClaudeCodeTLSConfig(host, sessionCache), tls.HelloCustom)
			if errPreset := tlsConn.ApplyPreset(claudeCodeTLSClientHelloSpec()); errPreset != nil {
				if errClose := tlsConn.Close(); errClose != nil {
					log.Debugf("claude tls: close connection after preset failure: %v", errClose)
				}
				return nil, fmt.Errorf("claude tls: apply Claude Code ClientHello: %w", errPreset)
			}
			if errHandshake := tlsConn.HandshakeContext(ctx); errHandshake != nil {
				if errClose := tlsConn.Close(); errClose != nil {
					log.Debugf("claude tls: close connection after handshake failure: %v", errClose)
				}
				return nil, fmt.Errorf("claude tls: handshake upstream: %w", errHandshake)
			}
			return httpwire.NewOrderedRequestConn(tlsConn, claudeCodeRequestHeaderOrder), nil
		},
	}
	return transport
}

// fallbackRoundTripper uses provider-specific TLS fingerprints for protected
// HTTPS hosts and falls back to the standard transport for all other requests.
type fallbackRoundTripper struct {
	anthropic http.RoundTripper
	chrome    http.RoundTripper
	fallback  http.RoundTripper
}

func (f *fallbackRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if IsAnthropicUpstreamURL(req.URL) {
		return f.anthropic.RoundTrip(req)
	}
	if req.URL.Scheme == "https" && strings.EqualFold(req.URL.Hostname(), "chatgpt.com") {
		return f.chrome.RoundTrip(req)
	}
	return f.fallback.RoundTrip(req)
}

// NewUtlsHTTPClient creates an HTTP client using provider-specific TLS
// fingerprints for protected hosts. It uses Claude Code's Node/OpenSSL profile
// for Anthropic and a Chrome profile for ChatGPT, with a standard-transport
// fallback for other hosts.
func NewUtlsHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	var proxyURL string
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}

	var ctxRoundTripper http.RoundTripper
	if ctx != nil {
		ctxRoundTripper, _ = ctx.Value("cliproxy.roundtripper").(http.RoundTripper)
	}

	var chromeRT http.RoundTripper = cachedChatGPTRoundTripper(proxyURL)
	var anthropicRT http.RoundTripper = cachedClaudeCodeRoundTripper(proxyURL)
	var standardTransport http.RoundTripper = http.DefaultTransport
	if proxyURL != "" {
		if transport := buildProxyTransport(proxyURL); transport != nil {
			standardTransport = transport
		}
	} else if ctxRoundTripper != nil {
		chromeRT = ctxRoundTripper
		anthropicRT = ctxRoundTripper
		standardTransport = ctxRoundTripper
	}

	client := &http.Client{
		Transport: &fallbackRoundTripper{
			anthropic: anthropicRT,
			chrome:    chromeRT,
			fallback:  standardTransport,
		},
	}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return client
}
