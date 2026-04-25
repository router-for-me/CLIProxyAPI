package helps

import (
	"context"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tls "github.com/refraction-networking/utls"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

// utlsRoundTripper implements http.RoundTripper using utls with a Chrome TLS
// fingerprint for upstreams that apply client fingerprint checks.
type utlsRoundTripper struct {
	mu          sync.Mutex
	connections map[string]*http2.ClientConn
	pending     map[string]*sync.Cond
	dialer      proxy.Dialer
	rootCAs     *x509.CertPool
	clientHello tls.ClientHelloID
}

type fingerprintTransportCacheKey struct {
	proxyURL       string
	rootCAKey      string
	clientHelloKey string
	fallbackKey    string
	hostsKey       string
}

type fingerprintHTTPClientCacheKey struct {
	transport fingerprintTransportCacheKey
	timeout   time.Duration
}

var (
	fingerprintTransportCache  sync.Map
	fingerprintHTTPClientCache sync.Map
)

func newUtlsRoundTripper(proxyURL string, rootCAs *x509.CertPool, clientHello tls.ClientHelloID) *utlsRoundTripper {
	var dialer proxy.Dialer = proxy.Direct
	if proxyURL != "" {
		proxyDialer, mode, errBuild := proxyutil.BuildDialer(proxyURL)
		if errBuild != nil {
			log.Errorf("utls: failed to configure proxy dialer for %q: %v", proxyURL, errBuild)
		} else if mode != proxyutil.ModeInherit && proxyDialer != nil {
			dialer = proxyDialer
		}
	}
	return &utlsRoundTripper{
		connections: make(map[string]*http2.ClientConn),
		pending:     make(map[string]*sync.Cond),
		dialer:      dialer,
		rootCAs:     rootCAs,
		clientHello: clientHello,
	}
}

func (t *utlsRoundTripper) getOrCreateConnection(host, addr string) (*http2.ClientConn, error) {
	t.mu.Lock()

	if h2Conn, ok := t.connections[host]; ok && h2Conn.CanTakeNewRequest() {
		t.mu.Unlock()
		return h2Conn, nil
	}

	if cond, ok := t.pending[host]; ok {
		cond.Wait()
		if h2Conn, ok := t.connections[host]; ok && h2Conn.CanTakeNewRequest() {
			t.mu.Unlock()
			return h2Conn, nil
		}
	}

	cond := sync.NewCond(&t.mu)
	t.pending[host] = cond
	t.mu.Unlock()

	h2Conn, err := t.createConnection(host, addr)

	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.pending, host)
	cond.Broadcast()

	if err != nil {
		return nil, err
	}

	t.connections[host] = h2Conn
	return h2Conn, nil
}

func (t *utlsRoundTripper) createConnection(host, addr string) (*http2.ClientConn, error) {
	conn, err := t.dialer.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{ServerName: host, RootCAs: t.rootCAs}
	tlsConn := tls.UClient(conn, tlsConfig, t.clientHello)

	if err := tlsConn.Handshake(); err != nil {
		conn.Close()
		return nil, err
	}

	tr := &http2.Transport{}
	h2Conn, err := tr.NewClientConn(tlsConn)
	if err != nil {
		tlsConn.Close()
		return nil, err
	}

	return h2Conn, nil
}

func (t *utlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	hostname := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(hostname, port)

	h2Conn, err := t.getOrCreateConnection(hostname, addr)
	if err != nil {
		return nil, err
	}

	resp, err := h2Conn.RoundTrip(req)
	if err != nil {
		t.mu.Lock()
		if cached, ok := t.connections[hostname]; ok && cached == h2Conn {
			delete(t.connections, hostname)
		}
		t.mu.Unlock()
		return nil, err
	}

	return resp, nil
}

// anthropicFingerprintHosts contains the hosts that should use utls Chrome TLS
// fingerprinting for Claude.
var anthropicFingerprintHosts = map[string]struct{}{
	"api.anthropic.com": {},
}

// codexFingerprintHosts contains the official OpenAI/Codex hosts that should
// use utls Chrome TLS fingerprinting. OpenAI-compatible custom base_url hosts
// intentionally fall back to the normal proxy-aware transport.
var codexFingerprintHosts = map[string]struct{}{
	"api.openai.com": {},
	"chatgpt.com":    {},
}

const codexTLSClientHelloEnvVar = "CODEX_TLS_CLIENT_HELLO"

// fallbackRoundTripper uses utls for selected HTTPS hosts and falls back to
// standard transport for all other requests.
type fallbackRoundTripper struct {
	utls               *utlsRoundTripper
	fallback           http.RoundTripper
	fingerprintedHosts map[string]struct{}
}

func (f *fallbackRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme == "https" {
		if _, ok := f.fingerprintedHosts[strings.ToLower(req.URL.Hostname())]; ok {
			return f.utls.RoundTrip(req)
		}
	}
	return f.fallback.RoundTrip(req)
}

// NewUtlsHTTPClient creates an HTTP client using utls Chrome TLS fingerprint.
// Use this for Claude API requests to match real Claude Code's TLS behavior.
// Falls back to standard transport for non-HTTPS requests.
func NewUtlsHTTPClient(cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	baseClient := NewProxyAwareHTTPClient(context.Background(), cfg, auth, timeout)
	proxyURL := resolvedProxyURL(cfg, auth)
	rootCAs := customRootCAsForUTLS("utls")
	return newFingerprintHTTPClient(proxyURL, rootCAs, baseClient.Transport, timeout, anthropicFingerprintHosts, defaultTLSClientHelloID())
}

// NewCodexFingerprintHTTPClient creates an HTTP client that uses a Chrome TLS
// fingerprint for official Codex/OpenAI hosts while preserving injected context
// transports and normal proxy-aware behavior for custom base_url hosts.
func NewCodexFingerprintHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	baseClient := NewProxyAwareHTTPClient(ctx, cfg, auth, timeout)
	if shouldUseContextRoundTripperForFingerprintClient(ctx, cfg, auth) {
		return baseClient
	}

	proxyURL := resolvedProxyURL(cfg, auth)
	rootCAs := customRootCAsForUTLS("codex utls")
	return newFingerprintHTTPClient(proxyURL, rootCAs, baseClient.Transport, timeout, codexFingerprintHosts, codexTLSClientHelloID())
}

func newFingerprintHTTPClient(proxyURL string, rootCAs *x509.CertPool, fallback http.RoundTripper, timeout time.Duration, hosts map[string]struct{}, clientHello tls.ClientHelloID) *http.Client {
	proxyURL = strings.TrimSpace(proxyURL)
	transportKey := fingerprintTransportCacheKey{
		proxyURL:       proxyURL,
		rootCAKey:      customRootCAPoolKey(rootCAs),
		clientHelloKey: fingerprintClientHelloKey(clientHello),
		fallbackKey:    roundTripperCacheKey(fallback),
		hostsKey:       fingerprintHostsKey(hosts),
	}
	transport := cachedFingerprintTransport(transportKey, proxyURL, rootCAs, fallback, hosts, clientHello)
	clientKey := fingerprintHTTPClientCacheKey{
		transport: transportKey,
		timeout:   timeout,
	}
	return cachedFingerprintHTTPClient(clientKey, transport, timeout)
}

func shouldUseContextRoundTripperForFingerprintClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) bool {
	if contextRoundTripper(ctx) == nil {
		return false
	}
	if authProxyURL(auth) != "" {
		return true
	}
	return resolvedProxyURL(cfg, auth) == ""
}

func defaultTLSClientHelloID() tls.ClientHelloID {
	// Keep this explicit rather than HelloChrome_Auto so the effective
	// browser-version fingerprint does not silently change on dependency bumps.
	return tls.HelloChrome_133
}

func codexTLSClientHelloID() tls.ClientHelloID {
	return parseTLSClientHelloID(os.Getenv(codexTLSClientHelloEnvVar), defaultTLSClientHelloID())
}

func parseTLSClientHelloID(value string, fallback tls.ClientHelloID) tls.ClientHelloID {
	switch normalizeTLSClientHelloName(value) {
	case "":
		return fallback
	case "auto", "chromeauto":
		return tls.HelloChrome_Auto
	case "chrome133":
		return tls.HelloChrome_133
	case "chrome131":
		return tls.HelloChrome_131
	case "chrome120":
		return tls.HelloChrome_120
	case "chrome120pq":
		return tls.HelloChrome_120_PQ
	case "chrome115pq":
		return tls.HelloChrome_115_PQ
	default:
		log.Warnf("unsupported %s=%q, using default Chrome 133 TLS fingerprint", codexTLSClientHelloEnvVar, strings.TrimSpace(value))
		return fallback
	}
}

func normalizeTLSClientHelloName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "")
	value = strings.ReplaceAll(value, "_", "")
	value = strings.ReplaceAll(value, " ", "")
	return value
}

func customRootCAsForUTLS(logPrefix string) *x509.CertPool {
	pool, errCA := misc.CustomRootCAsFromEnv()
	if errCA != nil {
		log.Warnf("%s: custom CA disabled: %v", strings.TrimSpace(logPrefix), errCA)
		return nil
	}
	return pool
}

func cachedFingerprintTransport(
	key fingerprintTransportCacheKey,
	proxyURL string,
	rootCAs *x509.CertPool,
	fallback http.RoundTripper,
	hosts map[string]struct{},
	clientHello tls.ClientHelloID,
) http.RoundTripper {
	if cached, ok := fingerprintTransportCache.Load(key); ok {
		if transport, okTransport := cached.(http.RoundTripper); okTransport {
			return transport
		}
	}

	transport := http.RoundTripper(&fallbackRoundTripper{
		utls:               newUtlsRoundTripper(proxyURL, rootCAs, clientHello),
		fallback:           fallback,
		fingerprintedHosts: cloneFingerprintHosts(hosts),
	})
	actual, _ := fingerprintTransportCache.LoadOrStore(key, transport)
	if cached, ok := actual.(http.RoundTripper); ok {
		return cached
	}
	return transport
}

func cachedFingerprintHTTPClient(
	key fingerprintHTTPClientCacheKey,
	transport http.RoundTripper,
	timeout time.Duration,
) *http.Client {
	if cached, ok := fingerprintHTTPClientCache.Load(key); ok {
		if client, okClient := cached.(*http.Client); okClient {
			return client
		}
	}

	client := &http.Client{Transport: transport, Timeout: timeout}
	actual, _ := fingerprintHTTPClientCache.LoadOrStore(key, client)
	if cached, ok := actual.(*http.Client); ok {
		return cached
	}
	return client
}

func fingerprintClientHelloKey(clientHello tls.ClientHelloID) string {
	return fmt.Sprintf("%s:%s:%d", clientHello.Client, clientHello.Version, clientHello.Seed)
}

func roundTripperCacheKey(rt http.RoundTripper) string {
	if rt == nil {
		return ""
	}
	value := reflect.ValueOf(rt)
	switch value.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Chan, reflect.Func, reflect.Slice, reflect.UnsafePointer:
		return fmt.Sprintf("%T:%x", rt, value.Pointer())
	default:
		return fmt.Sprintf("%T:%v", rt, rt)
	}
}

func fingerprintHostsKey(hosts map[string]struct{}) string {
	if len(hosts) == 0 {
		return ""
	}
	names := make([]string, 0, len(hosts))
	for host := range hosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if host != "" {
			names = append(names, host)
		}
	}
	sort.Strings(names)
	return strconv.Itoa(len(names)) + ":" + strings.Join(names, ",")
}

func cloneFingerprintHosts(hosts map[string]struct{}) map[string]struct{} {
	if len(hosts) == 0 {
		return map[string]struct{}{}
	}
	cloned := make(map[string]struct{}, len(hosts))
	for host := range hosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if host != "" {
			cloned[host] = struct{}{}
		}
	}
	return cloned
}
